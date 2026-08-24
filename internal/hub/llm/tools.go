package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dx111ge/homelabmon/internal/agent/integrations"
	"github.com/dx111ge/homelabmon/internal/agent/scanners"
	"github.com/dx111ge/homelabmon/internal/mesh"
	"github.com/dx111ge/homelabmon/internal/models"
	"github.com/dx111ge/homelabmon/internal/notify"
	"github.com/dx111ge/homelabmon/internal/plugin"
	"github.com/dx111ge/homelabmon/internal/store"
	"github.com/spf13/viper"
)

// DockerRouter routes container control calls to the node that owns a host.
type DockerRouter interface {
	DockerControl(ctx context.Context, hostID, containerID, action string) error
}

// ExecRouter runs commands on nodes (implemented by mesh.PeerClient).
type ExecRouter interface {
	Exec(ctx context.Context, hostID, command, shell string, timeoutSec int) (*mesh.ExecResult, error)
}

// Notifier dispatches notifications (implemented by notify.Dispatcher).
type Notifier interface {
	Send(n notify.Notification)
	HasSenders() bool
	SetSenders(senders []notify.Sender)
}

// ToolExecutor executes tool calls against the platform: CMDB queries,
// settings, notifications, scans, and management actions on any mesh node.
type ToolExecutor struct {
	store      *store.Store
	identity   *models.NodeIdentity
	docker     DockerRouter
	execRouter ExecRouter
	scanFunc   func() (int, error)
	notifier   Notifier
}

func NewToolExecutor(s *store.Store, identity *models.NodeIdentity) *ToolExecutor {
	return &ToolExecutor{store: s, identity: identity}
}

// SetDockerRouter enables container management (local + remote via mesh).
func (e *ToolExecutor) SetDockerRouter(r DockerRouter) { e.docker = r }

// SetExecRouter enables remote command execution across the mesh.
func (e *ToolExecutor) SetExecRouter(r ExecRouter) { e.execRouter = r }

// SetScanFunc enables on-demand network scans.
func (e *ToolExecutor) SetScanFunc(f func() (int, error)) { e.scanFunc = f }

// SetNotifier enables sending notifications.
func (e *ToolExecutor) SetNotifier(n Notifier) { e.notifier = n }

const confirmHint = `"hint":"this action is destructive; ask the user to confirm, then call again with confirm=true"`

var validDeviceTypes = []string{"server", "desktop", "laptop", "phone", "tablet", "tv",
	"media", "iot", "printer", "camera", "router", "switch", "ap", "nas", "other"}

