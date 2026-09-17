package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
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

func mkdirAll(dir string) error { return os.MkdirAll(dir, 0700) }

func writeFile(path, content string, perm os.FileMode) {
	_ = os.WriteFile(path, []byte(content), perm)
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
	if !strings.Contains(out, "only Linux and FreeBSD targets") {
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

// TestEnrollNodeAuthFailureIncludesHint verifies that an SSH auth failure
// carries a self-diagnosing hint describing the hub's identity files.
func TestEnrollNodeAuthFailureIncludesHint(t *testing.T) {
	e, _, fake := newEnrollTestExecutor(t)
	fake.failOn = "uname"
	fake.outputs["uname"] = "cd@target: Permission denied (publickey,password)."

	out, err := e.Execute(context.Background(), "enroll_node",
		json.RawMessage(`{"address":"192.168.178.60","username":"cd","confirm":true}`))
	if err != nil {
		t.Fatalf("enroll_node: %v", err)
	}
	t.Logf("result: %s", out)
	if !strings.Contains(out, `"hint"`) || !strings.Contains(out, "authorized_keys") {
		t.Fatalf("expected auth hint in error, got: %s", out)
	}
}

// TestEnrollNodeUsesConfiguredSSHKey verifies --ssh-key is passed as ssh -i
// with IdentitiesOnly.
func TestEnrollNodeUsesConfiguredSSHKey(t *testing.T) {
	e, _, fake := newEnrollTestExecutor(t)
	fake.failOn = "uname"
	e.SetSSHKeyPath("/data/.ssh/deploy_ed25519")

	e.Execute(context.Background(), "enroll_node",
		json.RawMessage(`{"address":"192.168.178.61","username":"cd","confirm":true}`))

	joined := strings.Join(fake.calls, "\n")
	if !strings.Contains(joined, "-i /data/.ssh/deploy_ed25519") || !strings.Contains(joined, "IdentitiesOnly=yes") {
		t.Errorf("configured key not passed to ssh: %s", joined)
	}
}

// TestHubSSHIdentityHint covers the diagnostic builder against a real temp dir.
func TestHubSSHIdentityHint(t *testing.T) {
	dir := t.TempDir() + "/.ssh"
	if err := mkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	good := dir + "/id_ed25519"
	writeFile(good, "private", 0600)
	writeFile(good+".pub", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE1AAAAITest user@hub\n", 0644)
	open := dir + "/id_rsa"
	writeFile(open, "private", 0644) // too open: ssh refuses

	hint := hubSSHIdentityHint("", dir)
	if !strings.Contains(hint, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE1AAAAITest user@hub") {
		t.Errorf("pub line missing from hint: %s", hint)
	}
	if !strings.Contains(hint, "TOO OPEN") {
		t.Errorf("open-permissions key not flagged: %s", hint)
	}

	// explicit key path wins over the directory scan
	hint = hubSSHIdentityHint(good, "")
	if !strings.Contains(hint, "id_ed25519 (perm 0600)") || strings.Contains(hint, "id_rsa") {
		t.Errorf("keyPath not honored: %s", hint)
	}

	// existing but keyless dir -> actionable guidance
	empty := t.TempDir() + "/.ssh"
	mkdirAll(empty)
	hint = hubSSHIdentityHint("", empty)
	if !strings.Contains(hint, "no id_* identity files") {
		t.Errorf("empty dir not reported: %s", hint)
	}
}

// TestEnrollNodeLiveSSHAuthFailure drives enroll_node against a real sshd
// that does not trust this hub, verifying the auth hint end-to-end. Skipped
// unless HOMELABMON_ENROLL_SSH_REFUSAL is set (format: user@host:port).
func TestEnrollNodeLiveSSHAuthFailure(t *testing.T) {
	target := os.Getenv("HOMELABMON_ENROLL_SSH_REFUSAL")
	if target == "" {
		t.Skip("set HOMELABMON_ENROLL_SSH_REFUSAL=user@host:port to run against a real refusing sshd")
	}
	e, _ := newTestExecutor(t)
	e.SetEnrollEndpoint("9601", false)

	at := strings.Index(target, "@")
	sep := strings.LastIndex(target, ":")
	if at == -1 || sep == -1 || sep < at {
		t.Fatalf("invalid target format: %q (want user@host:port)", target)
	}
	username, address := target[:at], target[at+1:sep]
	port, _ := strconv.Atoi(target[sep+1:])

	out, err := e.Execute(context.Background(), "enroll_node",
		json.RawMessage(fmt.Sprintf(`{"address":%q,"username":%q,"port":%d,"confirm":true}`, address, username, port)))
	if err != nil {
		t.Fatalf("enroll_node: %v", err)
	}
	t.Logf("result: %s", truncate(out, 600))
	if !strings.Contains(out, "Permission denied") || !strings.Contains(out, `"hint"`) {
		t.Errorf("expected Permission denied with auth hint, got: %s", truncate(out, 400))
	}
}

// TestParseUnameIgnoresSSHWarnings covers the field-reported bug: on first
// contact ssh prints "Warning: Permanently added ..." into the combined
// output, which must not be mistaken for the uname result.
func TestParseUnameIgnoresSSHWarnings(t *testing.T) {
	out := "Warning: Permanently added '192.168.178.211' (ED25519) to the list of known hosts.\nLinux\nx86_64\n"
	goos, arch, errMsg := parseUname(out)
	if errMsg != "" || goos != "linux" || arch != "amd64" {
		t.Fatalf("warning polluted parse: goos=%q arch=%q err=%q", goos, arch, errMsg)
	}

	// unsupported arch is still detected correctly behind a warning
	out = "Warning: Permanently added 'h' (ED25519) to the list of known hosts.\nLinux\nsparc64\n"
	if _, _, errMsg = parseUname(out); errMsg == "" || !strings.Contains(errMsg, "sparc64") {
		t.Errorf("unsupported arch not surfaced: %q", errMsg)
	}

	// unsupported OS behind a warning
	out = "Warning: Permanently added 'h' (ED25519) to the list of known hosts.\nSunOS\nx86_64\n"
	_, _, errMsg = parseUname(out)
	// SunOS is a known OS keyword, so the arch parses; enrollNode rejects non-linux
	if errMsg != "" {
		t.Errorf("SunOS+x86_64 should parse: %q", errMsg)
	}
}

// TestEnrollNodeFreeBSDRcService verifies the OPNsense/FreeBSD path: rc.d
// script instead of systemd, and no sudo for the root user.
func TestEnrollNodeFreeBSDRcService(t *testing.T) {
	e, _, fake := newEnrollTestExecutor(t)
	fake.outputs["uname"] = "FreeBSD\namd64\n"
	dist := t.TempDir()
	writeFile(dist+"/homelabmon-freebsd-amd64", "binary", 0755)
	e.SetDeployDistDir(dist)
	e.SetSSHKeyPath("")
	enrollPollInterval = time.Millisecond
	enrollPollMax = 20 * time.Millisecond

	out, err := e.Execute(context.Background(), "enroll_node",
		json.RawMessage(`{"address":"192.168.178.2","username":"root","extra_args":"--exec","confirm":true}`))
	if err != nil {
		t.Fatalf("enroll_node: %v", err)
	}
	if !strings.Contains(out, `"verified":false`) {
		t.Fatalf("expected deployed-but-unverified result, got: %s", truncate(out, 400))
	}

	joined := strings.Join(fake.calls, "\n")
	if strings.Contains(joined, "sudo ") {
		t.Errorf("root user must not get sudo prefixes: %s", joined)
	}
	for _, want := range []string{
		"/usr/local/etc/rc.d/homelabmon",
		"sysrc homelabmon_enable=YES",
		"service homelabmon start",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("freebsd flow missing %q", want)
		}
	}
	// rc script content
	var unit string
	for _, c := range fake.calls {
		if strings.Contains(c, "rc.d/homelabmon") {
			unit = fake.stdins[calleeArgs(c)]
		}
	}
	for _, want := range []string{"# PROVIDE: homelabmon", "/usr/sbin/daemon", "export HOME=${homelabmon_home:-/root}", "--ui --scan --exec"} {
		if !strings.Contains(unit, want) {
			t.Errorf("rc script missing %q: %s", want, unit)
		}
	}
	if strings.Contains(unit, "--enroll") {
		t.Errorf("rc script must not contain enrollment flags: %s", unit)
	}
}

// TestEnrollNodeLinuxRootSkipsSudo verifies root targets run without sudo
// (minimal Linux systems and OPNsense do not ship it).
func TestEnrollNodeLinuxRootSkipsSudo(t *testing.T) {
	e, _, fake := newEnrollTestExecutor(t)
	enrollPollInterval = time.Millisecond
	enrollPollMax = 20 * time.Millisecond

	e.Execute(context.Background(), "enroll_node",
		json.RawMessage(`{"address":"192.168.178.70","username":"root","confirm":true}`))

	joined := strings.Join(fake.calls, "\n")
	if strings.Contains(joined, "sudo ") {
		t.Errorf("root user must not get sudo prefixes: %s", joined)
	}
	if !strings.Contains(joined, "systemctl enable --now homelabmon") {
		t.Errorf("linux systemd flow not executed: %s", joined)
	}
}

// TestEnrollRcScript checks the rc.d rendering helpers.
func TestEnrollRcScript(t *testing.T) {
	s := enrollRcScript("home", "--exec")
	if !strings.Contains(s, `--site 'home' --exec`) {
		t.Errorf("site/extra args not rendered: %s", s)
	}
	s = enrollRcScript("", "")
	if !strings.Contains(s, "--ui --scan") || strings.Contains(s, "--site") {
		t.Errorf("default args wrong: %s", s)
	}
}
