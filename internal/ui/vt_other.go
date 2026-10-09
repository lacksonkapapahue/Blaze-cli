//go:build !windows

package ui

func enableVT() bool { return IsTTY() }