// ToolDefinitions returns the tools available to the LLM.
func ToolDefinitions() []Tool {
	return []Tool{
		// ---- Read tools ----
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "list_hosts",
				Description: "List all known hosts (servers, devices) with their status, OS, IP, and type. Use this to see what's on the network.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","description":"Filter by status: online, offline, or all (default: all)","enum":["online","offline","all"]},"monitor_type":{"type":"string","description":"Filter by type: agent, passive, or all (default: all)","enum":["agent","passive","all"]}}}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "get_host",
				Description: "Get detailed information about a specific host by hostname or ID, including hardware specs, OS, IPs.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"hostname":{"type":"string","description":"Hostname to look up (case-insensitive partial match)"}},"required":["hostname"]}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "get_metrics",
				Description: "Get current or historical CPU, memory, disk, and network metrics for a host.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"hostname":{"type":"string","description":"Hostname to get metrics for"},"hours":{"type":"integer","description":"Hours of history (default: 1, max: 168)"}},"required":["hostname"]}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "list_services",
				Description: "List all discovered services (Docker containers, web servers, databases, etc.) across all hosts or for a specific host.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"hostname":{"type":"string","description":"Filter by hostname (optional)"},"category":{"type":"string","description":"Filter by category: container, web, database, media, monitoring, etc. (optional)"}}}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "get_summary",
				Description: "Get a high-level summary of the entire homelab: host counts, online/offline, service counts, resource usage.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "list_docker_containers",
				Description: "List all discovered Docker containers across the mesh with their host, image, compose stack, health, ports, and container ID. Use this before docker_control to find the exact container name.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"hostname":{"type":"string","description":"Filter by hostname (optional)"}}}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "list_peers",
				Description: "List the mesh peers this node exchanges heartbeats with: addresses, status, version, and site labels.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "list_integrations",
				Description: "List configured external integrations (FRITZ!Box, Unifi, Home Assistant, Pi-hole, pfSense) with their status. New integrations with credentials are added via the Settings UI.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "get_settings",
				Description: "Get the current platform settings: alert thresholds, retention, scan interval, notification channels, site label, and this node's identity.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			},
		},
		// ---- Management tools ----
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "docker_control",
				Description: "Start, stop, or restart a Docker container on any host in the mesh. Resolve the exact container name or ID with list_docker_containers first. stop and restart are disruptive and require explicit user confirmation.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"container":{"type":"string","description":"Container name, image, or container ID (partial match)"},"hostname":{"type":"string","description":"Host the container runs on (optional if the container name is unique across the mesh)"},"action":{"type":"string","description":"Action to perform","enum":["start","stop","restart"]},"confirm":{"type":"boolean","description":"Must be true for stop/restart, and only after the user explicitly agreed"}},"required":["container","action"]}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "trigger_network_scan",
				Description: "Run an on-demand network scan (ARP + mDNS) now to discover passive devices (phones, TVs, IoT). Takes 30-60 seconds; returns the number of devices found.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "send_notification",
				Description: "Send a push notification through the configured channels (ntfy.sh / webhook). Use severity: info, warning, or critical.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","description":"Short notification title"},"message":{"type":"string","description":"Notification body"},"severity":{"type":"string","description":"Severity level","enum":["info","warning","critical"]}},"required":["title","message"]}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "update_settings",
				Description: "Update platform settings: alert thresholds (percent), metric retention (days, 0 = forever), scan interval (seconds, min 60), notification URLs, or this node's site label. Only provided fields are changed.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"cpu_threshold":{"type":"number","description":"CPU alert threshold in percent (0-100)"},"mem_threshold":{"type":"number","description":"Memory alert threshold in percent (0-100)"},"disk_threshold":{"type":"number","description":"Disk alert threshold in percent (0-100)"},"retention_days":{"type":"integer","description":"Days to keep metric history (0 = forever)"},"scan_interval":{"type":"integer","description":"Network scan interval in seconds (min 60)"},"ntfy_url":{"type":"string","description":"ntfy.sh topic URL (empty string removes it)"},"webhook_url":{"type":"string","description":"Webhook URL for Discord/Slack/custom (empty string removes it)"},"site":{"type":"string","description":"Site label for multi-site federation (e.g. home, cloud)"}}}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "rename_host",
				Description: "Set a friendly display name for a host or discovered device.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"hostname":{"type":"string","description":"Current hostname, display name, or ID"},"new_name":{"type":"string","description":"New display name"}},"required":["hostname","new_name"]}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "set_device_type",
				Description: "Correct the classification of a discovered device.",
				Parameters:  json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"hostname":{"type":"string","description":"Hostname, display name, or ID of the device"},"device_type":{"type":"string","description":"Device type","enum":%s}},"required":["hostname","device_type"]}`, mustJSON(validDeviceTypes))),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "delete_host",
				Description: "Remove a host or device from the CMDB. Agent hosts will reappear with their next heartbeat; passive devices reappear when seen again by a scan. Destructive: requires explicit user confirmation.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"hostname":{"type":"string","description":"Hostname, display name, or ID of the host to remove"},"confirm":{"type":"boolean","description":"Must be true, and only after the user explicitly agreed"}},"required":["hostname","confirm"]}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "manage_integration",
				Description: "Manage an external integration: test its connection, sync its devices now, or delete it. Deleting is destructive and requires explicit user confirmation.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","description":"Action to perform","enum":["test","sync","delete"]},"name":{"type":"string","description":"Integration name or ID (partial match)"},"confirm":{"type":"boolean","description":"Must be true for delete, and only after the user explicitly agreed"}},"required":["action","name"]}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "check_vendors",
				Description: "Re-resolve MAC address vendors and device classifications for discovered devices that are missing them.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "add_peer",
				Description: "Connect this node to another homelabmon node to form or extend the mesh. The new peer is contacted on the next heartbeat (within ~60 seconds); further peers are discovered automatically via gossip.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"address":{"type":"string","description":"Peer address as host:port (e.g. 192.168.1.50:9600)"}},"required":["address"]}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "run_command",
				Description: "Run a shell command on any agent node in the mesh (Linux, Windows, macOS, FreeBSD/OPNsense) and get stdout, stderr, and the exit code. Linux/macOS/BSD run /bin/sh; Windows runs cmd.exe (shell=\"powershell\" for PowerShell). Requires explicit user confirmation for EVERY command: show the exact command and target host first. Use for devops tasks: package upgrades, service checks, logs, config inspection.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"The shell command to run"},"hostname":{"type":"string","description":"Target host (optional, defaults to the local node). For fleet-wide tasks, call this once per host."},"shell":{"type":"string","description":"Windows only: cmd (default) or powershell","enum":["cmd","powershell"]},"timeout_seconds":{"type":"integer","description":"Timeout in seconds (default 120, max 600; use higher for upgrades)"},"confirm":{"type":"boolean","description":"Must be true, and only after the user explicitly approved the exact command"}},"required":["command","confirm"]}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "list_exec_history",
				Description: "Show recent remote command executions: command, host, exit code, duration, time. Useful to review what was already done (e.g. during an upgrade session).",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"hostname":{"type":"string","description":"Filter by host (optional)"},"limit":{"type":"integer","description":"Number of entries (default 20, max 200)"}}}`),
			},
		},
	}
}

// Execute runs a tool call and returns the result as a string.
func (e *ToolExecutor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	// Read tools
	case "list_hosts":
		return e.listHosts(ctx, args)
	case "get_host":
		return e.getHost(ctx, args)
	case "get_metrics":
		return e.getMetrics(ctx, args)
	case "list_services":
		return e.listServices(ctx, args)
	case "get_summary":
		return e.getSummary(ctx)
	case "list_docker_containers":
		return e.listDockerContainers(ctx, args)
	case "list_peers":
		return e.listPeers(ctx)
	case "list_integrations":
		return e.listIntegrations(ctx)
	case "get_settings":
		return e.getSettings()
	// Management tools
	case "docker_control":
		return e.dockerControl(ctx, args)
	case "trigger_network_scan":
		return e.triggerScan()
	case "send_notification":
		return e.sendNotification(args)
	case "update_settings":
		return e.updateSettings(ctx, args)
	case "rename_host":
		return e.renameHost(ctx, args)
	case "set_device_type":
		return e.setDeviceType(ctx, args)
	case "delete_host":
		return e.deleteHost(ctx, args)
	case "manage_integration":
		return e.manageIntegration(ctx, args)
	case "check_vendors":
		return e.checkVendors(ctx)
	case "add_peer":
		return e.addPeer(ctx, args)
	case "run_command":
		return e.runCommand(ctx, args)
	case "list_exec_history":
		return e.listExecHistory(ctx, args)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// ---------- read tools ----------

func (e *ToolExecutor) listHosts(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Status      string `json:"status"`
		MonitorType string `json:"monitor_type"`
	}
	json.Unmarshal(args, &params)

	hosts, err := e.store.ListHosts(ctx)
	if err != nil {
		return "", err
	}

	type hostSummary struct {
		Hostname    string   `json:"hostname"`
		DisplayName string   `json:"display_name,omitempty"`
		Status      string   `json:"status"`
		MonitorType string   `json:"monitor_type"`
		DeviceType  string   `json:"device_type"`
		OS          string   `json:"os,omitempty"`
		IPs         []string `json:"ips,omitempty"`
		Vendor      string   `json:"vendor,omitempty"`
		LastSeen    string   `json:"last_seen"`
	}

	var result []hostSummary
	for _, h := range hosts {
		if params.Status != "" && params.Status != "all" && h.Status != params.Status {
			continue
		}
		if params.MonitorType != "" && params.MonitorType != "all" && h.MonitorType != params.MonitorType {
			continue
		}
		result = append(result, hostSummary{
			Hostname:    h.Hostname,
			DisplayName: h.DisplayName,
			Status:      h.Status,
			MonitorType: h.MonitorType,
			DeviceType:  h.DeviceType,
			OS:          h.OS,
			IPs:         h.IPAddresses,
			Vendor:      h.Vendor,
			LastSeen:    h.LastSeen.Format("2006-01-02 15:04:05"),
		})
	}

	return string(mustJSON(result)), nil
}

