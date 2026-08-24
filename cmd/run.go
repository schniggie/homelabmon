package cmd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/dx111ge/homelabmon/configs"
	"github.com/dx111ge/homelabmon/internal/agent"
	"github.com/dx111ge/homelabmon/internal/agent/discovery"
	"github.com/dx111ge/homelabmon/internal/agent/integrations"
	"github.com/dx111ge/homelabmon/internal/agent/observers"
	"github.com/dx111ge/homelabmon/internal/agent/scanners"
	hubapi "github.com/dx111ge/homelabmon/internal/hub/api"
	"github.com/dx111ge/homelabmon/internal/hub/llm"
	"github.com/dx111ge/homelabmon/internal/mesh"
	"github.com/dx111ge/homelabmon/internal/models"
	"github.com/dx111ge/homelabmon/internal/notify"
	"github.com/dx111ge/homelabmon/internal/plugin"
	"github.com/dx111ge/homelabmon/internal/store"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Start the HomeMonitor node",
	RunE:  runAgent,
}

func init() {
	rootCmd.AddCommand(runCmd)

	// Use PersistentFlags on root so they work with both `homelabmon --ui` and `homelabmon run --ui`
	rootCmd.PersistentFlags().Bool("ui", false, "enable web dashboard")
	rootCmd.PersistentFlags().String("llm", "", "Ollama API URL (e.g., http://localhost:11434)")
	rootCmd.PersistentFlags().String("llm-model", "qwen2.5:7b", "Ollama model for chat (default: qwen2.5:7b)")
	rootCmd.PersistentFlags().StringSlice("peer", nil, "initial peer addresses (e.g., 192.168.1.10:9600)")
	rootCmd.PersistentFlags().String("bind", ":9600", "bind address for API and UI")
	rootCmd.PersistentFlags().Int("collect-interval", 30, "metric collection interval in seconds")
	rootCmd.PersistentFlags().Bool("scan", false, "enable network scanning")
	rootCmd.PersistentFlags().Int("scan-interval", 300, "network scan interval in seconds (default 5 min)")
	rootCmd.PersistentFlags().String("notify-ntfy", "", "ntfy.sh topic URL (e.g., https://ntfy.sh/homelabmon-alerts)")
	rootCmd.PersistentFlags().String("notify-webhook", "", "webhook URL for notifications (Discord, Slack, custom)")
	rootCmd.PersistentFlags().Float64("notify-cpu-threshold", 90, "CPU % threshold for alert notifications")
	rootCmd.PersistentFlags().Float64("notify-mem-threshold", 90, "memory % threshold for alert notifications")
	rootCmd.PersistentFlags().Float64("notify-disk-threshold", 90, "disk % threshold for alert notifications")
	rootCmd.PersistentFlags().Bool("no-auth", false, "disable web UI authentication")
	rootCmd.PersistentFlags().Int("retention-days", 7, "number of days to keep metric history (0 = forever)")
	rootCmd.PersistentFlags().String("enroll-url", "", "URL of a CA node to enroll with (e.g., https://192.168.1.10:9600)")
	rootCmd.PersistentFlags().String("enroll-token", "", "one-time enrollment token from the CA node")
	rootCmd.PersistentFlags().String("site", "", "site label for multi-site federation (e.g., home, office, cloud)")

	viper.BindPFlag("site", rootCmd.PersistentFlags().Lookup("site"))

	viper.BindPFlag("ui", rootCmd.PersistentFlags().Lookup("ui"))
	viper.BindPFlag("llm", rootCmd.PersistentFlags().Lookup("llm"))
	viper.BindPFlag("llm-model", rootCmd.PersistentFlags().Lookup("llm-model"))
	viper.BindPFlag("peers", rootCmd.PersistentFlags().Lookup("peer"))
	viper.BindPFlag("bind", rootCmd.PersistentFlags().Lookup("bind"))
	viper.BindPFlag("collect-interval", rootCmd.PersistentFlags().Lookup("collect-interval"))
	viper.BindPFlag("scan", rootCmd.PersistentFlags().Lookup("scan"))
	viper.BindPFlag("scan-interval", rootCmd.PersistentFlags().Lookup("scan-interval"))
	viper.BindPFlag("notify-ntfy", rootCmd.PersistentFlags().Lookup("notify-ntfy"))
	viper.BindPFlag("notify-webhook", rootCmd.PersistentFlags().Lookup("notify-webhook"))
	viper.BindPFlag("notify-cpu-threshold", rootCmd.PersistentFlags().Lookup("notify-cpu-threshold"))
	viper.BindPFlag("notify-mem-threshold", rootCmd.PersistentFlags().Lookup("notify-mem-threshold"))
	viper.BindPFlag("notify-disk-threshold", rootCmd.PersistentFlags().Lookup("notify-disk-threshold"))
	viper.BindPFlag("no-auth", rootCmd.PersistentFlags().Lookup("no-auth"))
	viper.BindPFlag("retention-days", rootCmd.PersistentFlags().Lookup("retention-days"))
}

