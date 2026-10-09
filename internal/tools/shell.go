package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Shell runs commands in the user's own shell and remembers the working
// directory between calls (cd sticks), like a real terminal session.
type Shell struct {
	mu   sync.Mutex
	cwd  string
	kind string // bash, zsh, sh, fish, pwsh, powershell, cmd
	path string
}

func NewShell(kind, path, cwd string) *Shell {
	// Agent commands use a POSIX shell or PowerShell; fish/cmd syntax differs too much.
	switch kind {
	case "fish", "tcsh", "csh", "nu", "xonsh":
		kind, path = "sh", "/bin/sh"
		if p, err := exec.LookPath("bash"); err == nil {
			kind, path = "bash", p
		}
	case "cmd":
		if p, err := exec.LookPath("powershell"); err == nil {
			kind, path = "powershell", p
		}
	}
	return &Shell{cwd: cwd, kind: kind, path: path}
}

func (s *Shell) Cwd() string { s.mu.Lock(); defer s.mu.Unlock(); return s.cwd }
func (s *Shell) Kind() string { return s.kind }
func (s *Shell) IsPowerShell() bool { return s.kind == "pwsh" || s.kind == "powershell" }

const cwdMark = "__BLAZE_CWD__"

// command builds the process for cmd, printing the final cwd on the last line.
func (s *Shell) command(ctx context.Context, cmd string, track bool) *exec.Cmd {
	if s.IsPowerShell() {
		script := cmd
		if track {
			script = "$ErrorActionPreference='Continue'; " + cmd + "\n$__c = if ($LASTEXITCODE -ne $null) { $LASTEXITCODE } elseif ($?) { 0 } else { 1 }; Write-Output ''; Write-Output ('" + cwdMark + "' + (Get-Location).Path); exit $__c"
		}
		c := exec.CommandContext(ctx, s.path, "-NoProfile", "-NonInteractive", "-Command", script)
		c.Dir = s.Cwd()
		return c
	}
	script := cmd
	if track {
		script = cmd + "\n__c=$?; echo; echo \"" + cwdMark + "$(pwd)\"; exit $__c"
	}
	c := exec.CommandContext(ctx, s.path, "-c", script)
	c.Dir = s.Cwd()
	return c
}

var hints = []struct {
	re   *regexp.Regexp
	hint string
}{
	{regexp.MustCompile(`(?i)command not found|is not recognized as|not found$`), "the program isn't installed: use install_tool, or check the name"},
	{regexp.MustCompile(`(?i)permission denied|access is denied`), "permission problem: check the path, or the command may need admin/sudo (ask the user first)"},
	{regexp.MustCompile(`(?i)no such file or directory|cannot find path`), "wrong path: use file_list to see what's there"},
	{regexp.MustCompile(`(?i)ModuleNotFoundError|No module named`), "missing Python package: run_code with packages=[...] installs it"},
	{regexp.MustCompile(`(?i)Cannot find module`), "missing Node package: run_code with packages=[...] or npm install"},
	{regexp.MustCompile(`(?i)address already in use|EADDRINUSE`), "the port is taken: pick another port or stop the old job"},
	{regexp.MustCompile(`(?i)could not resolve host|network is unreachable|temporary failure in name resolution`), "no internet or DNS problem"},
}

// Run executes cmd with a timeout and returns "exit N · cwd X" plus the output.
func (s *Shell) Run(cmd string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := s.command(ctx, cmd, true)
	c.Stdin = nil
	c.Env = append(os.Environ(), "BLAZE=1", "GIT_TERMINAL_PROMPT=0", "PAGER=cat", "GIT_PAGER=cat", "DEBIAN_FRONTEND=noninteractive")
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	err := c.Run()
	out := buf.String()
	code := 0
	if c.ProcessState != nil {
		code = c.ProcessState.ExitCode()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("exit 124 · timed out after %s\n%s\n[hint: long work or servers belong in run_shell background=true, then job_log]", timeout, tail(out, 6000))
	}
	if err != nil && c.ProcessState == nil {
		return "Error: couldn't start the shell: " + err.Error()
	}
	// Pull the cwd marker off the end.
	if i := strings.LastIndex(out, cwdMark); i >= 0 {
		nl := strings.IndexAny(out[i:], "\r\n")
		end := len(out)
		if nl >= 0 {
			end = i + nl
		}
		dir := strings.TrimSpace(out[i+len(cwdMark) : end])
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			s.mu.Lock()
			s.cwd = dir
			s.mu.Unlock()
		}
		out = strings.TrimRight(out[:i], "\r\n ")
	}
	res := fmt.Sprintf("exit %d · cwd %s\n%s", code, s.Cwd(), tail(out, 12000))
	if code != 0 {
		for _, h := range hints {
			if h.re.MatchString(out) {
				res += "\n[hint: " + h.hint + "]"
				break
			}
		}
	}
	return strings.TrimRight(res, "\n")
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

