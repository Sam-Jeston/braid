//go:build windows

package host

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// hideWindow keeps the desktop app, a GUI process, from flashing a console
// window for every child it starts.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// SetProcessGroup does nothing on Windows. Killing wsl.exe ends the Linux
// processes it started; a native CLI's own children aren't tracked.
func SetProcessGroup(cmd *exec.Cmd) {}

func InterruptGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func KillGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
