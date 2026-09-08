package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dx111ge/homelabmon/internal/mesh"
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

type fakeExecRouter struct {
	calls []string // "hostID|command|shell"
	res   *mesh.ExecResult
	err   error
}

func (f *fakeExecRouter) Exec(ctx context.Context, hostID, command, shell string, timeoutSec int) (*mesh.ExecResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, hostID+"|"+command+"|"+shell)
	return f.res, nil
}

func TestRunCommandConfirmGatingAndHistory(t *testing.T) {
	e, st := newTestExecutor(t)
	seedHost(t, st, models.Host{ID: "node-a", Hostname: "alpha", MonitorType: "agent", Status: "online", OS: "ubuntu"})
	seedHost(t, st, models.Host{ID: "node-b", Hostname: "beta", MonitorType: "agent", Status: "online", OS: "freebsd"})
	seedHost(t, st, models.Host{ID: "dev-1", Hostname: "tv", MonitorType: "passive"})

	fake := &fakeExecRouter{res: &mesh.ExecResult{
		HostID: "node-b", OS: "freebsd", Stdout: "ok", ExitCode: 0, DurationMs: 12,
	}}
	e.SetExecRouter(fake)

	// every command requires confirmation, even harmless ones
	res, err := e.Execute(context.Background(), "run_command", json.RawMessage(`{"command":"uptime"}`))
	if err != nil {
		t.Fatalf("run_command: %v", err)
	}
	if !strings.Contains(res, "confirmation required") {
		t.Errorf("expected confirmation gate, got: %s", res)
	}
	if len(fake.calls) != 0 {
		t.Errorf("router must not be called without confirmation: %v", fake.calls)
	}

	// passive devices refuse execution
	res, _ = e.Execute(context.Background(), "run_command",
		json.RawMessage(`{"command":"uptime","hostname":"tv","confirm":true}`))
	if !strings.Contains(res, "passive device") {
		t.Errorf("expected passive-device refusal, got: %s", res)
	}

	// confirmed command routes to the right host and records history
	res, _ = e.Execute(context.Background(), "run_command",
		json.RawMessage(`{"command":"pkg upgrade -n","hostname":"beta","timeout_seconds":300,"confirm":true}`))
	if !strings.Contains(res, `"stdout":"ok"`) {
		t.Errorf("expected result, got: %s", res)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "node-b|pkg upgrade -n|cmd" {
		t.Errorf("wrong routing: %v", fake.calls)
	}

	hist, err := st.ListExecHistory(context.Background(), "node-b", 10)
	if err != nil || len(hist) != 1 {
		t.Fatalf("expected 1 history record, got %d err %v", len(hist), err)
	}
	if hist[0].Command != "pkg upgrade -n" || hist[0].OS != "freebsd" || hist[0].Hostname != "beta" {
		t.Errorf("history record wrong: %+v", hist[0])
	}

	// history tool reflects the record
	res, _ = e.Execute(context.Background(), "list_exec_history", json.RawMessage(`{"hostname":"beta"}`))
	if !strings.Contains(res, "pkg upgrade -n") {
		t.Errorf("history tool missing record: %s", res)
	}
}

func TestRunCommandUnavailable(t *testing.T) {
	e, _ := newTestExecutor(t)
	res, _ := e.Execute(context.Background(), "run_command",
		json.RawMessage(`{"command":"uptime","confirm":true}`))
	if !strings.Contains(res, "not available") {
		t.Errorf("expected unavailable error without router, got: %s", res)
	}
}

func TestManagementActionsAutoRecorded(t *testing.T) {
	e, st := newTestExecutor(t)
	seedHost(t, st, models.Host{ID: "node-b", Hostname: "beta", MonitorType: "agent", Status: "online"})
	if err := st.UpsertService(context.Background(), &models.DiscoveredService{
		HostID: "node-b", Name: "nginx", Port: 80, Category: "container",
		Source: "docker", ContainerID: "abc123def456", ContainerImg: "nginx:latest",
		Status: "active", LastSeen: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}
	e.SetDockerRouter(&fakeDockerRouter{})

	// successful docker action records a node-scoped memory
	e.Execute(context.Background(), "docker_control",
		json.RawMessage(`{"container":"nginx","hostname":"beta","action":"start"}`))
	memories, _ := st.ListMemories(context.Background(), "node-b", "action", 10)
	if len(memories) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(memories))
	}
	if !strings.Contains(memories[0].Title, "start nginx") || memories[0].Hostname != "beta" {
		t.Errorf("memory wrong: %+v", memories[0])
	}

	// failed/refused actions are NOT recorded
	e.Execute(context.Background(), "docker_control",
		json.RawMessage(`{"container":"nginx","hostname":"beta","action":"stop"}`)) // no confirm
	memories, _ = st.ListMemories(context.Background(), "node-b", "action", 10)
	if len(memories) != 1 {
		t.Errorf("refused action must not be recorded: %d", len(memories))
	}
}

