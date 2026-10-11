//go:build linux || darwin

package ui

import "golang.org/x/sys/unix"

// makeRaw is raw input (no echo, no line buffering, no signals: Ctrl+C reaches the
// prompt as a key) with output processing kept, so "\n" still starts a new line
// for everything a turn prints while the prompt is open.
func makeRaw(fd int) (func(), error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGet)
	if err != nil {
		return nil, err
	}
	t := *old
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	t.Oflag |= unix.OPOST | unix.ONLCR
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, ioctlSet, &t); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlSet, old) }, nil
}