func (e *ToolExecutor) getHost(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Hostname string `json:"hostname"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	h := e.findHost(ctx, params.Hostname)
	if h == nil {
		return fmt.Sprintf(`{"error":"host %q not found"}`, params.Hostname), nil
	}
	return string(mustJSON(h)), nil
}

func (e *ToolExecutor) getMetrics(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Hostname string `json:"hostname"`
		Hours    int    `json:"hours"`
	}
	json.Unmarshal(args, &params)
	if params.Hours <= 0 {
		params.Hours = 1
	}
	if params.Hours > 168 {
		params.Hours = 168
	}

	h := e.findHost(ctx, params.Hostname)
	if h == nil {
		return fmt.Sprintf(`{"error":"host %q not found"}`, params.Hostname), nil
	}

	if params.Hours <= 1 {
		m, err := e.store.GetLatestMetric(ctx, h.ID)
		if err != nil {
			return `{"error":"no metrics available"}`, nil
		}
		type metricResult struct {
			CollectedAt string      `json:"collected_at"`
			CPUPercent  float64     `json:"cpu_percent"`
			MemPercent  float64     `json:"mem_percent"`
			MemUsedGB   float64     `json:"mem_used_gb"`
			MemTotalGB  float64     `json:"mem_total_gb"`
			NetSentMB   float64     `json:"net_sent_mb"`
			NetRecvMB   float64     `json:"net_recv_mb"`
			Disks       interface{} `json:"disks"`
		}
		r := metricResult{
			CollectedAt: m.CollectedAt.Format("2006-01-02 15:04:05"),
			CPUPercent:  m.CPUPercent,
			MemPercent:  m.MemPercent,
			MemUsedGB:   float64(m.MemUsed) / (1024 * 1024 * 1024),
			MemTotalGB:  float64(m.MemTotal) / (1024 * 1024 * 1024),
			NetSentMB:   float64(m.NetBytesSent) / (1024 * 1024),
			NetRecvMB:   float64(m.NetBytesRecv) / (1024 * 1024),
			Disks:       m.Disks(),
		}
		return string(mustJSON(r)), nil
	}

	metrics, err := e.store.GetMetricHistory(ctx, h.ID, params.Hours)
	if err != nil || len(metrics) == 0 {
		return `{"error":"no metrics in time range"}`, nil
	}

	var avgCPU, avgMem, maxCPU, maxMem float64
	for _, m := range metrics {
		avgCPU += m.CPUPercent
		avgMem += m.MemPercent
		if m.CPUPercent > maxCPU {
			maxCPU = m.CPUPercent
		}
		if m.MemPercent > maxMem {
			maxMem = m.MemPercent
		}
	}
	avgCPU /= float64(len(metrics))
	avgMem /= float64(len(metrics))

	type historySummary struct {
		DataPoints int     `json:"data_points"`
		Hours      int     `json:"hours"`
		AvgCPU     float64 `json:"avg_cpu_percent"`
		MaxCPU     float64 `json:"max_cpu_percent"`
		AvgMem     float64 `json:"avg_mem_percent"`
		MaxMem     float64 `json:"max_mem_percent"`
		FirstAt    string  `json:"first_at"`
		LastAt     string  `json:"last_at"`
	}
	r := historySummary{
		DataPoints: len(metrics),
		Hours:      params.Hours,
		AvgCPU:     avgCPU,
		MaxCPU:     maxCPU,
		AvgMem:     avgMem,
		MaxMem:     maxMem,
		FirstAt:    metrics[0].CollectedAt.Format("2006-01-02 15:04:05"),
		LastAt:     metrics[len(metrics)-1].CollectedAt.Format("2006-01-02 15:04:05"),
	}
	return string(mustJSON(r)), nil
}

func (e *ToolExecutor) listServices(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Hostname string `json:"hostname"`
		Category string `json:"category"`
	}
	json.Unmarshal(args, &params)

	type svcSummary struct {
		Name         string `json:"name"`
		Port         int    `json:"port"`
		Category     string `json:"category"`
		Source       string `json:"source"`
		Status       string `json:"status"`
		Host         string `json:"host"`
		ContainerImg string `json:"container_image,omitempty"`
	}
	var services []svcSummary

	hosts, _ := e.store.ListHosts(ctx)
	hostNames := make(map[string]string)
	for _, h := range hosts {
		hostNames[h.ID] = h.Hostname
	}

	allSvcs, err := e.store.ListAllServices(ctx)
	if err != nil {
		return "", err
	}

	for _, s := range allSvcs {
		if s.Category == "unknown" {
			continue
		}
		hostName := hostNames[s.HostID]
		if params.Hostname != "" && !containsCI(hostName, params.Hostname) {
			continue
		}
		if params.Category != "" && s.Category != params.Category {
			continue
		}
		services = append(services, svcSummary{
			Name:         s.Name,
			Port:         s.Port,
			Category:     s.Category,
			Source:       s.Source,
			Status:       s.Status,
			Host:         hostName,
			ContainerImg: s.ContainerImg,
		})
	}

	return string(mustJSON(services)), nil
}

func (e *ToolExecutor) getSummary(ctx context.Context) (string, error) {
	hosts, _ := e.store.ListHosts(ctx)

	var agentCount, passiveCount, onlineCount, offlineCount int
	for _, h := range hosts {
		switch h.MonitorType {
		case "agent":
			agentCount++
		case "passive":
			passiveCount++
		}
		switch h.Status {
		case "online":
			onlineCount++
		case "offline":
			offlineCount++
		}
	}

	allSvcs, _ := e.store.ListAllServices(ctx)
	var activeServices int
	categories := make(map[string]int)
	for _, s := range allSvcs {
		if s.Status == "active" && s.Category != "unknown" {
			activeServices++
			categories[s.Category]++
		}
	}

	type summary struct {
		TotalHosts     int            `json:"total_hosts"`
		AgentNodes     int            `json:"agent_nodes"`
		PassiveDevices int            `json:"passive_devices"`
		Online         int            `json:"online"`
		Offline        int            `json:"offline"`
		ActiveServices int            `json:"active_services"`
		ServicesByType map[string]int `json:"services_by_type"`
	}

	r := summary{
		TotalHosts:     len(hosts),
		AgentNodes:     agentCount,
		PassiveDevices: passiveCount,
		Online:         onlineCount,
		Offline:        offlineCount,
		ActiveServices: activeServices,
		ServicesByType: categories,
	}
	return string(mustJSON(r)), nil
}

func (e *ToolExecutor) listDockerContainers(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Hostname string `json:"hostname"`
	}
	json.Unmarshal(args, &params)

	hosts, _ := e.store.ListHosts(ctx)
	hostByID := make(map[string]models.Host)
	for _, h := range hosts {
		hostByID[h.ID] = h
	}

	var hostID string
	if params.Hostname != "" {
		h := e.findHost(ctx, params.Hostname)
		if h == nil {
			return fmt.Sprintf(`{"error":"host %q not found"}`, params.Hostname), nil
		}
		hostID = h.ID
	}

	allSvcs, err := e.store.ListAllServices(ctx)
	if err != nil {
		return "", err
	}

	type containerInfo struct {
		Name        string `json:"name"`
		ContainerID string `json:"container_id"`
		Image       string `json:"image"`
		Stack       string `json:"stack,omitempty"`
		Health      string `json:"health,omitempty"`
		Status      string `json:"status"`
		Ports       []int  `json:"ports"`
		Host        string `json:"host"`
	}

	byContainer := make(map[string]*containerInfo)
	var order []string
	for _, s := range allSvcs {
		if s.Source != "docker" {
			continue
		}
		if hostID != "" && s.HostID != hostID {
			continue
		}
		host := hostByID[s.HostID]
		ci, ok := byContainer[s.ContainerID]
		if !ok {
			ci = &containerInfo{
				Name:        s.Name,
				ContainerID: s.ContainerID,
				Image:       s.ContainerImg,
				Stack:       s.Stack,
				Health:      s.Health,
				Status:      s.Status,
				Host:        host.Hostname,
			}
			byContainer[s.ContainerID] = ci
			order = append(order, s.ContainerID)
		}
		if s.Port != 0 {
			ci.Ports = append(ci.Ports, s.Port)
		}
	}

	var result []*containerInfo
	for _, id := range order {
		result = append(result, byContainer[id])
	}
	return string(mustJSON(result)), nil
}

func (e *ToolExecutor) listPeers(ctx context.Context) (string, error) {
	peers, err := e.store.ListPeers(ctx)
	if err != nil {
		return "", err
	}
	type peerInfo struct {
		ID       string `json:"id"`
		Hostname string `json:"hostname,omitempty"`
		Address  string `json:"address"`
		Status   string `json:"status"`
		Version  string `json:"version,omitempty"`
		Site     string `json:"site,omitempty"`
		LastSeen string `json:"last_seen"`
		IsLocal  bool   `json:"is_local_node"`
	}
	var result []peerInfo
	result = append(result, peerInfo{
		ID:       e.identity.ID,
		Hostname: e.identity.Hostname,
		Address:  e.identity.BindAddr,
		Status:   "online",
		Version:  e.identity.Version,
		Site:     e.identity.Site,
		LastSeen: time.Now().UTC().Format("2006-01-02 15:04:05"),
		IsLocal:  true,
	})
	for _, p := range peers {
		last := ""
		if p.LastHeartbeat != nil {
			last = p.LastHeartbeat.Format("2006-01-02 15:04:05")
		}
		result = append(result, peerInfo{
			ID:       p.ID,
			Hostname: p.Hostname,
			Address:  p.Address,
			Status:   p.Status,
			Version:  p.Version,
			Site:     p.Site,
			LastSeen: last,
		})
	}
	return string(mustJSON(result)), nil
}

func (e *ToolExecutor) listIntegrations(ctx context.Context) (string, error) {
	igs, err := e.store.ListIntegrations(ctx)
	if err != nil {
		return "", err
	}
	type igInfo struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Name     string `json:"name"`
		URL      string `json:"url"`
		Enabled  bool   `json:"enabled"`
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
		LastSync string `json:"last_sync,omitempty"`
	}
	var result []igInfo
	for _, ig := range igs {
		info := igInfo{
			ID:      ig.ID,
			Type:    ig.Type,
			Name:    ig.Name,
			URL:     ig.Config["url"],
			Enabled: ig.Enabled,
			Status:  ig.Status,
			Error:   ig.Error,
		}
		if ig.LastSync != nil {
			info.LastSync = ig.LastSync.Format("2006-01-02 15:04:05")
		}
		result = append(result, info)
	}
	return string(mustJSON(result)), nil
}

func (e *ToolExecutor) getSettings() (string, error) {
	s := struct {
		NodeID             string  `json:"node_id"`
		Hostname           string  `json:"hostname"`
		Site               string  `json:"site"`
		Version            string  `json:"version"`
		ScanEnabled        bool    `json:"scan_enabled"`
		ScanIntervalSec    int     `json:"scan_interval_sec"`
		CollectIntervalSec int     `json:"collect_interval_sec"`
		RetentionDays      int     `json:"retention_days"`
		CPUThreshold       float64 `json:"cpu_threshold_percent"`
		MemThreshold       float64 `json:"mem_threshold_percent"`
		DiskThreshold      float64 `json:"disk_threshold_percent"`
		NtfyURL            string  `json:"ntfy_url,omitempty"`
		WebhookURL         string  `json:"webhook_url,omitempty"`
	}{
		NodeID:             e.identity.ID,
		Hostname:           e.identity.Hostname,
		Site:               viper.GetString("site"),
		Version:            e.identity.Version,
		ScanEnabled:        e.scanFunc != nil,
		ScanIntervalSec:    viper.GetInt("scan-interval"),
		CollectIntervalSec: viper.GetInt("collect-interval"),
		RetentionDays:      viper.GetInt("retention-days"),
		CPUThreshold:       viper.GetFloat64("notify-cpu-threshold"),
		MemThreshold:       viper.GetFloat64("notify-mem-threshold"),
		DiskThreshold:      viper.GetFloat64("notify-disk-threshold"),
		NtfyURL:            viper.GetString("notify-ntfy"),
		WebhookURL:         viper.GetString("notify-webhook"),
	}
	return string(mustJSON(s)), nil
}

// ---------- management tools ----------

func (e *ToolExecutor) dockerControl(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Container string `json:"container"`
		Hostname  string `json:"hostname"`
		Action    string `json:"action"`
		Confirm   bool   `json:"confirm"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	switch params.Action {
	case "start", "stop", "restart":
	default:
		return fmt.Sprintf(`{"error":"invalid action %q (use start, stop, or restart)"}`, params.Action), nil
	}

	if e.docker == nil {
		return `{"error":"docker management is not available on this node (no Docker daemon detected)"}`, nil
	}

	if params.Action != "start" && !params.Confirm {
		return fmt.Sprintf(`{"error":"confirmation required for %s",%s}`, params.Action, confirmHint), nil
	}

	// Resolve the target host if one was named
	var hostID string
	if params.Hostname != "" {
		h := e.findHost(ctx, params.Hostname)
		if h == nil {
			return fmt.Sprintf(`{"error":"host %q not found"}`, params.Hostname), nil
		}
		hostID = h.ID
	}

	// Resolve the container against known docker services
	allSvcs, err := e.store.ListAllServices(ctx)
	if err != nil {
		return "", err
	}
	hosts, _ := e.store.ListHosts(ctx)
	hostNames := make(map[string]string)
	for _, h := range hosts {
		hostNames[h.ID] = h.Hostname
	}

	type match struct {
		hostID, name, id, image string
		active                  bool
	}
	var matches []match
	seen := make(map[string]bool)
	for _, s := range allSvcs {
		if s.Source != "docker" || s.ContainerID == "" {
			continue
		}
		if hostID != "" && s.HostID != hostID {
			continue
		}
		if !containsCI(s.Name, params.Container) &&
			!strings.HasPrefix(s.ContainerID, params.Container) &&
			!containsCI(s.ContainerImg, params.Container) {
			continue
		}
		if seen[s.ContainerID] {
			continue
		}
		seen[s.ContainerID] = true
		matches = append(matches, match{
			hostID: s.HostID,
			name:   s.Name,
			id:     s.ContainerID,
			image:  s.ContainerImg,
			active: s.Status == "active",
		})
	}

	if len(matches) == 0 {
		return fmt.Sprintf(`{"error":"no Docker container matching %q found. Use list_docker_containers to see what exists"}`, params.Container), nil
	}
	if len(matches) > 1 {
		var names []string
		for _, m := range matches {
			names = append(names, fmt.Sprintf("%s on %s", m.name, hostNames[m.hostID]))
		}
		return fmt.Sprintf(`{"error":"container name %q is ambiguous","matches":%s,"hint":"narrow it down with the hostname parameter"}`, params.Container, mustJSON(names)), nil
	}

	m := matches[0]
	if err := e.docker.DockerControl(ctx, m.hostID, m.id, params.Action); err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error()), nil
	}
	return string(mustJSON(map[string]interface{}{
		"ok":         true,
		"action":     params.Action,
		"container":  m.name,
		"image":      m.image,
		"host":       hostNames[m.hostID],
		"was_active": m.active,
	})), nil
}