var danger = regexp.MustCompile(`(?i)(\brm\s+-[a-z]*r[a-z]*f?\s+(/|~|\*|\$HOME|\.\s*$)|\bmkfs|\bdd\s+if=|\bshutdown\b|\breboot\b|\bformat\s+[a-z]:|Remove-Item.+-Recurse.+([A-Z]:\\|~|\$HOME|\*)|\breg\s+(delete|add)\b|Set-ExecutionPolicy|\bdiskpart\b|\bchmod\s+-R\s+777\s+/|:\(\)\s*\{|>\s*/dev/sd|\bsudo\b|\bgit\s+push\s+.*--force|\bgit\s+reset\s+--hard|\bgit\s+clean\s+-[a-z]*f)`)

func risky(cmd string) bool { return danger.MatchString(cmd) }

// ---------- background jobs ----------

type job struct {
	ID      int
	Cmd     string
	Started time.Time
	proc    *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex
	log     bytes.Buffer
	done    bool
	code    int
}

func (j *job) Write(b []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.log.Write(b)
	if j.log.Len() > 400_000 {
		keep := j.log.Bytes()[j.log.Len()-300_000:]
		nb := append([]byte(nil), keep...)
		j.log.Reset()
		j.log.Write(nb)
	}
	return len(b), nil
}

func (j *job) text() string { j.mu.Lock(); defer j.mu.Unlock(); return j.log.String() }

type Jobs struct {
	mu   sync.Mutex
	next int
	all  []*job
}

func (js *Jobs) Start(sh *Shell, cmd string) (*job, error) {
	js.mu.Lock()
	js.next++
	j := &job{ID: js.next, Cmd: cmd, Started: time.Now()}
	js.all = append(js.all, j)
	js.mu.Unlock()
	c := sh.command(context.Background(), cmd, false)
	c.Env = append(os.Environ(), "BLAZE=1", "PYTHONUNBUFFERED=1", "GIT_TERMINAL_PROMPT=0")
	c.Stdout = j
	c.Stderr = j
	in, _ := c.StdinPipe()
	j.stdin = in
	j.proc = c
	if err := c.Start(); err != nil {
		return nil, err
	}
	go func() {
		_ = c.Wait()
		j.mu.Lock()
		j.done = true
		if c.ProcessState != nil {
			j.code = c.ProcessState.ExitCode()
		}
		j.mu.Unlock()
	}()
	return j, nil
}

