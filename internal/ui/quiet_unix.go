//go:build linux || darwin

package ui

import (
	"os"

	"golang.org/x/sys/unix"
)

// Quiet switches the terminal to a "working" mode while a task runs: no echo and
// no line buffering, but Ctrl-C and output processing stay as they are. Keys typed
// meanwhile are kept for the prompt (they show up in it when it reopens), and the
// terminal's focus reports arrive at once instead of being echoed as ^[[O. It
// returns the function that puts the terminal back.
func Quiet() func() { restore, _ := quiet(); return restore }

// quiet is Quiet that also says whether the mode was switched.
func quiet() (func(), bool) {
	if !StdinTTY() || !IsTTY() {
		return func() {}, false
	}
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlGet)
	if err != nil {
		return func() {}, false
	}
	t := *old
	t.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN // IEXTEN off: Ctrl+O is not "discard output" (macOS), Ctrl+V not "literal next"
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	if unix.IoctlSetTermios(fd, ioctlSet, &t) != nil {
		return func() {}, false
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlSet, old) }, true
}
