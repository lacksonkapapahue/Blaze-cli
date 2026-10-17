//go:build linux || darwin

package ui

import (
	"bytes"
	"os"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// queryBackground asks a local terminal for its background colour (OSC 11).
// It gives up after ~100ms (a bit longer once an answer has started arriving)
// and hands any keys typed meanwhile back to the keyboard reader.
func queryBackground() (light, ok bool) {
	if !color || os.Getenv("TERM") == "dumb" || os.Getenv("BLAZE_NO_OSC11") != "" {
		return false, false
	}
	if remoteTerminal() {
		return false, false
	}
	if !IsTTY() || !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, false
	}
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return false, false
	}
	defer term.Restore(fd, old)

	q := "\x1b]11;?\x1b\\"
	if _, err := os.Stdout.WriteString(q); err != nil {
		return false, false
	}

	var got []byte
	start := time.Now()
	deadline := start.Add(100 * time.Millisecond)
	extended := false
	buf := make([]byte, 256)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int(left/time.Millisecond)+1)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n == 0 {
			if n == 0 && !extended && bytes.Contains(got, []byte("\x1b")) {
				// an answer is on its way (slow link): give it a little longer
				extended = true
				deadline = start.Add(300 * time.Millisecond)
				continue
			}
			break
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 && fds[0].Revents&unix.POLLIN == 0 {
			break
		}
		m, err := unix.Read(fd, buf)
		if m > 0 {
			got = append(got, buf[:m]...)
		}
		if err != nil || m <= 0 {
			break
		}
		if osc11ReplyComplete(got) {
			break
		}
	}
	light, ok, rest := splitReplies(got)
	if len(rest) > 0 {
		giveBackKeys(rest)
	}
	return light, ok
}

// giveBackKeys puts keys read during the query in front of the keyboard reader's
// input, so the prompt still gets them.
func giveBackKeys(b []byte) {
	stdinR.mu.Lock()
	stdinR.left = append(append([]byte(nil), b...), stdinR.left...)
	stdinR.mu.Unlock()
}
