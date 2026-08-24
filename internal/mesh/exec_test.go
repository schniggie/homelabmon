package mesh

import (
	"strings"
	"testing"
	"time"
)

func TestShellCommandMatrix(t *testing.T) {
	cases := []struct {
		goos, shell, command string
		name                 string
		args                 []string
	}{
		{"linux", "", "uptime", "sh", []string{"-c", "uptime"}},
		{"darwin", "cmd", "sw_vers", "sh", []string{"-c", "sw_vers"}}, // shell ignored on unix
		{"freebsd", "", "pkg upgrade -y", "sh", []string{"-c", "pkg upgrade -y"}},
		{"windows", "", "dir", "cmd", []string{"/C", "dir"}},
		{"windows", "cmd", "dir", "cmd", []string{"/C", "dir"}},
		{"windows", "powershell", "Get-Service", "powershell", []string{"-NoProfile", "-Command", "Get-Service"}},
	}
	for _, c := range cases {
		name, args := shellCommand(c.goos, c.shell, c.command)
		if name != c.name || strings.Join(args, " ") != strings.Join(c.args, " ") {
			t.Errorf("shellCommand(%q,%q): got %s %v, want %s %v", c.goos, c.shell, name, args, c.name, c.args)
		}
	}
}

func TestRunCommandOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("executes real commands")
	}
	res := runCommand("linux", "", "echo hello-$(id -u)", 10*time.Second)
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "hello-") {
		t.Errorf("expected successful echo, got %+v", res)
	}
}

func TestRunCommandExitCode(t *testing.T) {
	if testing.Short() {
		t.Skip("executes real commands")
	}
	res := runCommand("linux", "", "echo boom >&2; exit 42", 10*time.Second)
	if res.ExitCode != 42 || !strings.Contains(res.Stderr, "boom") {
		t.Errorf("expected exit 42 with stderr, got %+v", res)
	}
}

func TestRunCommandTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("executes real commands")
	}
	res := runCommand("linux", "", "sleep 30", 2*time.Second)
	if !res.TimedOut {
		t.Errorf("expected timeout, got %+v", res)
	}
}

func TestRunCommandOutputCap(t *testing.T) {
	if testing.Short() {
		t.Skip("executes real commands")
	}
	res := runCommand("linux", "", "yes spam", 3*time.Second)
	if len(res.Stdout) > 40*1024 {
		t.Errorf("stdout not capped: %d bytes", len(res.Stdout))
	}
	if !strings.Contains(res.Stdout, "output truncated") {
		t.Errorf("expected truncation marker, got %d bytes", len(res.Stdout))
	}
}

func TestExecDisabledByDefault(t *testing.T) {
	tr := &Transport{execEnabled: false}
	if tr.execEnabled {
		t.Fatal("exec must default to disabled")
	}
}