func TestRememberAndRecall(t *testing.T) {
	e, st := newTestExecutor(t)
	seedHost(t, st, models.Host{ID: "node-c", Hostname: "gamma", MonitorType: "agent", Status: "online"})

	// agent stores a node note
	res, err := e.Execute(context.Background(), "remember",
		json.RawMessage(`{"title":"Pihole runs in compose stack dns","detail":"config at /etc/pihole, restart via docker compose","hostname":"gamma"}`))
	if err != nil || !strings.Contains(res, `"ok":"true"`) {
		t.Fatalf("remember failed: %v %s", err, res)
	}

	// recalling for that host returns the note
	res, _ = e.Execute(context.Background(), "recall_memory",
		json.RawMessage(`{"hostname":"gamma"}`))
	if !strings.Contains(res, "Pihole runs in compose stack dns") {
		t.Errorf("recall missing note: %s", res)
	}

	// kind="all" must not filter anything out (LLMs pass it literally)
	res, _ = e.Execute(context.Background(), "recall_memory",
		json.RawMessage(`{"kind":"all"}`))
	if !strings.Contains(res, "Pihole runs in compose stack dns") {
		t.Errorf(`recall with kind="all" missing note: %s`, res)
	}

	// unknown host refused
	res, _ = e.Execute(context.Background(), "recall_memory",
		json.RawMessage(`{"hostname":"nonexistent"}`))
	if !strings.Contains(res, "error") {
		t.Errorf("expected error for unknown host: %s", res)
	}
}

// newFakeSearXNG mimics a SearXNG instance with JSON format enabled and
// records the raw query string of the last request.
func newFakeSearXNG(t *testing.T, status int) (*httptest.Server, *string) {
	t.Helper()
	lastQuery := ""
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.RawQuery
		if status != http.StatusOK {
			w.WriteHeader(status)
			w.Write([]byte("rate limited"))
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"query": r.URL.Query().Get("q"),
			"results": []map[string]string{
				{"title": "nginx releases", "url": "https://github.com/nginx/nginx/releases", "content": "nginx-1.30.2 stable", "engine": "brave"},
				{"title": "nginx news", "url": "https://nginx.org/", "content": "njs 1.9 released", "engine": "duckduckgo"},
				{"title": "third hit", "url": "https://example.com/3", "content": "filler", "engine": "google"},
			},
			"answers": []string{"1.30.2"},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &lastQuery
}

func TestWebSearchReturnsCompactResults(t *testing.T) {
	e, _ := newTestExecutor(t)
	srv, lastQuery := newFakeSearXNG(t, http.StatusOK)
	e.SetSearchURL(srv.URL + "/")

	out, err := e.Execute(context.Background(), "web_search", json.RawMessage(`{"query":"nginx latest stable","count":2}`))
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	if !strings.Contains(out, "nginx-1.30.2 stable") || !strings.Contains(out, "https://nginx.org/") {
		t.Errorf("results missing: %s", truncate(out, 300))
	}
	if strings.Contains(out, "third hit") {
		t.Errorf("count param ignored: %s", out)
	}
	if strings.Contains(out, `"engine"`) && strings.Contains(out, "filler") {
		t.Errorf("unexpected extra fields")
	}
	if !strings.Contains(*lastQuery, "format=json") || !strings.Contains(*lastQuery, "q=nginx") {
		t.Errorf("upstream query wrong: %s", *lastQuery)
	}
	if !strings.Contains(out, "1.30.2") { // instant answer included
		t.Errorf("answer missing: %s", truncate(out, 300))
	}
}

func TestWebSearchNotConfigured(t *testing.T) {
	e, _ := newTestExecutor(t)
	out, err := e.Execute(context.Background(), "web_search", json.RawMessage(`{"query":"anything"}`))
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	if !strings.Contains(out, "--searxng") {
		t.Errorf("expected not-configured hint: %s", out)
	}
}

func TestWebSearchUpstreamError(t *testing.T) {
	e, _ := newTestExecutor(t)
	srv, _ := newFakeSearXNG(t, http.StatusForbidden)
	e.SetSearchURL(srv.URL)
	out, err := e.Execute(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`))
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	if !strings.Contains(out, "403") || !strings.Contains(out, "rate limited") {
		t.Errorf("upstream error not surfaced: %s", out)
	}
}

func TestWebSearchEmptyQuery(t *testing.T) {
	e, _ := newTestExecutor(t)
	srv, _ := newFakeSearXNG(t, http.StatusOK)
	e.SetSearchURL(srv.URL)
	out, err := e.Execute(context.Background(), "web_search", json.RawMessage(`{"query":"   "}`))
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	if !strings.Contains(out, "query is required") {
		t.Errorf("empty query not rejected: %s", out)
	}
}

// TestWebSearchLive exercises the executor against a real SearXNG instance.
// Skipped unless HOMELABMON_SEARXNG_URL is set:
//
//	HOMELABMON_SEARXNG_URL=https://searxng.example.org go test -run TestWebSearchLive ./internal/hub/llm/
func TestWebSearchLive(t *testing.T) {
	url := os.Getenv("HOMELABMON_SEARXNG_URL")
	if url == "" {
		t.Skip("set HOMELABMON_SEARXNG_URL to run the live SearXNG test")
	}
	e, _ := newTestExecutor(t)
	e.SetSearchURL(url)
	out, err := e.Execute(context.Background(), "web_search",
		json.RawMessage(`{"query":"nginx stable release notes","count":3}`))
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	t.Logf("live result: %s", truncate(out, 800))
	// The pipeline must always produce a well-formed response; result hits
	// depend on the instance's upstream engines not being rate-limited.
	if !strings.Contains(out, `"results"`) {
		t.Errorf("malformed live response: %s", truncate(out, 300))
	}
	if !strings.Contains(out, `"url"`) {
		t.Logf("instance returned no results (engines possibly suspended): %s", truncate(out, 300))
	}
}

func TestWebSearchRateLimitedNote(t *testing.T) {
	e, _ := newTestExecutor(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results":              []map[string]string{},
			"unresponsive_engines": [][]interface{}{{"brave", "Suspended: too many requests"}, {"duckduckgo", "timeout"}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e.SetSearchURL(srv.URL)

	out, err := e.Execute(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`))
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	if !strings.Contains(out, "rate-limited") || !strings.Contains(out, "brave") {
		t.Errorf("engine-status note missing: %s", out)
	}
}