func (e *ToolExecutor) triggerScan() (string, error) {
	if e.scanFunc == nil {
		return `{"error":"network scanning is not enabled on this node (start the agent with --scan)"}`, nil
	}
	count, err := e.scanFunc()
	if err != nil {
		return fmt.Sprintf(`{"error":"scan failed: %s"}`, err.Error()), nil
	}
	return string(mustJSON(map[string]interface{}{
		"ok":      true,
		"devices": count,
		"note":    "devices discovered or updated by the scan",
	})), nil
}

func (e *ToolExecutor) sendNotification(args json.RawMessage) (string, error) {
	var params struct {
		Title    string `json:"title"`
		Message  string `json:"message"`
		Severity string `json:"severity"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}
	if params.Title == "" || params.Message == "" {
		return `{"error":"title and message are required"}`, nil
	}
	if e.notifier == nil || !e.notifier.HasSenders() {
		return `{"error":"no notification channels configured. Set an ntfy or webhook URL via update_settings or the Settings page"}`, nil
	}
	switch params.Severity {
	case "", notify.SeverityInfo, notify.SeverityWarning, notify.SeverityCritical:
	default:
		return fmt.Sprintf(`{"error":"invalid severity %q (use info, warning, or critical)"}`, params.Severity), nil
	}
	if params.Severity == "" {
		params.Severity = notify.SeverityInfo
	}
	e.notifier.Send(notify.Notification{
		Title:    params.Title,
		Message:  params.Message,
		Severity: params.Severity,
		Category: "chat",
	})
	return `{"ok":true,"note":"sent to all configured channels; identical notifications are deduplicated for 10 minutes"}`, nil
}

func (e *ToolExecutor) updateSettings(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		CPUThreshold  *float64 `json:"cpu_threshold"`
		MemThreshold  *float64 `json:"mem_threshold"`
		DiskThreshold *float64 `json:"disk_threshold"`
		RetentionDays *int     `json:"retention_days"`
		ScanInterval  *int     `json:"scan_interval"`
		NtfyURL       *string  `json:"ntfy_url"`
		WebhookURL    *string  `json:"webhook_url"`
		Site          *string  `json:"site"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	var applied []string

	setThreshold := func(name string, v *float64) error {
		if v == nil {
			return nil
		}
		if *v < 0 || *v > 100 {
			return fmt.Errorf("%s must be between 0 and 100", name)
		}
		viper.Set(name, *v)
		if err := e.store.SetSetting(ctx, name, strconv.FormatFloat(*v, 'f', -1, 64)); err != nil {
			return err
		}
		applied = append(applied, fmt.Sprintf("%s=%v%%", name, *v))
		return nil
	}
	if err := setThreshold("notify-cpu-threshold", params.CPUThreshold); err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error()), nil
	}
	if err := setThreshold("notify-mem-threshold", params.MemThreshold); err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error()), nil
	}
	if err := setThreshold("notify-disk-threshold", params.DiskThreshold); err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error()), nil
	}

	if params.RetentionDays != nil {
		if *params.RetentionDays < 0 {
			return `{"error":"retention_days must be >= 0 (0 = keep forever)"}`, nil
		}
		viper.Set("retention-days", *params.RetentionDays)
		e.store.SetSetting(ctx, "retention-days", strconv.Itoa(*params.RetentionDays))
		applied = append(applied, fmt.Sprintf("retention-days=%d", *params.RetentionDays))
	}
	if params.ScanInterval != nil {
		if *params.ScanInterval < 60 {
			return `{"error":"scan_interval must be at least 60 seconds"}`, nil
		}
		viper.Set("scan-interval", *params.ScanInterval)
		e.store.SetSetting(ctx, "scan-interval", strconv.Itoa(*params.ScanInterval))
		applied = append(applied, fmt.Sprintf("scan-interval=%ds", *params.ScanInterval))
	}

	rebuildSenders := params.NtfyURL != nil || params.WebhookURL != nil
	if params.NtfyURL != nil {
		url := strings.TrimSpace(*params.NtfyURL)
		viper.Set("notify-ntfy", url)
		e.store.SetSetting(ctx, "notify-ntfy", url)
		applied = append(applied, "notify-ntfy="+displayURL(url))
	}
	if params.WebhookURL != nil {
		url := strings.TrimSpace(*params.WebhookURL)
		viper.Set("notify-webhook", url)
		e.store.SetSetting(ctx, "notify-webhook", url)
		applied = append(applied, "notify-webhook="+displayURL(url))
	}
	if params.Site != nil {
		site := strings.TrimSpace(*params.Site)
		viper.Set("site", site)
		e.store.SetSetting(ctx, "site", site)
		e.identity.Site = site
		applied = append(applied, "site="+site)
	}

	if rebuildSenders && e.notifier != nil {
		var senders []notify.Sender
		if url := viper.GetString("notify-ntfy"); url != "" {
			senders = append(senders, notify.NewNtfySender(url))
		}
		if url := viper.GetString("notify-webhook"); url != "" {
			senders = append(senders, notify.NewWebhookSender(url))
		}
		e.notifier.SetSenders(senders)
	}

	if len(applied) == 0 {
		return `{"ok":true,"note":"nothing to change: no recognized settings provided"}`, nil
	}
	return string(mustJSON(map[string]interface{}{"ok": true, "applied": applied})), nil
}

