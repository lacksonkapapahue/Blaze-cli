package ui

import (
	"os"

	"golang.org/x/term"
)

// readHidden reads a line without echo. It goes through In (raw mode, no echo)
// rather than term.ReadPassword, because the keyboard goroutine owns stdin.
func readHidden() (string, bool) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", false
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return "", false
	}
	defer term.Restore(fd, old)
	var b []rune
	for {
		r, _, err := In.ReadRune()
		if err != nil {
			return string(b), len(b) > 0
		}
		switch r {
		case '\r', '\n':
			return string(b), true
		case 3: // Ctrl-C
			return "", true
		case 127, 8:
			if len(b) > 0 {
				b = b[:len(b)-1]
			}
		case 21: // Ctrl-U
			b = nil
		case 27: // drop escape sequences (arrows, bracketed paste marks)
			for In.Buffered() > 0 {
				c, _, _ := In.ReadRune()
				if c >= 0x40 && c <= 0x7e && c != '[' {
					break
				}
			}
		default:
			if r >= 32 {
				b = append(b, r)
			}
		}
	}
}

// IsTTY is true when stdout is a terminal (not piped).
func IsTTY() bool { return term.IsTerminal(int(os.Stdout.Fd())) }
