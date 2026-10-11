//go:build windows

package mcp

import (
	"os/exec"
	"strconv"
	"syscall"
)

const createNoWindow = 0x08000000

// detach starts a local server without a console window of its own (no flashing
// window, and Ctrl-C in Blaze's console doesn't reach it).
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// killTree stops the server and its children (npx → node, cmd → …).
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	k := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	k.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	_ = k.Run()
	_ = cmd.Process.Kill()
}