func (e *ToolExecutor) renameHost(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Hostname string `json:"hostname"`
		NewName  string `json:"new_name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}
	if params.NewName == "" {
		return `{"error":"new_name is required"}`, nil
	}
	h := e.findHost(ctx, params.Hostname)
	if h == nil {
		return fmt.Sprintf(`{"error":"host %q not found"}`, params.Hostname), nil
	}
	if err := e.store.RenameHost(ctx, h.ID, params.NewName); err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error()), nil
	}
	return string(mustJSON(map[string]string{"ok": "true", "host": h.Label(), "display_name": params.NewName})), nil
}

func (e *ToolExecutor) setDeviceType(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Hostname   string `json:"hostname"`
		DeviceType string `json:"device_type"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}
	if !contains(validDeviceTypes, params.DeviceType) {
		return fmt.Sprintf(`{"error":"invalid device_type %q","valid_types":%s}`, params.DeviceType, mustJSON(validDeviceTypes)), nil
	}
	h := e.findHost(ctx, params.Hostname)
	if h == nil {
		return fmt.Sprintf(`{"error":"host %q not found"}`, params.Hostname), nil
	}
	if err := e.store.UpdateHostDeviceType(ctx, h.ID, params.DeviceType); err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error()), nil
	}
	return string(mustJSON(map[string]string{"ok": "true", "host": h.Label(), "device_type": params.DeviceType})), nil
}

