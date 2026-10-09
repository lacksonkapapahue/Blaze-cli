// Package agent is Blaze's loop on the PC: think, call tools, check, answer.
package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

type Agent struct {
	Cfg     *config.Config
	Env     *tools.Env
	History []llm.Message
	Quiet   bool // one-shot / scheduled: no spinner
	spin    *ui.Spinner
	diffed  bool // a diff was drawn for the current tool call
}

func New(cfg *config.Config, info *sysinfo.Info, cwd string, yolo bool) *Agent {
	a := &Agent{Cfg: cfg, spin: &ui.Spinner{}}
	a.Env = &tools.Env{
		Info:  info,
		Root:  cwd,
		Yolo:  yolo || cfg.AutoApprove,
		Shell: tools.NewShell(info.Shell, info.ShellPath, cwd),
		Jobs:  &tools.Jobs{},
	}
	a.Env.Confirm = func(title, detail string) bool {
		a.spin.Stop()
		return ui.Confirm(title, detail)
	}
	a.Env.OnDiff = func(path, before, after string) {
		if a.Quiet {
			return
		}
		a.spin.Stop()
		ui.Diff(path, before, after)
		a.diffed = true
	}
	a.Env.OnTodo = func(items []tools.Todo) {
		if a.Quiet {
			return
		}
		a.spin.Stop()
		rows := make([]ui.TodoRow, len(items))
		for i, t := range items {
			rows[i] = ui.TodoRow{Text: t.Text, Status: t.Status}
		}
		ui.Todos(rows)
	}
	a.Env.Status = func(s string) {
		if a.Quiet {
			return
		}
		a.spin.Set(s)
	}
	return a
}

func (a *Agent) system() string {
	var b strings.Builder
	b.WriteString(`You are Blaze, a capable assistant running in the user's terminal on their own PC. You act, not just advise: use your tools to run commands, read and edit files, write and run code, use git, search the web and install what's missing.

RULES
- Know before you ask: the PC profile below tells you the OS, shell, package managers and installed tools. Never ask the user what OS or shell they use.
- Write commands for THIS shell (PowerShell syntax on Windows, POSIX elsewhere).
- Look before you change: file_list / file_read / git status first. Make the smallest correct change. After changing code, run it or its tests to check.
- Long-running things (servers, watchers, builds over ~2 min) go in run_shell background=true; then job_log.
- Missing program? install_tool (it asks the user). Never use sudo or admin rights without asking.
- Be honest about failures: say what failed and what you tried. Never claim something worked without checking.
- Answers: short and direct, plain Markdown. Lead with the result. Use numbered steps only for real sequences.
- Stay in the folder the user started in unless they point elsewhere.
`)
	b.WriteString(`- For tasks with 3+ steps, keep a checklist with todo_write and update it as you go.
- The user can attach files as <file path=...> blocks and run shell commands themselves (shown as <shell> blocks); use what they contain.
`)
	if a.Env.Plan {
		b.WriteString("\nPLAN MODE IS ON\nDon't change anything: no writes, edits, installs or commands that alter state (those tools are blocked). Read and investigate, then answer with a short numbered plan of exactly what you'll change and how you'll check it. Stop there; the user approves before you act.\n")
	}
	if pi := ProjectInstructions(a.Env.Root); pi != "" {
		b.WriteString("\nPROJECT INSTRUCTIONS (BLAZE.md)\n" + pi + "\n")
	}
	if a.Cfg.Persona != "" {
		b.WriteString("\nSTYLE\n" + a.Cfg.Persona + "\n")
	}
	b.WriteString("\nPC PROFILE\n" + a.Env.Info.Summary() + "\n")
	b.WriteString("\nStarted in: " + a.Env.Root + "\nCurrent folder: " + a.Env.Shell.Cwd() + "\n")
	if _, err := os.Stat(a.Env.Root + string(os.PathSeparator) + ".git"); err == nil {
		b.WriteString("This folder is a git repository.\n")
	}
	if m := tools.Memory(); m != "" {
		b.WriteString("\nWHAT YOU KNOW ABOUT THE USER\n" + m + "\n")
	}
	if sk := tools.Skills(); len(sk) > 0 {
		b.WriteString("\nSKILLS (load with use_skill before that kind of task)\n")
		for _, s := range sk {
			b.WriteString("- " + s.Name + ": " + s.Desc + "\n")
		}
	}
	b.WriteString("\nNow: " + time.Now().Format("Mon 2 Jan 2006 15:04 MST") + "\n")
	return b.String()
}

