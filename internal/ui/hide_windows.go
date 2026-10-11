package ui

import (
	"os/exec"
	"syscall"
)

// hideConsole keeps a helper (PowerShell) from flashing a console window.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
