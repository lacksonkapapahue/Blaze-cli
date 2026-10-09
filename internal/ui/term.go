package ui

import (
	"os"

	"golang.org/x/term"
)

func readHidden() (string, bool) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", false
	}
	b, err := term.ReadPassword(fd)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// IsTTY is true when stdout is a terminal (not piped).
func IsTTY() bool { return term.IsTerminal(int(os.Stdout.Fd())) }
