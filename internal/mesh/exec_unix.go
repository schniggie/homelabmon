//go:build !windows

package mesh

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group so the whole
// tree (shell + children) can be killed on timeout.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills every process in the command's group. Without this,
// killing the shell orphans its children, which keep the output pipes open
// and block Run() until they exit on their own.
func killGroup(pid int) {
	// Negative pid addresses the process group.
	syscall.Kill(-pid, syscall.SIGKILL)
}
