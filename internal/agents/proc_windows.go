//go:build windows

package agents

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}

func interruptGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
