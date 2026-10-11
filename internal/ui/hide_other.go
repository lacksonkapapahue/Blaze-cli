//go:build !windows

package ui

import "os/exec"

func hideConsole(*exec.Cmd) {}
