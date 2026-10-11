//go:build windows

package connect

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a local MCP server from flashing a console window.
func hideWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