func runAgent(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := dataDir()

	// 1. Load or generate node identity
	nodeID := loadOrCreateNodeID(dir)
	log.Info().Str("node_id", nodeID).Str("data_dir", dir).Msg("starting homelabmon")

	// 2. Open store
	st, err := store.New(dir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// 2b. Load saved settings (DB values apply only if CLI flag was not explicitly set)
	if saved, err := st.AllSettings(ctx); err == nil {
		for k, v := range saved {
			if !cmd.Flags().Changed(k) && !rootCmd.PersistentFlags().Changed(k) {
				viper.Set(k, v)
			}
		}
	}

	// 3. Collect host info
	hostInfo, err := agent.CollectHostInfo(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("collect host info: %w", err)
	}
	site := viper.GetString("site")
	hostInfo.Site = site
	if err := st.UpsertHost(ctx, hostInfo); err != nil {
		return fmt.Errorf("store host info: %w", err)
	}

	identity := &models.NodeIdentity{
		ID:       nodeID,
		Hostname: hostInfo.Hostname,
		BindAddr: viper.GetString("bind"),
		Version:  Version,
		DataDir:  dir,
		Site:     site,
	}

	// Peer client routes management calls (e.g. docker control) to the
	// mesh node that owns the target host.
	peerClient := mesh.NewPeerClient(identity, st)

	log.Info().
		Str("hostname", hostInfo.Hostname).
		Str("os", hostInfo.OS).
		Str("arch", hostInfo.Arch).
		Str("platform", hostInfo.Platform).
		Int("cpu_cores", hostInfo.CPUCores).
		Strs("ips", hostInfo.IPAddresses).
		Msg("host info collected")

	// 4. Plugin registry + observers
	registry := plugin.NewRegistry()
	registry.Register(&observers.CPUObserver{})
	registry.Register(&observers.MemoryObserver{})
	registry.Register(&observers.DiskObserver{})
	registry.Register(&observers.NetworkObserver{})

	// Phase 2: service discovery observers
	portsObs := &observers.PortsObserver{}
	procObs := &observers.ProcessObserver{}
	dockerObs := &observers.DockerObserver{}
	registry.Register(portsObs)
	registry.Register(procObs)
	registry.Register(dockerObs)

	registry.DetectAndStart(ctx)

	// Load fingerprints and create discovery engine
	fingerprints, err := discovery.ParseFingerprints(configs.FingerprintsYAML)
	if err != nil {
		log.Warn().Err(err).Msg("failed to parse fingerprints, service discovery disabled")
	}
	var discoveryEngine *discovery.Engine
	if fingerprints != nil {
		discoveryEngine = discovery.NewEngine(nodeID, st, portsObs, procObs, fingerprints)
	}

	// Phase 3: network scanners (when --scan is enabled)
	scanEnabled := viper.GetBool("scan")
	var arpScanner *scanners.ARPScanner
	var mdnsScanner *scanners.MDNSScanner
	if scanEnabled {
		arpScanner = &scanners.ARPScanner{}
		mdnsScanner = &scanners.MDNSScanner{}
		registry.Register(arpScanner)
		registry.Register(mdnsScanner)
		registry.DetectAndStart(ctx)
		log.Info().Msg("network scanning enabled")
	}

	// Phase 5: load external plugins from <data-dir>/plugins/
	pluginDir := filepath.Join(dir, "plugins")
	if entries, err := os.ReadDir(pluginDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(pluginDir, entry.Name())
			if p := plugin.LoadExternalPlugin(path); p != nil {
				registry.Register(p)
			}
		}
		registry.DetectAndStart(ctx)
	}

	// Notifications
	dispatcher := notify.NewDispatcher(10 * time.Minute)
	if url := viper.GetString("notify-ntfy"); url != "" {
		dispatcher.AddSender(notify.NewNtfySender(url))
	}
	if url := viper.GetString("notify-webhook"); url != "" {
		dispatcher.AddSender(notify.NewWebhookSender(url))
	}

	// 5. Collector
	interval := time.Duration(viper.GetInt("collect-interval")) * time.Second
	collector := agent.NewCollector(nodeID, st, registry, interval)
	collector.Start(ctx)
	defer collector.Stop()

	// 6. LLM (if --llm) -- the agent gets access to all platform tools:
	// CMDB queries, docker control across the mesh, scans, notifications,
	// settings, integrations, and host management.
	var chatHandler *llm.ChatHandler
	var llmClient *llm.Client
	executor := llm.NewToolExecutor(st, identity)
	executor.SetDockerRouter(peerClient)
	executor.SetNotifier(dispatcher)
	if llmURL := viper.GetString("llm"); llmURL != "" {
		llmModel := viper.GetString("llm-model")
		if llmModel == "" {
			llmModel = "qwen2.5:7b"
		}
		llmClient = llm.NewClient(llmURL, llmModel)
		if err := llmClient.Ping(ctx); err != nil {
			log.Warn().Err(err).Str("url", llmURL).Msg("Ollama not reachable, chat disabled")
		} else {
			chatHandler = llm.NewChatHandler(llmClient, executor)
			log.Info().Str("url", llmURL).Str("model", llmModel).Msg("LLM agent enabled (full platform tool access)")
		}
	}

	// 6b. Scan coordinator (dedup scans across mesh peers)
	scanInterval := time.Duration(viper.GetInt("scan-interval")) * time.Second
	if scanInterval < 60*time.Second {
		scanInterval = 60 * time.Second
	}
	scanCoord := agent.NewScanCoordinator(scanInterval)

	// 7. Mesh transport + mTLS
	transport := mesh.NewTransport(identity, st, collector)
	transport.SetScanCoordinator(scanCoord)
	if dockerObs != nil {
		transport.SetDocker(dockerObs)
	}

	pki := mesh.NewPKI(dir)
	if pki.CAExists() {
		if err := pki.Load(); err != nil {
			log.Warn().Err(err).Msg("mTLS certs found but failed to load (running without TLS)")
		} else {
			transport.SetPKI(pki)
			peerClient.SetTLSConfig(pki.ClientTLSConfig())
			log.Info().Msg("mTLS enabled for mesh transport")
		}
	}

	// 7b. Enrollment (if --enroll-url and --enroll-token are set)
	enrollURL := viper.GetString("enroll-url")
	enrollToken := viper.GetString("enroll-token")
	if enrollURL != "" && enrollToken != "" {
		if err := enrollWithCA(dir, nodeID, enrollURL, enrollToken); err != nil {
			log.Error().Err(err).Msg("enrollment failed")
		} else {
			log.Info().Msg("enrolled successfully, loading certs")
			if err := pki.Load(); err == nil {
				transport.SetPKI(pki)
				peerClient.SetTLSConfig(pki.ClientTLSConfig())
				log.Info().Msg("mTLS enabled after enrollment")
			}
		}
	}

	// 8. Web UI (if --ui)
	if viper.GetBool("ui") {
		authEnabled := !viper.GetBool("no-auth")
		auth := hubapi.NewAuthManager(dir, authEnabled)

		uiServer, err := hubapi.NewUIServer(st, collector, identity, scanEnabled, dispatcher, chatHandler, llmClient, auth)
		if err != nil {
			return fmt.Errorf("create UI server: %w", err)
		}
		uiServer.PeerClient = peerClient
		if scanEnabled {
			scanOnce := func() (int, error) {
				count := runNetworkScan(ctx, arpScanner, mdnsScanner, st)
				scanCoord.RecordLocalScan()
				return count, nil
			}
			uiServer.ScanFunc = scanOnce
			executor.SetScanFunc(scanOnce)
		}
		uiServer.SetupRoutes(transport.Mux())

		if authEnabled {
			transport.SetHandler(auth.Middleware(transport.Mux()))
			log.Info().Str("token", auth.Token()).Msg("web UI auth enabled (token in ~/.homelabmon/auth-token)")
		} else {
			log.Warn().Msg("web UI auth DISABLED (--no-auth)")
		}
		log.Info().Msg("web UI enabled")
	}

	// 9. Start transport
	bindAddr := viper.GetString("bind")
	if err := transport.Start(bindAddr); err != nil {
		return fmt.Errorf("start transport: %w", err)
	}

	// 9. Register initial peers
	peers := viper.GetStringSlice("peers")
	for _, addr := range peers {
		now := time.Now().UTC()
		st.UpsertPeer(ctx, &models.PeerInfo{
			ID:            "pending-" + addr,
			Address:       addr,
			Status:        "unknown",
			LastHeartbeat: &now,
		})
		log.Info().Str("peer", addr).Msg("added initial peer")
	}

	// 10. Heartbeat service
	hbService := agent.NewHeartbeatService(identity, collector, st)
	hbService.SetScanCoordinator(scanCoord)
	if pki.Ready() {
		hbService.SetTLSConfig(pki.ClientTLSConfig())
	}
	hbService.Start(ctx)
	defer hbService.Stop()

	// 11. Background: service discovery every 60s
	go func() {
		// Wait for first metric collection to populate ports/processes
		time.Sleep(5 * time.Second)
		runDiscovery(ctx, discoveryEngine, dockerObs, nodeID, st)

		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runDiscovery(ctx, discoveryEngine, dockerObs, nodeID, st)
			}
		}
	}()

	// 12. Background: network scanning (when --scan is enabled)
	// ARP and mDNS run in separate goroutines, staggered by half the interval
	// to spread network load and avoid latency spikes.
	// Smart dedup: skip if a peer already scanned within the interval.
	if scanEnabled {
		log.Info().Dur("scan_interval", scanInterval).Msg("network scan interval")

		// ARP scan goroutine
		go func() {
			time.Sleep(10 * time.Second)
			if scanCoord.ShouldScan() {
				runNetworkScan(ctx, arpScanner, nil, st)
				scanCoord.RecordLocalScan()
			}

			ticker := time.NewTicker(scanInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if scanCoord.ShouldScan() {
						runNetworkScan(ctx, arpScanner, nil, st)
						scanCoord.RecordLocalScan()
					}
				}
			}
		}()

		// mDNS scan goroutine — offset by half the scan interval
		go func() {
			time.Sleep(10*time.Second + scanInterval/2)
			if scanCoord.ShouldScan() {
				runNetworkScan(ctx, nil, mdnsScanner, st)
				scanCoord.RecordLocalScan()
			}

			ticker := time.NewTicker(scanInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if scanCoord.ShouldScan() {
						runNetworkScan(ctx, nil, mdnsScanner, st)
						scanCoord.RecordLocalScan()
					}
				}
			}
		}()
	}

	// 13. Background: purge old metrics every hour (reads retention-days from viper each cycle)
	go func() {
		log.Info().Int("retention_days", viper.GetInt("retention-days")).Msg("metric retention policy")
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				days := viper.GetInt("retention-days")
				if days <= 0 {
					continue
				}
				n, _ := st.PurgeOldMetrics(ctx, days)
				if n > 0 {
					log.Info().Int64("purged", n).Int("retention_days", days).Msg("purged old metrics")
				}
			}
		}
	}()

	// 13. Background: mark stale services gone every 5 minutes
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				st.MarkStaleServicesGone(ctx, 10*time.Minute)
			}
		}
	}()

	// 14. Background: mark stale hosts offline if 2 heartbeats missed (~2.5 min) + notify
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stale, _ := st.MarkStaleHostsOffline(ctx, 150*time.Second)
				for _, h := range stale {
					log.Warn().Str("host", h.Hostname).Msg("host went offline")
					dispatcher.Send(notify.FormatHostOffline(h.ID, h.Hostname))
				}
			}
		}
	}()

	// 15. Background: metric threshold alerts (reads thresholds from viper each cycle for hot-reload)
	go func() {
		ticker := time.NewTicker(interval) // check at same rate as collection
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !dispatcher.HasSenders() {
					continue
				}
				checkThresholds(ctx, st, dispatcher,
					viper.GetFloat64("notify-cpu-threshold"),
					viper.GetFloat64("notify-mem-threshold"),
					viper.GetFloat64("notify-disk-threshold"))
			}
		}
	}()

	// 16. Background: integration auto-sync every 5 minutes
	go func() {
		time.Sleep(15 * time.Second) // initial delay
		runIntegrationSync(ctx, st)

		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runIntegrationSync(ctx, st)
			}
		}
	}()

	// 17. Config hot-reload: react to config.yaml changes at runtime
	viper.OnConfigChange(func(e fsnotify.Event) {
		log.Info().Str("file", e.Name).Msg("config file changed, applying updates")

		// Update log level
		if lvl, err := zerolog.ParseLevel(viper.GetString("log-level")); err == nil && lvl != zerolog.NoLevel {
			zerolog.SetGlobalLevel(lvl)
			log.Info().Str("level", lvl.String()).Msg("log level updated")
		}

		// Rebuild notification senders
		var senders []notify.Sender
		if url := viper.GetString("notify-ntfy"); url != "" {
			senders = append(senders, notify.NewNtfySender(url))
		}
		if url := viper.GetString("notify-webhook"); url != "" {
			senders = append(senders, notify.NewWebhookSender(url))
		}
		dispatcher.SetSenders(senders)

		log.Info().
			Int("retention_days", viper.GetInt("retention-days")).
			Float64("cpu_threshold", viper.GetFloat64("notify-cpu-threshold")).
			Float64("mem_threshold", viper.GetFloat64("notify-mem-threshold")).
			Float64("disk_threshold", viper.GetFloat64("notify-disk-threshold")).
			Msg("config reloaded")
	})

	log.Info().Str("bind", bindAddr).Bool("ui", viper.GetBool("ui")).Msg("homelabmon running")

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Info().Msg("shutting down...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	transport.Stop(shutdownCtx)
	registry.StopAll()

	log.Info().Msg("homelabmon stopped")
	return nil
}