// Ask runs one user turn to completion and returns the final text.
func (a *Agent) Ask(ctx context.Context, prompt string) (string, error) {
	a.History = append(a.History, llm.Message{Role: "user", Content: prompt})
	a.trim()
	all := tools.All()
	started := time.Now()
	var final string
	for step := 0; step < a.Cfg.MaxSteps; step++ {
		msgs := append([]llm.Message{{Role: "system", Content: a.system()}}, a.History...)
		md := &ui.MD{}
		if !a.Quiet {
			a.spin.Start("thinking")
			opened := false
			md.Pre = func() {
				a.spin.Stop()
				if !opened {
					ui.Println("")
					opened = true
				}
			}
			md.Post = func() { a.spin.Start("writing") }
		}
		r, err := llm.Complete(ctx, a.Cfg, msgs, all, func(s string) {
			if a.Quiet {
				return
			}
			a.spin.Set("writing")
			md.Write(s)
		})
		if !a.Quiet {
			md.Flush()
		}
		a.spin.Stop()
		if err != nil {
			// Keep history valid: drop the unanswered user turn on hard failure of the first step.
			if step == 0 {
				a.History = a.History[:len(a.History)-1]
			}
			return "", err
		}
		a.History = append(a.History, llm.Message{Role: "assistant", Content: r.Text, ToolCalls: r.ToolCalls})
		if len(r.ToolCalls) == 0 {
			final = r.Text
			if !a.Quiet {
				ui.Footer(r.Route, time.Since(started))
			}
			return final, nil
		}
		for _, tc := range r.ToolCalls {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if !a.Quiet {
				a.spin.Start(describe(tc))
			}
			t0 := time.Now()
			a.diffed = false
			res := tools.Run(a.Env, tc.Function.Name, tc.Function.Arguments)
			a.spin.Stop()
			quietStep := tc.Function.Name == "todo_write" || (a.diffed && succeeded(res))
			if !a.Quiet && !quietStep {
				ui.StepDone(succeeded(res), describe(tc), summarize(res), time.Since(t0))
			}
			a.History = append(a.History, llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: res})
		}
	}
	return final, fmt.Errorf("stopped after %d steps without finishing; say \"continue\" to keep going", a.Cfg.MaxSteps)
}

func describe(tc llm.ToolCall) string {
	n := tc.Function.Name
	arg := tc.Function.Arguments
	pick := func(k string) string {
		i := strings.Index(arg, `"`+k+`"`)
		if i < 0 {
			return ""
		}
		rest := arg[i+len(k)+2:]
		j := strings.Index(rest, `"`)
		if j < 0 {
			return ""
		}
		rest = rest[j+1:]
		end := strings.Index(rest, `"`)
		if end < 0 {
			end = len(rest)
		}
		v := rest[:end]
		if len(v) > 70 {
			v = v[:70] + "…"
		}
		return v
	}
	switch n {
	case "run_shell":
		return "$ " + pick("command")
	case "file_read", "file_write", "file_edit", "file_delete", "file_list":
		return strings.TrimPrefix(n, "file_") + " " + pick("path")
	case "git":
		return "git " + pick("args")
	case "web_search":
		return "search " + pick("query")
	case "fetch_page":
		return "read " + pick("url")
	case "run_code":
		return "run " + pick("language")
	}
	return strings.ReplaceAll(n, "_", " ")
}

// succeeded reads a tool result for failure: errors, non-zero exits, declines.
func succeeded(res string) bool {
	t := strings.TrimSpace(res)
	low := strings.ToLower(t)
	switch {
	case strings.HasPrefix(t, "Error"), strings.HasPrefix(t, "Refused"), strings.HasPrefix(t, "Plan mode is on"), strings.HasPrefix(low, "declined"), strings.Contains(low, "did not approve"):
		return false
	case strings.HasPrefix(t, "exit ") && !strings.HasPrefix(t, "exit 0"):
		return false
	}
	return true
}

func summarize(res string) string {
	l := strings.SplitN(strings.TrimSpace(res), "\n", 2)[0]
	if len(l) > 60 {
		l = l[:60] + "…"
	}
	return l
}

// trim keeps the conversation inside a sane window without splitting tool pairs.
func (a *Agent) trim() {
	const max = 60
	if len(a.History) <= max {
		return
	}
	cut := len(a.History) - max
	for cut < len(a.History) && a.History[cut].Role != "user" {
		cut++
	}
	a.History = a.History[cut:]
}

func (a *Agent) Reset() { a.History = nil; tools.ResetTodos() }

func (a *Agent) Close() { a.Env.Jobs.StopAll() }
