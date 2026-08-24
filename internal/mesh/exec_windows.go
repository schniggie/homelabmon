//go:build windows

package mesh

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}

func killGroup(pid int) {
	// On Windows, Process.Kill terminates the process; WaitDelay ensures
	// Run() returns even if grandchildren keep the pipes open.
}