func (js *Jobs) Get(id int) *job {
	js.mu.Lock()
	defer js.mu.Unlock()
	for _, j := range js.all {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func (js *Jobs) List() []*job { js.mu.Lock(); defer js.mu.Unlock(); return append([]*job(nil), js.all...) }

func (js *Jobs) StopAll() {
	for _, j := range js.List() {
		if !j.done && j.proc != nil && j.proc.Process != nil {
			_ = j.proc.Process.Kill()
		}
	}
}

func (j *job) state() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.done {
		return fmt.Sprintf("exited %d", j.code)
	}
	return "running " + time.Since(j.Started).Round(time.Second).String()
}

var portRe = regexp.MustCompile(`(?i)(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::\]):(\d{2,5})`)

func init() {
	register("run_shell", "Run a command in the user's shell on this PC (cd and exported variables stick between calls). Returns exit code, cwd and output. For servers or anything over ~2 minutes set background=true and follow with job_log. Not for writing files (file_write) or quick Python/Node snippets (run_code).",
		[]prop{p("command", "string", "The command"), p("background", "boolean", "Run as a background job (servers, watchers, long builds)"), p("timeout_s", "integer", "Foreground timeout, default 120, max 1800")},
		[]string{"command"}, func(e *Env, a args) string {
			cmd := strings.TrimSpace(a.s("command"))
			if cmd == "" {
				return "Error: empty command."
			}
			if risky(cmd) && !e.ask("Run a risky command?", cmd) {
				return "The user did NOT approve this command. Don't run it; suggest a safer way or ask them."
			}
			if a.b("background") {
				j, err := e.Jobs.Start(e.Shell, cmd)
				if err != nil {
					return "Error: couldn't start the job: " + err.Error()
				}
				e.status(fmt.Sprintf("Job %d started: %s", j.ID, cmd))
				time.Sleep(2 * time.Second)
				t := j.text()
				res := fmt.Sprintf("Started job %d (%s). First output:\n%s", j.ID, j.state(), tail(t, 1500))
				if m := portRe.FindStringSubmatch(t); m != nil {
					res += "\n[server on http://localhost:" + m[1] + " — open_url can show it to the user]"
				}
				return res
			}
			e.status("Running: " + firstLine(cmd))
			to := a.i("timeout_s", 120)
			if to < 5 {
				to = 5
			}
			if to > 1800 {
				to = 1800
			}
			return e.Shell.Run(cmd, time.Duration(to)*time.Second)
		})

	register("job_log", "Background jobs. No id: list every job with its state and last lines. With id: that job's latest output.",
		[]prop{p("id", "integer", "Job number"), p("lines", "integer", "Last N lines, default 80")}, nil,
		func(e *Env, a args) string {
			id := a.i("id", 0)
			if id == 0 {
				js := e.Jobs.List()
				if len(js) == 0 {
					return "No background jobs."
				}
				var b strings.Builder
				for _, j := range js {
					lines := strings.Split(strings.TrimRight(j.text(), "\n"), "\n")
					last := lines[len(lines)-1]
					fmt.Fprintf(&b, "Job %d [%s] %s\n   %s\n", j.ID, j.state(), j.Cmd, last)
				}
				return b.String()
			}
			j := e.Jobs.Get(id)
			if j == nil {
				return fmt.Sprintf("No job %d. Call job_log with no id to list them.", id)
			}
			lines := strings.Split(j.text(), "\n")
			n := a.i("lines", 80)
			if len(lines) > n {
				lines = lines[len(lines)-n:]
			}
			return fmt.Sprintf("Job %d [%s] %s\n%s", j.ID, j.state(), j.Cmd, strings.Join(lines, "\n"))
		})

	register("job_input", "Type a line into a running background job (answers a prompt, sends a command to a REPL).",
		[]prop{p("id", "integer", "Job number"), p("text", "string", "What to type (a newline is added)")}, []string{"id", "text"},
		func(e *Env, a args) string {
			j := e.Jobs.Get(a.i("id", 0))
			if j == nil || j.done {
				return "That job isn't running."
			}
			if _, err := io.WriteString(j.stdin, a.s("text")+"\n"); err != nil {
				return "Error: " + err.Error()
			}
			time.Sleep(1500 * time.Millisecond)
			return "Sent. Latest output:\n" + tail(j.text(), 1500)
		})

	register("job_stop", "Stop a background job.", []prop{p("id", "integer", "Job number")}, []string{"id"},
		func(e *Env, a args) string {
			j := e.Jobs.Get(a.i("id", 0))
			if j == nil {
				return "No such job."
			}
			if j.done {
				return fmt.Sprintf("Job %d had already %s.", j.ID, j.state())
			}
			if j.proc.Process != nil {
				_ = j.proc.Process.Kill()
			}
			return fmt.Sprintf("Stopped job %d.", j.ID)
		})
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i] + " …"
	}
	if len(s) > 100 {
		return s[:100] + "…"
	}
	return s
}