func (e *ToolExecutor) deleteHost(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Hostname string `json:"hostname"`
		Confirm  bool   `json:"confirm"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}
	if !params.Confirm {
		return fmt.Sprintf(`{"error":"confirmation required",%s}`, confirmHint), nil
	}
	h := e.findHost(ctx, params.Hostname)
	if h == nil {
		return fmt.Sprintf(`{"error":"host %q not found"}`, params.Hostname), nil
	}
	if err := e.store.DeleteHost(ctx, h.ID); err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error()), nil
	}
	note := "passive device removed; it will reappear if a scan sees it again"
	if h.MonitorType == "agent" {
		note = "agent host removed; it will reappear with its next heartbeat unless the node was retired"
	}
	return string(mustJSON(map[string]string{"ok": "true", "deleted": h.Label(), "note": note})), nil
}

func (e *ToolExecutor) manageIntegration(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Action  string `json:"action"`
		Name    string `json:"name"`
		Confirm bool   `json:"confirm"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	igs, err := e.store.ListIntegrations(ctx)
	if err != nil {
		return "", err
	}
	var found *store.Integration
	for i := range igs {
		if igs[i].ID == params.Name || containsCI(igs[i].Name, params.Name) || containsCI(igs[i].ID, params.Name) {
			found = &igs[i]
			break
		}
	}
	if found == nil {
		return fmt.Sprintf(`{"error":"no integration matching %q. Use list_integrations to see what is configured"}`, params.Name), nil
	}

	switch params.Action {
	case "delete":
		if !params.Confirm {
			return fmt.Sprintf(`{"error":"confirmation required for delete",%s}`, confirmHint), nil
		}
		e.store.DeleteSecret(ctx, store.SecretKeyID(found.ID, "password"))
		if err := e.store.DeleteIntegration(ctx, found.ID); err != nil {
			return fmt.Sprintf(`{"error":"%s"}`, err.Error()), nil
		}
		return string(mustJSON(map[string]string{"ok": "true", "deleted": found.Name})), nil

	case "test", "sync":
		password, _ := e.store.GetSecret(ctx, store.SecretKeyID(found.ID, "password"))
		url := found.Config["url"]
		username := found.Config["username"]

		if params.Action == "test" {
			var pingErr error
			switch found.Type {
			case "fritzbox":
				pingErr = integrations.NewFritzBox(url, username, password).Ping(ctx)
			case "unifi":
				pingErr = integrations.NewUnifi(url, username, password).Ping(ctx)
			case "homeassistant":
				pingErr = integrations.NewHomeAssistant(url, password).Ping(ctx)
			case "pihole":
				pingErr = integrations.NewPiHole(url, password).Ping(ctx)
			case "pfsense":
				pingErr = integrations.NewPfSense(url, username, password).Ping(ctx)
			default:
				return fmt.Sprintf(`{"error":"unknown integration type %q"}`, found.Type), nil
			}
			if pingErr != nil {
				e.store.UpdateIntegrationStatus(ctx, found.ID, "error", pingErr.Error())
				return fmt.Sprintf(`{"ok":false,"integration":%q,"error":"%s"}`, found.Name, pingErr.Error()), nil
			}
			e.store.UpdateIntegrationStatus(ctx, found.ID, "ok", "")
			return string(mustJSON(map[string]interface{}{"ok": true, "integration": found.Name, "connected": true})), nil
		}

		var devices []plugin.DiscoveredDevice
		var syncErr error
		switch found.Type {
		case "fritzbox":
			devices, syncErr = integrations.NewFritzBox(url, username, password).Sync(ctx)
		case "unifi":
			devices, syncErr = integrations.NewUnifi(url, username, password).Sync(ctx)
		case "homeassistant":
			devices, syncErr = integrations.NewHomeAssistant(url, password).Sync(ctx)
		case "pihole":
			devices, syncErr = integrations.NewPiHole(url, password).Sync(ctx)
		case "pfsense":
			devices, syncErr = integrations.NewPfSense(url, username, password).Sync(ctx)
		default:
			return fmt.Sprintf(`{"error":"unknown integration type %q"}`, found.Type), nil
		}
		if syncErr != nil {
			e.store.UpdateIntegrationStatus(ctx, found.ID, "error", syncErr.Error())
			return fmt.Sprintf(`{"ok":false,"integration":%q,"error":"%s"}`, found.Name, syncErr.Error()), nil
		}
		stored := 0
		for _, d := range devices {
			if d.IP == "" {
				continue
			}
			if err := e.store.UpsertPassiveDevice(ctx, d); err == nil {
				stored++
			}
		}
		e.store.UpdateIntegrationStatus(ctx, found.ID, "ok", "")
		return string(mustJSON(map[string]interface{}{"ok": true, "integration": found.Name, "devices_synced": stored})), nil

	default:
		return fmt.Sprintf(`{"error":"invalid action %q (use test, sync, or delete)"}`, params.Action), nil
	}
}

