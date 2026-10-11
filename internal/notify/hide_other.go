//go:build !windows

package notify

import "os/exec"

func hide(cmd *exec.Cmd) {}