func runDiscovery(ctx context.Context, engine *discovery.Engine, dockerObs *observers.DockerObserver, nodeID string, st *store.Store) {
	if engine != nil {
		engine.Discover(ctx)
	}

	// Convert running Docker containers to services
	containers := dockerObs.Latest()
	now := time.Now().UTC()
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		stack := c.Labels["com.docker.compose.project"]
		health := ""
		if strings.Contains(c.Status, "(healthy)") {
			health = "healthy"
		} else if strings.Contains(c.Status, "(unhealthy)") {
			health = "unhealthy"
		}
		for _, p := range c.Ports {
			if p.PublicPort == 0 {
				continue
			}
			svc := &models.DiscoveredService{
				HostID:       nodeID,
				Name:         c.ContainerName(),
				Port:         p.PublicPort,
				Protocol:     p.Type,
				Process:      c.Image,
				Category:     "container",
				Source:       "docker",
				ContainerID:  c.ID[:12],
				ContainerImg: c.Image,
				Stack:        stack,
				Health:       health,
				Status:       "active",
				LastSeen:     now,
			}
			st.UpsertService(ctx, svc)
		}
	}
}

func runNetworkScan(ctx context.Context, arpScanner *scanners.ARPScanner, mdnsScanner *scanners.MDNSScanner, st *store.Store) int {
	var allDevices []plugin.DiscoveredDevice

	// ARP scan
	if arpScanner != nil {
		devices, err := arpScanner.Scan(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("ARP scan failed")
		} else {
			allDevices = append(allDevices, devices...)
		}
	}

	// mDNS scan
	if mdnsScanner != nil {
		devices, err := mdnsScanner.Scan(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("mDNS scan failed")
		} else {
			allDevices = append(allDevices, devices...)
		}
	}

	// Filter out multicast/broadcast addresses
	var filtered []plugin.DiscoveredDevice
	for _, d := range allDevices {
		if strings.HasPrefix(d.IP, "224.") || strings.HasPrefix(d.IP, "239.") || strings.HasPrefix(d.IP, "255.") {
			continue
		}
		if strings.HasPrefix(d.MAC, "01:00:5e") || strings.HasPrefix(d.MAC, "ff:ff:ff") {
			continue
		}
		filtered = append(filtered, d)
	}
	allDevices = filtered

	// Enrich with OUI vendor lookup and classify
	for i := range allDevices {
		if allDevices[i].MAC != "" && allDevices[i].Vendor == "" {
			allDevices[i].Vendor = scanners.LookupVendor(allDevices[i].MAC)
		}
		if allDevices[i].DeviceType == "" && allDevices[i].Vendor != "" {
			allDevices[i].DeviceType = scanners.ClassifyByVendor(allDevices[i].Vendor)
		}
	}

	// Merge by MAC (prefer entry with more info)
	merged := make(map[string]plugin.DiscoveredDevice)
	for _, d := range allDevices {
		key := d.MAC
		if key == "" {
			key = d.IP
		}
		if existing, ok := merged[key]; ok {
			if d.Hostname != "" && existing.Hostname == "" {
				existing.Hostname = d.Hostname
			}
			if d.Vendor != "" && existing.Vendor == "" {
				existing.Vendor = d.Vendor
			}
			if d.DeviceType != "" && existing.DeviceType == "" {
				existing.DeviceType = d.DeviceType
			}
			if d.Source != existing.Source {
				existing.Source = existing.Source + "," + d.Source
			}
			merged[key] = existing
		} else {
			merged[key] = d
		}
	}

	// Store each device
	stored := 0
	for _, d := range merged {
		if err := st.UpsertPassiveDevice(ctx, d); err != nil {
			log.Warn().Err(err).Str("ip", d.IP).Msg("failed to store passive device")
		} else {
			stored++
		}
	}

	// Determine which scanner(s) ran for the log message
	var sources []string
	if arpScanner != nil {
		sources = append(sources, "arp")
	}
	if mdnsScanner != nil {
		sources = append(sources, "mdns")
	}
	log.Info().Int("devices", stored).Strs("sources", sources).Msg("network scan complete")
	return stored
}

