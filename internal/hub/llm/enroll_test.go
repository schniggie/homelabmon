package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dx111ge/homelabmon/internal/models"
	"github.com/dx111ge/homelabmon/internal/store"
)

// fakeEnrollRunner scripts the SSH interactions of enroll_node and records
// every invocation (name, args, stdin) for assertions.
type fakeEnrollRunner struct {
	calls   []string // "name|args...|stdinLen:123|stdinHead"
	stdins  map[string]string
	outputs map[string]string // matched by substring of the joined args
	failOn  string            // substring that makes Run return an error
}

func (f *fakeEnrollRunner) Run(ctx context.Context, stdin string, name string, args ...string) (string, error) {
	joined := strings.Join(args, " ")
	head := stdin
	if len(head) > 60 {
		head = head[:60]
	}
	f.calls = append(f.calls, fmt.Sprintf("%s|%s|len:%d|head:%q", name, joined, len(stdin), head))
	for sub, out := range f.outputs {
		if strings.Contains(joined, sub) {
			if f.failOn != "" && strings.Contains(joined, f.failOn) {
				return out, fmt.Errorf("exit status 1")
			}
			return out, nil
		}
	}
	if f.failOn != "" && strings.Contains(joined, f.failOn) {
		return "", fmt.Errorf("exit status 1")
	}
	if stdin != "" {
		f.stdins[joined] = stdin
	}
	return "", nil
}

func newEnrollTestExecutor(t *testing.T) (*ToolExecutor, *store.Store, *fakeEnrollRunner) {
	t.Helper()
	e, st := newTestExecutor(t)
	fake := &fakeEnrollRunner{
		stdins: map[string]string{},
		outputs: map[string]string{
			"uname": "Linux\nx86_64\n",
		},
	}
	e.runner = fake
	e.SetEnrollEndpoint("9601", false)
	t.Cleanup(func() {
		enrollPollInterval = 5 * time.Second
		enrollPollMax = 90 * time.Second
	})
	return e, st, fake
}

func TestEnrollNodeRequiresConfirmation(t *testing.T) {
	e, _, fake := newEnrollTestExecutor(t)
	out, err := e.Execute(context.Background(), "enroll_node",
		json.RawMessage(`{"address":"192.168.178.50","username":"dx"}`))
	if err != nil {
		t.Fatalf("enroll_node: %v", err)
	}
	if !strings.Contains(out, "confirmation required") || !strings.Contains(out, "192.168.178.50") {
		t.Errorf("expected confirmation gate naming the target: %s", out)
	}
	if len(fake.calls) != 0 {
		t.Errorf("no SSH activity allowed before confirmation, got %d calls", len(fake.calls))
	}
}

func TestEnrollNodeNeverAutoApproved(t *testing.T) {
	args := json.RawMessage(`{"address":"192.168.178.50","username":"dx"}`)
	got := withAutoConfirm("enroll_node", args)
	if string(got) != string(args) {
		t.Errorf("enroll_node must never be auto-approved, confirm was injected: %s", got)
	}
}

