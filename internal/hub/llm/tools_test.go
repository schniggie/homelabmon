package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dx111ge/homelabmon/internal/models"
	"github.com/dx111ge/homelabmon/internal/notify"
	"github.com/dx111ge/homelabmon/internal/store"
	"github.com/spf13/viper"
)

type fakeDockerRouter struct {
	calls []string // "hostID|containerID|action"
	err   error
}

func (f *fakeDockerRouter) DockerControl(ctx context.Context, hostID, containerID, action string) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, hostID+"|"+containerID+"|"+action)
	return nil
}

type fakeNotifier struct {
	sent    []notify.Notification
	hasSndr bool
}

func (f *fakeNotifier) Send(n notify.Notification) { f.sent = append(f.sent, n) }
func (f *fakeNotifier) HasSenders() bool           { return f.hasSndr }
func (f *fakeNotifier) SetSenders([]notify.Sender) {}

func newTestExecutor(t *testing.T) (*ToolExecutor, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	identity := &models.NodeIdentity{
		ID:       "node-a",
		Hostname: "host-a",
		BindAddr: ":9600",
		Version:  "test",
	}
	return NewToolExecutor(st, identity), st
}

func seedHost(t *testing.T, st *store.Store, h models.Host) {
	t.Helper()
	if err := st.UpsertHost(context.Background(), &h); err != nil {
		t.Fatalf("upsert host: %v", err)
	}
}

func TestListHostsAndGetSummary(t *testing.T) {
	e, st := newTestExecutor(t)
	seedHost(t, st, models.Host{
		ID: "node-a", Hostname: "alpha", MonitorType: "agent",
		DeviceType: "server", Status: "online", OS: "linux",
		IPAddresses: []string{"10.0.0.1"},
	})
	seedHost(t, st, models.Host{
		ID: "dev-1", Hostname: "tv-living", MonitorType: "passive",
		DeviceType: "tv", Status: "offline", Vendor: "Samsung",
	})

	res, err := e.Execute(context.Background(), "list_hosts", json.RawMessage(`{"monitor_type":"agent"}`))
	if err != nil {
		t.Fatalf("list_hosts: %v", err)
	}
	if !strings.Contains(res, `"hostname":"alpha"`) || strings.Contains(res, "tv-living") {
		t.Errorf("list_hosts filter failed: %s", res)
	}

	res, err = e.Execute(context.Background(), "get_summary", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("get_summary: %v", err)
	}
	if !strings.Contains(res, `"agent_nodes":1`) || !strings.Contains(res, `"passive_devices":1`) {
		t.Errorf("get_summary wrong: %s", res)
	}
}