func checkThresholds(ctx context.Context, st *store.Store, dispatcher *notify.Dispatcher, cpuThresh, memThresh, diskThresh float64) {
	hosts, err := st.ListHosts(ctx)
	if err != nil {
		return
	}
	for _, h := range hosts {
		if h.MonitorType != "agent" || h.Status != "online" {
			continue
		}
		metric, err := st.GetLatestMetric(ctx, h.ID)
		if err != nil || metric == nil {
			continue
		}
		if metric.CPUPercent >= cpuThresh {
			dispatcher.Send(notify.FormatThreshold(h.ID, h.Hostname, "CPU", metric.CPUPercent))
		}
		if metric.MemPercent >= memThresh {
			dispatcher.Send(notify.FormatThreshold(h.ID, h.Hostname, "Memory", metric.MemPercent))
		}
		for _, d := range metric.Disks() {
			if d.UsedPercent >= diskThresh {
				dispatcher.Send(notify.FormatThreshold(h.ID, h.Hostname, "Disk "+d.Path, d.UsedPercent))
			}
		}
	}
}

func runIntegrationSync(ctx context.Context, st *store.Store) {
	igs, err := st.ListIntegrations(ctx)
	if err != nil || len(igs) == 0 {
		return
	}

	for _, ig := range igs {
		if !ig.Enabled {
			continue
		}

		url := ig.Config["url"]
		username := ig.Config["username"]
		password, _ := st.GetSecret(ctx, store.SecretKeyID(ig.ID, "password"))

		var devices []plugin.DiscoveredDevice
		var err error

		switch ig.Type {
		case "fritzbox":
			fb := integrations.NewFritzBox(url, username, password)
			devices, err = fb.Sync(ctx)
		case "unifi":
			uf := integrations.NewUnifi(url, username, password)
			devices, err = uf.Sync(ctx)
		case "homeassistant":
			ha := integrations.NewHomeAssistant(url, password)
			devices, err = ha.Sync(ctx)
		case "pihole":
			ph := integrations.NewPiHole(url, password)
			devices, err = ph.Sync(ctx)
		case "pfsense":
			pf := integrations.NewPfSense(url, username, password)
			devices, err = pf.Sync(ctx)
		default:
			continue
		}

		if err != nil {
			st.UpdateIntegrationStatus(ctx, ig.ID, "error", err.Error())
			log.Warn().Err(err).Str("integration", ig.Name).Msg("integration sync failed")
			continue
		}

		stored := 0
		for _, d := range devices {
			if d.IP == "" {
				continue
			}
			if err := st.UpsertPassiveDevice(ctx, d); err == nil {
				stored++
			}
		}
		st.UpdateIntegrationStatus(ctx, ig.ID, "ok", "")
		log.Info().Str("integration", ig.Name).Int("devices", stored).Msg("integration sync complete")
	}
}

