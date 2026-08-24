package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/rs/zerolog/log"
)

// Exec limits: commands may run long (upgrades, package installs) but must
// not hang forever or flood the mesh with output.
const (
	execMaxTimeoutSec = 600       // 10 minutes
	execMaxOutput     = 32 * 1024 // per stream, stdout and stderr each
)

// ExecResult is the outcome of a remote command execution.
type ExecResult struct {
	HostID     string `json:"host_id"`
	OS         string `json:"os"`
	Command    string `json:"command"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	DurationMs int64  `json:"duration_ms"`
}

// SetExecEnabled turns the /api/v1/exec endpoint on. Disabled by default:
// enabling it allows command execution on this node by anyone who can reach
// the mesh port -- use mTLS on untrusted networks.
func (t *Transport) SetExecEnabled(enabled bool) { t.execEnabled = enabled }

// shellCommand builds the interpreter invocation for a command on the given
// OS. Windows supports "cmd" (default) and "powershell"; everything else
// (Linux, macOS, FreeBSD/OPNsense, ...) uses /bin/sh.
func shellCommand(goos, shell, command string) (string, []string) {
	if goos == "windows" {
		if shell == "powershell" {
			return "powershell", []string{"-NoProfile", "-Command", command}
		}
		return "cmd", []string{"/C", command}
	}
	return "sh", []string{"-c", command}
}

// runCommand executes a command with the node's native shell and returns its
// result. Output streams are capped at execMaxOutput each.
func runCommand(goos, shell, command string, timeout time.Duration) *ExecResult {
	start := time.Now()

	name, args := shellCommand(goos, shell, command)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	setProcessGroup(cmd)
	// On timeout, kill the whole process group (shell + children); orphaned
	// children would otherwise hold the output pipes open and block Run().
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			killGroup(cmd.Process.Pid)
			return cmd.Process.Kill()
		}
		return nil
	}
	// Hard stop: if anything still holds the pipes after the group kill,
	// abandon the copy goroutines instead of hanging.
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	stdoutW := &limitedWriter{buf: &stdout, max: execMaxOutput}
	stderrW := &limitedWriter{buf: &stderr, max: execMaxOutput}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	err := cmd.Run()
	duration := time.Since(start)

	if stdoutW.truncated {
		stdout.WriteString("\n[homelabmon] output truncated")
	}
	if stderrW.truncated {
		stderr.WriteString("\n[homelabmon] output truncated")
	}

	res := &ExecResult{
		OS:         goos,
		Command:    command,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMs: duration.Milliseconds(),
	}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		res.Stderr += "\n[homelabmon] command timed out"
	} else if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
			res.Stderr += "\n[homelabmon] " + err.Error()
		}
	}
	return res
}

// limitedWriter caps the recorded output of a command stream.
type limitedWriter struct {
	buf       *bytes.Buffer
	max       int
	truncated bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.max {
		w.truncated = true
		return len(p), nil // discard, pretend all consumed
	}
	room := w.max - w.buf.Len()
	if len(p) > room {
		w.buf.Write(p[:room])
		w.truncated = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

// handleExec runs a command on this node. The endpoint only exists when the
// node was started with --exec.
func (t *Transport) handleExec(w http.ResponseWriter, r *http.Request) {
	if !t.execEnabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "remote command execution is disabled on this node (start the agent with --exec)",
		})
		return
	}

	var req struct {
		Command    string `json:"command"`
		Shell      string `json:"shell"` // windows: "", "cmd", "powershell"; ignored elsewhere
		TimeoutSec int    `json:"timeout_seconds"`
	}
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	if req.Command == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command is required"})
		return
	}
	if req.TimeoutSec <= 0 {
		req.TimeoutSec = 120
	}
	if req.TimeoutSec > execMaxTimeoutSec {
		req.TimeoutSec = execMaxTimeoutSec
	}

	log.Warn().
		Str("command", req.Command).
		Str("remote", r.RemoteAddr).
		Msg("executing remote command")

	res := runCommand(runtime.GOOS, req.Shell, req.Command, time.Duration(req.TimeoutSec)*time.Second)
	res.HostID = t.identity.ID
	writeJSON(w, http.StatusOK, res)
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, v interface{}) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return err
	}
	return nil
}
