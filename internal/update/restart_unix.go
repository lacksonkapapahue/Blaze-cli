//go:build !windows

package update

import (
	"os"
	"syscall"
)

// Restart replaces this process with the (new) binary at self, same arguments.
// It only returns on failure.
func Restart(self string, args []string) error {
	return syscall.Exec(self, append([]string{self}, args...), os.Environ())
}