func enrollWithCA(dir, nodeID, caURL, token string) error {
	pki := mesh.NewPKI(dir)

	// Generate a key + CSR
	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	csrTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{
			Organization: []string{"HomelabMon"},
			CommonName:   nodeID,
		},
	}
	csrDER, err := x509.CreateCertificateRequest(cryptorand.Reader, csrTemplate, key)
	if err != nil {
		return fmt.Errorf("create CSR: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	// Send enrollment request (skip TLS verify since we don't have the CA cert yet)
	reqBody := map[string]string{
		"token":   token,
		"node_id": nodeID,
		"csr":     string(csrPEM),
	}
	body, _ := json.Marshal(reqBody)

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Post(caURL+"/api/v1/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("contact CA: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Cert   string `json:"cert"`
		CACert string `json:"ca_cert"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if result.Error != "" {
		return fmt.Errorf("enrollment rejected: %s", result.Error)
	}

	// Save CA cert
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte(result.CACert), 0600); err != nil {
		return fmt.Errorf("write CA cert: %w", err)
	}

	// Save node cert
	if err := os.WriteFile(filepath.Join(dir, "node.crt"), []byte(result.Cert), 0600); err != nil {
		return fmt.Errorf("write node cert: %w", err)
	}

	// Save node key
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, "node.key"), keyPEM, 0600); err != nil {
		return fmt.Errorf("write node key: %w", err)
	}

	// Verify we can load everything
	if err := pki.Load(); err != nil {
		return fmt.Errorf("verify certs: %w", err)
	}

	return nil
}

func loadOrCreateNodeID(dir string) string {
	idPath := filepath.Join(dir, "node-id")
	if data, err := os.ReadFile(idPath); err == nil {
		id := string(data)
		if len(id) > 0 {
			return id
		}
	}
	os.MkdirAll(dir, 0700)
	id := uuid.New().String()
	os.WriteFile(idPath, []byte(id), 0600)
	return id
}