func TestDockerControlConfirmGating(t *testing.T) {
	e, st := newTestExecutor(t)
	seedHost(t, st, models.Host{ID: "node-a", Hostname: "alpha", MonitorType: "agent", Status: "online"})
	seedHost(t, st, models.Host{ID: "node-b", Hostname: "beta", MonitorType: "agent", Status: "online"})
	if err := st.UpsertService(context.Background(), &models.DiscoveredService{
		HostID: "node-b", Name: "nginx", Port: 8080, Category: "container",
		Source: "docker", ContainerID: "abc123def456", ContainerImg: "nginx:latest",
		Status: "active", LastSeen: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}

	fake := &fakeDockerRouter{}
	e.SetDockerRouter(fake)

	// stop without confirmation must be refused and not reach the router
	res, err := e.Execute(context.Background(), "docker_control",
		json.RawMessage(`{"container":"nginx","action":"stop"}`))
	if err != nil {
		t.Fatalf("docker_control: %v", err)
	}
	if !strings.Contains(res, "confirmation required") {
		t.Errorf("expected confirmation gate, got: %s", res)
	}
	if len(fake.calls) != 0 {
		t.Errorf("router must not be called without confirmation, got %v", fake.calls)
	}

	// stop with confirmation routes to the owning host
	res, err = e.Execute(context.Background(), "docker_control",
		json.RawMessage(`{"container":"nginx","action":"stop","confirm":true}`))
	if err != nil {
		t.Fatalf("docker_control: %v", err)
	}
	if !strings.Contains(res, `"ok":true`) {
		t.Errorf("expected ok, got: %s", res)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "node-b|abc123def456|stop" {
		t.Errorf("wrong routing: %v", fake.calls)
	}

	// start does not need confirmation
	res, err = e.Execute(context.Background(), "docker_control",
		json.RawMessage(`{"container":"nginx","action":"start"}`))
	if err != nil {
		t.Fatalf("docker_control: %v", err)
	}
	if len(fake.calls) != 2 || fake.calls[1] != "node-b|abc123def456|start" {
		t.Errorf("start should route without confirm: %v", fake.calls)
	}

	// ambiguous match across hosts without hostname
	if err := st.UpsertService(context.Background(), &models.DiscoveredService{
		HostID: "node-a", Name: "nginx-proxy", Port: 80, Category: "container",
		Source: "docker", ContainerID: "fff000fff000", ContainerImg: "nginx:1.25",
		Status: "active", LastSeen: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}
	res, _ = e.Execute(context.Background(), "docker_control",
		json.RawMessage(`{"container":"nginx","action":"start"}`))
	if !strings.Contains(res, "ambiguous") {
		t.Errorf("expected ambiguity error, got: %s", res)
	}
}

func TestDeleteHostConfirmAndRename(t *testing.T) {
	e, st := newTestExecutor(t)
	seedHost(t, st, models.Host{ID: "dev-9", Hostname: "old-name", MonitorType: "passive", Status: "offline"})

	// delete without confirm refused
	res, _ := e.Execute(context.Background(), "delete_host", json.RawMessage(`{"hostname":"old-name","confirm":false}`))
	if !strings.Contains(res, "confirmation required") {
		t.Errorf("expected confirmation gate, got: %s", res)
	}
	h, _ := st.GetHost(context.Background(), "dev-9")
	if h == nil {
		t.Fatal("host must not be deleted without confirmation")
	}

	// rename
	res, err := e.Execute(context.Background(), "rename_host", json.RawMessage(`{"hostname":"old-name","new_name":"Living Room TV"}`))
	if err != nil || !strings.Contains(res, "Living Room TV") {
		t.Errorf("rename_host failed: %v %s", err, res)
	}
	h, _ = st.GetHost(context.Background(), "dev-9")
	if h == nil || h.DisplayName != "Living Room TV" {
		t.Errorf("rename not persisted: %+v", h)
	}

	// findHost should now match the display name too
	res, _ = e.Execute(context.Background(), "get_host", json.RawMessage(`{"hostname":"living room"}`))
	if !strings.Contains(res, "dev-9") {
		t.Errorf("display name lookup failed: %s", res)
	}

	// delete with confirm works
	res, _ = e.Execute(context.Background(), "delete_host", json.RawMessage(`{"hostname":"Living Room TV","confirm":true}`))
	if !strings.Contains(res, `"ok":"true"`) {
		t.Errorf("delete failed: %s", res)
	}
	h, _ = st.GetHost(context.Background(), "dev-9")
	if h != nil {
		t.Error("host should be deleted")
	}
}

func TestSetDeviceTypeValidation(t *testing.T) {
	e, st := newTestExecutor(t)
	seedHost(t, st, models.Host{ID: "dev-2", Hostname: "phone-x", MonitorType: "passive"})

	res, _ := e.Execute(context.Background(), "set_device_type", json.RawMessage(`{"hostname":"phone-x","device_type":"spaceship"}`))
	if !strings.Contains(res, "invalid device_type") {
		t.Errorf("expected validation error, got: %s", res)
	}

	res, _ = e.Execute(context.Background(), "set_device_type", json.RawMessage(`{"hostname":"phone-x","device_type":"phone"}`))
	if !strings.Contains(res, `"ok":"true"`) {
		t.Errorf("valid type rejected: %s", res)
	}
}

func TestUpdateSettingsPersists(t *testing.T) {
	e, st := newTestExecutor(t)
	defer viper.Set("notify-cpu-threshold", 90.0)
	defer viper.Set("retention-days", 7)

	res, err := e.Execute(context.Background(), "update_settings",
		json.RawMessage(`{"cpu_threshold":75,"retention_days":30}`))
	if err != nil {
		t.Fatalf("update_settings: %v", err)
	}
	if !strings.Contains(res, `"applied"`) {
		t.Errorf("expected applied list, got: %s", res)
	}
	if got := viper.GetFloat64("notify-cpu-threshold"); got != 75 {
		t.Errorf("viper not updated: %v", got)
	}
	if v, _ := st.GetSetting(context.Background(), "retention-days"); v != "30" {
		t.Errorf("store not persisted: %q", v)
	}

	// out of range rejected
	res, _ = e.Execute(context.Background(), "update_settings", json.RawMessage(`{"cpu_threshold":150}`))
	if !strings.Contains(res, "error") {
		t.Errorf("expected range error, got: %s", res)
	}
}

func TestSendNotificationRequiresSenders(t *testing.T) {
	e, _ := newTestExecutor(t)
	fn := &fakeNotifier{}
	e.SetNotifier(fn)

	res, _ := e.Execute(context.Background(), "send_notification",
		json.RawMessage(`{"title":"Hi","message":"Hello"}`))
	if !strings.Contains(res, "no notification channels") {
		t.Errorf("expected no-senders error, got: %s", res)
	}

	fn.hasSndr = true
	res, _ = e.Execute(context.Background(), "send_notification",
		json.RawMessage(`{"title":"Hi","message":"Hello","severity":"warning"}`))
	if !strings.Contains(res, `"ok":true`) || len(fn.sent) != 1 || fn.sent[0].Severity != "warning" {
		t.Errorf("send failed: %s sent=%v", res, fn.sent)
	}
}

func TestAddPeer(t *testing.T) {
	e, st := newTestExecutor(t)
	res, err := e.Execute(context.Background(), "add_peer", json.RawMessage(`{"address":"192.168.1.50:9600"}`))
	if err != nil || !strings.Contains(res, `"ok":true`) {
		t.Fatalf("add_peer failed: %v %s", err, res)
	}
	peers, _ := st.ListPeers(context.Background())
	if len(peers) != 1 || peers[0].Address != "192.168.1.50:9600" {
		t.Errorf("peer not stored: %+v", peers)
	}

	res, _ = e.Execute(context.Background(), "add_peer", json.RawMessage(`{"address":"no-port"}`))
	if !strings.Contains(res, "error") {
		t.Errorf("expected address validation error, got: %s", res)
	}
}

func TestScanUnavailable(t *testing.T) {
	e, _ := newTestExecutor(t)
	res, _ := e.Execute(context.Background(), "trigger_network_scan", json.RawMessage(`{}`))
	if !strings.Contains(res, "not enabled") {
		t.Errorf("expected scan-unavailable error, got: %s", res)
	}
}

// Every advertised tool must be executable (no "unknown tool" dispatch gaps).
func TestAllToolDefinitionsExecutable(t *testing.T) {
	e, _ := newTestExecutor(t)
	for _, tool := range ToolDefinitions() {
		name := tool.Function.Name
		_, err := e.Execute(context.Background(), name, json.RawMessage(`{}`))
		if err != nil && strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("tool %q has no executor", name)
		}
	}
}
