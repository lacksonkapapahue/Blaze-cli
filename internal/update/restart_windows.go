//go:build windows

package update

import (
	"os"
	"os/exec"
)

// Restart starts the new binary in this console with the same arguments and exits
// with its exit code when it ends (Windows has no exec).
func Restart(self string, args []string) error {
	cmd := exec.Command(self, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Wait()
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	os.Exit(code)
	return nil
}
