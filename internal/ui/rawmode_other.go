//go:build !linux && !darwin

package ui

import "golang.org/x/term"

// makeRaw is raw keyboard input (Windows: the console's output side keeps its
// virtual-terminal processing, so "\n" still starts a new line).
func makeRaw(fd int) (func(), error) {
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { _ = term.Restore(fd, old) }, nil
}
