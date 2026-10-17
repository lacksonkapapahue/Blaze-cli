//go:build windows

package clip

import (
	"os/exec"
	"syscall"
)

func init() {
	hideCmd = func(c *exec.Cmd) {
		c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	}
}
