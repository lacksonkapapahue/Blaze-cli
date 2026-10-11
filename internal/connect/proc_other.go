//go:build !windows

package connect

import "os/exec"

func hideWindow(c *exec.Cmd) {}
