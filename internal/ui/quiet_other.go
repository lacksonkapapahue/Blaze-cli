//go:build !linux && !darwin

package ui

// Quiet is a no-op here: Windows consoles keep their own input mode.
func Quiet() func() { return func() {} }
