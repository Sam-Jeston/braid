//go:build !windows

package host

import (
	"os/exec"
	"syscall"
)

func hideWindow(cmd *exec.Cmd) {}

// SetProcessGroup starts cmd in its own process group, so InterruptGroup and
// KillGroup reach the children it starts too.
func SetProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func InterruptGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	}
}

func KillGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