func (e *ToolExecutor) checkVendors(ctx context.Context) (string, error) {
	devices, err := e.store.ListPassiveDevices(ctx)
	if err != nil {
		return "", err
	}
	updated := 0
	for _, d := range devices {
		if d.Vendor != "" || d.MACAddress == "" {
			continue
		}
		vendor := scanners.LookupVendor(d.MACAddress)
		if vendor == "" {
			continue
		}
		devType := scanners.ClassifyByVendor(vendor)
		e.store.UpdateHostVendor(ctx, d.ID, vendor, devType)
		updated++
	}
	return string(mustJSON(map[string]interface{}{
		"ok":      true,
		"updated": updated,
		"note":    "devices with vendors already set were skipped",
	})), nil
}

func (e *ToolExecutor) addPeer(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}
	addr := strings.TrimSpace(params.Address)
	if addr == "" || !strings.Contains(addr, ":") {
		return `{"error":"address must be host:port, e.g. 192.168.1.50:9600"}`, nil
	}
	now := time.Now().UTC()
	if err := e.store.UpsertPeer(ctx, &models.PeerInfo{
		ID:            "pending-" + addr,
		Address:       addr,
		Status:        "unknown",
		LastHeartbeat: &now,
	}); err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error()), nil
	}
	return string(mustJSON(map[string]interface{}{
		"ok":   true,
		"peer": addr,
		"note": "peer added; it will be contacted on the next heartbeat (within ~60s) and exchange peers via gossip",
	})), nil
}

