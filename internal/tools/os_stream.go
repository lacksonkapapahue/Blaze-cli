package tools

// Hooks for Blaze OS (internal/osweb): the web terminal runs commands through the
// same hard blocklist and risky-command check as run_shell, and streams their
// output as it arrives.

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
)

// Guard is the safety check run_shell applies to a command: why it is on the hard
// blocklist ("" = not blocked) and whether it should ask first.
func Guard(cmd string) (blockedWhy string, isRisky bool) {
	return blocked(cmd), risky(cmd)
}

// RunStream runs cmd in the shell like Run (cd sticks), writing its output to w as
// it arrives instead of collecting it. It returns the exit code (-1 when the shell
// couldn't start; 130 when ctx was cancelled).
func (s *Shell) RunStream(ctx context.Context, cmd string, w io.Writer) int {
	c := s.command(ctx, cmd, true)
	c.Stdin = nil
	c.Env = append(os.Environ(), "BLAZE=1", "GIT_TERMINAL_PROMPT=0", "PAGER=cat", "GIT_PAGER=cat", "DEBIAN_FRONTEND=noninteractive", "TERM=xterm-256color", "CLICOLOR_FORCE=1")
	f := &cwdFilter{w: w}
	c.Stdout = f
	c.Stderr = f
	err := c.Run()
	f.flush()
	if err != nil && c.ProcessState == nil {
		_, _ = io.WriteString(w, "couldn't start the shell: "+err.Error()+"\n")
		return -1
	}
	if dir := f.dir(); dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			s.mu.Lock()
			s.cwd = dir
			s.mu.Unlock()
		}
	}
	if ctx.Err() != nil {
		return 130
	}
	return c.ProcessState.ExitCode()
}

// cwdFilter passes output through but keeps back the cwd marker line the shell
// prints last.
type cwdFilter struct {
	mu   sync.Mutex
	w    io.Writer
	part string
	cwd  string
}

func (f *cwdFilter) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.part + string(b)
	f.part = ""
	var out strings.Builder
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		line := s[:i]
		s = s[i+1:]
		if t := strings.TrimRight(line, "\r"); strings.HasPrefix(t, cwdMark) {
			f.cwd = strings.TrimSpace(t[len(cwdMark):])
			continue
		}
		out.WriteString(line + "\n")
	}
	// a partial line that could be the start of the marker waits for more
	if s != "" && (strings.HasPrefix(cwdMark, s) || strings.HasPrefix(s, cwdMark)) {
		f.part = s
	} else {
		out.WriteString(s)
	}
	if out.Len() > 0 {
		if _, err := io.WriteString(f.w, out.String()); err != nil {
			return len(b), nil // the reader went away; keep the command running
		}
	}
	return len(b), nil
}

func (f *cwdFilter) flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(f.part, cwdMark) {
		f.cwd = strings.TrimSpace(f.part[len(cwdMark):])
	} else if f.part != "" {
		_, _ = io.WriteString(f.w, f.part)
	}
	f.part = ""
}

func (f *cwdFilter) dir() string { f.mu.Lock(); defer f.mu.Unlock(); return f.cwd }
