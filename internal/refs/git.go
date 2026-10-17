package refs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// hideCmd keeps git from flashing a console window on Windows (proc_windows.go).
var hideCmd = func(*exec.Cmd) {}

// maxGitOut caps what is read from one git command.
const maxGitOut = 4 * 1024 * 1024

// runGit runs a read-only git command in dir: started directly with an argument
// list (never a shell), no pager, no prompts, no optional locks, and external
// diff drivers and textconv filters off (--no-ext-diff --no-textconv are passed
// by the callers), so a repo's config can't run programs through @diff.
func runGit(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git isn't installed")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := append([]string{"--no-pager", "-c", "core.quotepath=off", "-c", "color.ui=never"}, args...)
	c := exec.CommandContext(ctx, "git", argv...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "PAGER=cat")
	hideCmd(c)
	out := &limitBuf{max: maxGitOut}
	var errb bytes.Buffer
	c.Stdout, c.Stderr = out, &errb
	err := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", errors.New("git took too long")
	}
	if err != nil && !out.full {
		msg := strings.TrimSpace(errb.String())
		if strings.Contains(strings.ToLower(msg), "not a git repository") {
			return "", errors.New("not a git repository")
		}
		if strings.Contains(msg, "does not have any commits") {
			return "", nil
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return out.b.String(), nil
}

// limitBuf keeps the first max bytes written and drops the rest.
type limitBuf struct {
	b    bytes.Buffer
	max  int
	full bool
}

func (l *limitBuf) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
			l.full = true
		} else {
			l.b.Write(p)
		}
	} else {
		l.full = true
	}
	return len(p), nil
}
