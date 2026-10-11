package ui

import (
	"io"
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

// readEchoed reads a line in Quiet mode (no tty echo, no line buffering), echoing
// it itself: printable keys, Backspace, Ctrl-U, Enter; Ctrl-D on an empty line is
// end of input. Escape sequences (arrows, focus reports) are dropped. Ctrl-C
// still raises SIGINT, as in a cooked read.
func readEchoed() (string, error) {
	var b []rune
	for {
		r, _, err := In.ReadRune()
		if err != nil {
			return string(b), err
		}
		switch r {
		case '\r', '\n':
			Printf("\r\n")
			return string(b), nil
		case 4: // Ctrl-D
			if len(b) == 0 {
				Printf("\r\n")
				return "", io.EOF
			}
		case 127, 8:
			if len(b) > 0 {
				b = b[:len(b)-1]
				Printf("\b \b")
			}
		case 21: // Ctrl-U
			for range b {
				Printf("\b \b")
			}
			b = nil
		case 27:
			for In.Buffered() > 0 {
				c, _, _ := In.ReadRune()
				if c >= 0x40 && c <= 0x7e && c != '[' {
					break
				}
			}
		default:
			if r >= 32 {
				b = append(b, r)
				Printf("%c", r)
			}
		}
	}
}

// IsTTY is true when stdout is a terminal (not piped).
func IsTTY() bool { return term.IsTerminal(int(os.Stdout.Fd())) }