// runCommand executes a shell command on a mesh node. Every command requires
// explicit confirmation; results are recorded in the exec history.
func (e *ToolExecutor) runCommand(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Command    string `json:"command"`
		Hostname   string `json:"hostname"`
		Shell      string `json:"shell"`
		TimeoutSec int    `json:"timeout_seconds"`
		Confirm    bool   `json:"confirm"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	if !params.Confirm {
		return fmt.Sprintf(`{"error":"confirmation required",%s}`, confirmHint), nil
	}
	if e.execRouter == nil {
		return `{"error":"remote command execution is not available on this node"}`, nil
	}

	hostID := ""
	hostLabel := e.identity.Hostname
	hostOS := ""
	if params.Hostname != "" {
		h := e.findHost(ctx, params.Hostname)
		if h == nil {
			return fmt.Sprintf(`{"error":"host %q not found"}`, params.Hostname), nil
		}
		if h.MonitorType != "agent" {
			return fmt.Sprintf(`{"error":"host %q is a passive device (%s); commands can only run on agent nodes"}`, params.Hostname, h.MonitorType), nil
		}
		hostID = h.ID
		hostLabel = h.Label()
		hostOS = h.OS
	}

	if params.TimeoutSec <= 0 {
		params.TimeoutSec = 120
	}
	if params.TimeoutSec > 600 {
		params.TimeoutSec = 600
	}
	if params.Shell == "" {
		params.Shell = "cmd"
	}

	res, err := e.execRouter.Exec(ctx, hostID, params.Command, params.Shell, params.TimeoutSec)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error()), nil
	}

	// Audit trail on the requesting node
	e.store.InsertExecRecord(ctx, &store.ExecRecord{
		HostID:     res.HostID,
		Hostname:   hostLabel,
		OS:         res.OS,
		Command:    params.Command,
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		ExitCode:   res.ExitCode,
		TimedOut:   res.TimedOut,
		DurationMs: res.DurationMs,
		ExecutedAt: time.Now().UTC(),
	})

	// Keep the LLM context small; full output lives in exec_history
	result := *res
	result.Stdout = truncate(result.Stdout, 4000)
	result.Stderr = truncate(result.Stderr, 2000)
	result.Command = params.Command
	if hostOS != "" && result.OS == "" {
		result.OS = hostOS
	}
	return string(mustJSON(result)), nil
}

func (e *ToolExecutor) listExecHistory(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Hostname string `json:"hostname"`
		Limit    int    `json:"limit"`
	}
	json.Unmarshal(args, &params)

	hostID := ""
	if params.Hostname != "" {
		h := e.findHost(ctx, params.Hostname)
		if h == nil {
			return fmt.Sprintf(`{"error":"host %q not found"}`, params.Hostname), nil
		}
		hostID = h.ID
	}

	records, err := e.store.ListExecHistory(ctx, hostID, params.Limit)
	if err != nil {
		return "", err
	}
	type histEntry struct {
		Host       string `json:"host"`
		OS         string `json:"os,omitempty"`
		Command    string `json:"command"`
		ExitCode   int    `json:"exit_code"`
		TimedOut   bool   `json:"timed_out,omitempty"`
		DurationMs int64  `json:"duration_ms"`
		ExecutedAt string `json:"executed_at"`
		Stderr     string `json:"stderr,omitempty"`
	}
	var out []histEntry
	for _, r := range records {
		out = append(out, histEntry{
			Host:       r.Hostname,
			OS:         r.OS,
			Command:    truncate(r.Command, 200),
			ExitCode:   r.ExitCode,
			TimedOut:   r.TimedOut,
			DurationMs: r.DurationMs,
			ExecutedAt: r.ExecutedAt.Format("2006-01-02 15:04:05"),
			Stderr:     truncate(r.Stderr, 200),
		})
	}
	return string(mustJSON(out)), nil
}

// ---------- helpers ----------

// findHost resolves a hostname, display name, or ID (case-insensitive
// partial match) to a host.
func (e *ToolExecutor) findHost(ctx context.Context, ref string) *models.Host {
	if ref == "" {
		return nil
	}
	hosts, err := e.store.ListHosts(ctx)
	if err != nil {
		return nil
	}
	// Prefer exact matches before partial ones
	for _, h := range hosts {
		if h.ID == ref || strings.EqualFold(h.Hostname, ref) || strings.EqualFold(h.DisplayName, ref) {
			return &h
		}
	}
	for _, h := range hosts {
		if containsCI(h.Hostname, ref) || containsCI(h.DisplayName, ref) {
			return &h
		}
	}
	return nil
}

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"error":"marshal failed"}`)
	}
	return b
}

func displayURL(url string) string {
	if url == "" {
		return "(removed)"
	}
	return url
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsCI(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	// Simple case-insensitive contains
	sl := make([]byte, len(s))
	for i := range s {
		if s[i] >= 'A' && s[i] <= 'Z' {
			sl[i] = s[i] + 32
		} else {
			sl[i] = s[i]
		}
	}
	subl := make([]byte, len(substr))
	for i := range substr {
		if substr[i] >= 'A' && substr[i] <= 'Z' {
			subl[i] = substr[i] + 32
		} else {
			subl[i] = substr[i]
		}
	}
	return bytes_contains(sl, subl)
}

func bytes_contains(s, sub []byte) bool {
	if len(sub) == 0 {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		match := true
		for j := range sub {
			if s[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
