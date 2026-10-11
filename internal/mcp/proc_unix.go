//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

// detach puts a local server in its own process group, so Ctrl-C in Blaze (which
// stops a turn) doesn't reach it, and killTree can stop anything it started.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