func TestEnrollNodeHappyPath(t *testing.T) {
	e, st, fake := newEnrollTestExecutor(t)
	enrollPollInterval = time.Millisecond
	enrollPollMax = 3 * time.Second

	// the new node's heartbeat appears shortly after "service start"
	go func() {
		time.Sleep(30 * time.Millisecond)
		st.UpsertHost(context.Background(), &models.Host{
			ID: "enrolled-1", Hostname: "newbox", MonitorType: "agent", Status: "online",
		})
	}()

	out, err := e.Execute(context.Background(), "enroll_node",
		json.RawMessage(`{"address":"192.168.178.50","username":"dx","site":"home","confirm":true}`))
	if err != nil {
		t.Fatalf("enroll_node: %v", err)
	}
	if !strings.Contains(out, `"verified":true`) || !strings.Contains(out, "newbox") {
		t.Fatalf("expected verified enrollment of newbox, got: %s", truncate(out, 400))
	}

	// one-time token: stored on the hub, delivered via stdin, never in argv
	storedToken, _ := st.GetSetting(context.Background(), "enroll-token")
	if storedToken == "" {
		t.Fatal("no token stored on the hub")
	}
	var enrollStdin, unitStdin string
	unitCmdSeen := false
	for _, c := range fake.calls {
		if strings.Contains(c, "homelabmon enroll") {
			// check only the argv portion (the record also embeds a stdin preview)
			if strings.Contains(calleeArgs(c), storedToken) {
				t.Errorf("token leaked into command args: %s", c)
			}
			enrollStdin = fake.stdins[calleeArgs(c)]
		}
		if strings.Contains(c, "homelabmon.service") {
			unitCmdSeen = true
			unitStdin = fake.stdins[calleeArgs(c)]
		}
	}
	if enrollStdin != storedToken {
		t.Errorf("enroll stdin token mismatch: %q vs %q", enrollStdin, storedToken)
	}
	if !unitCmdSeen || !strings.Contains(unitStdin, "ExecStart=/usr/local/bin/homelabmon --ui --scan --site 'home'") {
		t.Errorf("systemd unit wrong: %q", unitStdin)
	}
	if strings.Contains(unitStdin, "enroll") {
		t.Errorf("unit must not contain enrollment flags (certs persist): %s", unitStdin)
	}
	// systemd sets no HOME for root services without User=/Environment - the
	// service would resolve a different data dir than the enroll step (live bug)
	if !strings.Contains(unitStdin, "User=root") || !strings.Contains(unitStdin, "Environment=HOME=/root") {
		t.Errorf("unit must pin User=root and HOME=/root: %s", unitStdin)
	}

	// enrollment recorded in agent memory
	mems, _ := st.ListMemories(context.Background(), "", "", 10)
	found := false
	for _, m := range mems {
		if strings.Contains(m.Title, "Enrolled node 192.168.178.50") {
			found = true
		}
	}
	if !found {
		t.Errorf("enrollment not recorded in memory: %+v", mems)
	}
}

// calleeArgs reconstructs the joined-args key used by fakeEnrollRunner.calls.
func calleeArgs(call string) string {
	// calls are "ssh|<args>|len:N|head:..."; extract everything between the
	// first and second pipe.
	first := strings.Index(call, "|")
	second := strings.Index(call[first+1:], "|")
	return call[first+1 : first+1+second]
}

func TestEnrollNodeDeployedButUnverified(t *testing.T) {
	e, _, fake := newEnrollTestExecutor(t)
	enrollPollInterval = time.Millisecond
	enrollPollMax = 20 * time.Millisecond

	out, err := e.Execute(context.Background(), "enroll_node",
		json.RawMessage(`{"address":"192.168.178.51","username":"root","confirm":true}`))
	if err != nil {
		t.Fatalf("enroll_node: %v", err)
	}
	if !strings.Contains(out, `"verified":false`) || !strings.Contains(out, "systemctl status homelabmon") {
		t.Fatalf("expected unverified-but-deployed result, got: %s", truncate(out, 400))
	}
	if !strings.Contains(strings.Join(fake.calls, "\n"), "systemctl enable --now homelabmon") {
		t.Errorf("service never started: %v", fake.calls)
	}
}

func TestEnrollNodeSSHFailure(t *testing.T) {
	e, _, fake := newEnrollTestExecutor(t)
	fake.failOn = "uname"
	out, err := e.Execute(context.Background(), "enroll_node",
		json.RawMessage(`{"address":"10.9.9.9","username":"dx","confirm":true}`))
	if err != nil {
		t.Fatalf("enroll_node: %v", err)
	}
	if !strings.Contains(out, "platform detection") {
		t.Fatalf("expected ssh failure surfaced: %s", out)
	}
}

func TestEnrollNodeUnsupportedOS(t *testing.T) {
	e, _, fake := newEnrollTestExecutor(t)
	fake.outputs["uname"] = "SunOS\nx86_64\n"
	out, err := e.Execute(context.Background(), "enroll_node",
		json.RawMessage(`{"address":"192.168.178.52","username":"dx","confirm":true}`))
	if err != nil {
		t.Fatalf("enroll_node: %v", err)
	}
	if !strings.Contains(out, "only Linux targets") {
		t.Fatalf("expected unsupported-OS error: %s", out)
	}
}

func TestEnrollNodeMissingCrossArchBinary(t *testing.T) {
	e, _, fake := newEnrollTestExecutor(t)
	fake.outputs["uname"] = "Linux\naarch64\n"
	e.SetDeployDistDir(t.TempDir()) // empty dist dir
	out, err := e.Execute(context.Background(), "enroll_node",
		json.RawMessage(`{"address":"192.168.178.53","username":"dx","confirm":true}`))
	if err != nil {
		t.Fatalf("enroll_node: %v", err)
	}
	if !strings.Contains(out, "no binary for linux/arm64") || !strings.Contains(out, "make") {
		t.Fatalf("expected missing-binary guidance: %s", out)
	}
}
