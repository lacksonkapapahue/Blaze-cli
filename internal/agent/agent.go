// Package agent is Blaze's loop on the PC: think, call tools, check, answer.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/checkpoint"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/learn"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/steps"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

type Agent struct {
	Cfg     *config.Config
	Env     *tools.Env
	History []llm.Message // written only through push/SetHistory (background agents read it via SystemPrompt)
	hmu     sync.RWMutex
	Quiet   bool // one-shot / scheduled: no spinner
	spin    *ui.Spinner
	diffed  bool // a diff was drawn for the current tool call
	cur     *ui.ActStep

	inMu  sync.Mutex
	inbox []Note       // what the user said while the turn ran (inbox.go)
	now   atomic.Value // what the turn is doing, in plain words (string)

	// Approval is the approval mode: "auto" (risky actions ask first) or
	// "autopilot" (nothing asks; catastrophic actions stay blocked; questions get
	// an automatic "decide yourself" reply). The work mode is Env.Plan (Plan/Build).
	Approval string

	// Host hooks (all optional): extra tools (background agents, connectors,
	// update_blaze, notify), a runner for them, which of them only read (allowed
	// in Plan mode) and extra system prompt text.
	Extra       func() []llm.Tool
	Handle      func(ctx context.Context, name, args string) (string, bool)
	ReadOnly    func(name string) bool
	ExtraSystem func() string

	// Handoff, when set, is asked after a step's tool calls ran: true ends the turn
	// at once (the main chat handed the work to agents and stays free, v0.7.0).
	Handoff func(calls []llm.ToolCall) bool
	// Checkpoints, when set, saves each file's prior content before Blaze changes it
	// (v0.8.0, /rewind). The host opens a turn per message (Begin/End).
	Checkpoints *checkpoint.Session
	// ChangePaths, when set, names the files a host tool is about to change (applying
	// a specialist's changes), so they are checkpointed too.
	ChangePaths func(name, args string) []string
	// Dropped counts messages trim cut from the front, so a checkpoint's position in
	// the conversation stays valid.
	Dropped int

	// ShowSteps prints a plain line for each finished step (BLAZE_STEPS=all);
	// otherwise a turn shows only the working line and collapses to "Worked · N steps".
	ShowSteps bool

	// images the user attached (AskImages), kept beside the history rather than in
	// it so saved sessions stay small
	imgMu  sync.Mutex
	images []imageNote
}

// imageNote is the images sent with one user message: attached to that message
// (matched by its text) in what is sent, for its own turn and the next one.
type imageNote struct {
	content string
	urls    []string
	age     int // turns started since
}

// AskImages is Ask with images (data URLs, "data:image/png;base64,…") on the
// user's message. The model sees them for this turn and the next; the saved
// history keeps only the text (its "[image N]" labels).
func (a *Agent) AskImages(ctx context.Context, prompt string, images []string) (string, error) {
	if len(images) > 0 {
		a.imgMu.Lock()
		a.images = append(a.images, imageNote{content: prompt, urls: images, age: -1})
		a.imgMu.Unlock()
	}
	return a.Ask(ctx, prompt)
}

// ageImages starts a turn for the image notes, dropping those two turns old.
func (a *Agent) ageImages() {
	a.imgMu.Lock()
	defer a.imgMu.Unlock()
	keep := a.images[:0]
	for _, n := range a.images {
		if n.age++; n.age <= 1 {
			keep = append(keep, n)
		}
	}
	a.images = keep
}

// withImages puts the noted images on their user messages in msgs (a copy of
// the history about to be sent).
func (a *Agent) withImages(msgs []llm.Message) {
	a.imgMu.Lock()
	defer a.imgMu.Unlock()
	for _, n := range a.images {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" && msgs[i].Content == n.content {
				msgs[i].Images = n.urls
				break
			}
		}
	}
}

// HandoffLine is said when the model handed work off without a word.
const HandoffLine = "On it. I'll report back when it's done."

// complete is the model call (a var so tests can fake it).
var complete = llm.Complete

// Autopilot reports whether the approval mode is Autopilot.
func (a *Agent) Autopilot() bool { return a.Approval == "autopilot" }

// StopSpinner clears the working line (before a prompt from outside the loop).
func (a *Agent) StopSpinner() { a.spin.Stop() }

func New(cfg *config.Config, info *sysinfo.Info, cwd string, yolo bool) *Agent {
	a := &Agent{Cfg: cfg, spin: &ui.Spinner{}, Approval: "auto"}
	if yolo || cfg.AutoApprove {
		a.Approval = "autopilot"
	}
	a.Env = &tools.Env{
		Info:  info,
		Root:  cwd,
		Shell: tools.NewShell(info.Shell, info.ShellPath, cwd),
		Jobs:  &tools.Jobs{},
	}
	a.Env.Confirm = a.gate
	a.Env.OnDiff = func(path, before, after string) {
		if a.Quiet {
			return
		}
		if a.cur != nil { // kept for Ctrl+O; no code while working
			a.cur.DiffPath, a.cur.Before, a.cur.After = path, before, after
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
		done := 0
		for _, t := range items {
			if t.Status == "done" {
				done++
			}
		}
		ui.SetTurnProgress(done, len(items))
		ui.Todos(rows)
	}
	a.Env.Status = func(s string) {
		if a.Quiet {
			return
		}
		if p := steps.PlainStatus(s); p != "" {
			a.spin.Set(p) // tools report "Running: <command>"; only plain words show
		}
	}
	a.ShowSteps = os.Getenv("BLAZE_STEPS") == "all"
	return a
}

// SystemPrompt is the system prompt for this agent's next step (background agents
// start from it too).
func (a *Agent) SystemPrompt() string { return a.system() }

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
- The user can attach files as <file path=...> blocks, @ references as <context ref="..."> blocks (files, folders, git diffs and logs, web pages) and images ([image N] in their message), and run shell commands themselves (shown as <shell> blocks); use what they contain.
`)
	b.WriteString(tools.AboutBrief())
	if a.Env.Plan {
		b.WriteString("\nPLAN MODE IS ON\nDon't change anything: no writes, edits, installs or commands that alter state (those tools are blocked). Read and investigate, then answer with a short numbered plan of exactly what you'll change and how you'll check it. Stop there; the user approves before you act.\n")
	}
	if a.Autopilot() && !a.Env.Plan {
		b.WriteString("\nAUTOPILOT IS ON\nThe user is away: work the task through to the end without asking questions. Make reasonable decisions yourself, note your assumptions, check your work, then finish with a short summary.\n")
	}
	if a.ExtraSystem != nil {
		b.WriteString(a.ExtraSystem())
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
	q := recentQuery(a.historyCopy())
	if m := tools.Memory(q); m != "" {
		b.WriteString("\nWHAT YOU KNOW ABOUT THE USER (ids let remember replace and forget remove a memory)\n" + m + "\n")
	}
	b.WriteString(tools.SkillsFor(q))
	b.WriteString(learn.Rules(LearnOn()))
	b.WriteString("\nNow: " + time.Now().Format("Mon 2 Jan 2006 15:04 MST") + "\n")
	return b.String()
}

// Ask runs one user turn to completion and returns the final text.
func (a *Agent) Ask(ctx context.Context, prompt string) (_ string, err error) {
	a.ageImages()
	a.push(llm.Message{Role: "user", Content: prompt})
	a.trim()
	started := time.Now()
	act := &ui.Activity{}
	if !a.Quiet {
		ui.BeginTurn()
	}
	a.setNow("reading your request")
	defer func() {
		ui.EndTurn()
		a.setNow("")
		a.cur = nil
		act.Seconds = time.Since(started).Seconds()
		if a.Quiet {
			return
		}
		if len(act.Steps) > 0 {
			ui.SetActivity(act) // Ctrl+O expands the last turn that did something
		}
		if err != nil && len(act.Steps) > 0 {
			ui.WorkedTurn("", act, time.Since(started))
		}
	}()
	var final string
	maxSteps, deadline := a.Cfg.MaxSteps, time.Time{}
	if maxSteps <= 0 {
		maxSteps = 30
	}
	if a.Autopilot() {
		maxSteps, deadline = tools.AutopilotSteps, started.Add(tools.AutopilotMinutes*time.Minute)
	}
	autoReplies := 0
	lastPlain := "" // the last step's plain words, so the thinking line can say what comes next
	for step := 0; step < maxSteps; step++ {
		if !deadline.IsZero() && time.Now().After(deadline) {
			return final, fmt.Errorf("Autopilot stopped after %d minutes (its time budget); say \"continue\" to keep going", tools.AutopilotMinutes)
		}
		if step > 0 {
			a.readNotes() // what the user said meanwhile steers this step
		}
		all := a.toolList()
		msgs := append([]llm.Message{{Role: "system", Content: a.system()}}, a.historyCopy()...)
		a.withImages(msgs)
		md := &ui.MD{}
		if !a.Quiet {
			a.spin.Start(steps.Thinking(step, lastPlain))
			opened := false
			md.Pre = func() {
				a.spin.Stop()
				if !opened {
					ui.Println("")
					opened = true
				}
			}
			md.Post = func() { a.spin.Start(steps.Writing) }
		}
		r, err := complete(ctx, a.Cfg, msgs, all, func(s string) {
			if a.Quiet {
				return
			}
			a.spin.Set(steps.Writing)
			md.Write(s)
		})
		if !a.Quiet {
			md.Flush()
		}
		a.spin.Stop()
		if err != nil {
			// Keep history valid: drop the unanswered user turn on hard failure of the first step.
			if step == 0 {
				a.SetHistory(a.History[:len(a.History)-1])
			}
			return "", err
		}
		a.push(llm.Message{Role: "assistant", Content: r.Text, ToolCalls: r.ToolCalls})
		if len(r.ToolCalls) == 0 {
			final = r.Text
			// Autopilot: the user is away, so a question gets the automatic answer.
			if a.Autopilot() && !a.Env.Plan && autoReplies < 3 && tools.AsksUser(r.Text) {
				autoReplies++
				if !a.Quiet {
					ui.StepDone(true, "Autopilot answered", "the user is away: decide and continue", 0)
				}
				a.push(llm.Message{Role: "user", Content: "[Autopilot] " + tools.AutoReply})
				continue
			}
			// The user said something the turn hasn't read: one more step for it.
			if a.readNotes() {
				continue
			}
			if !a.Quiet {
				ui.WorkedTurn(r.Route, act, time.Since(started))
			}
			a.Reflect(ctx) // end-of-turn learning check (learn.go)
			return final, nil
		}
		for _, tc := range r.ToolCalls {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			// While working only plain words show (Blaze Desktop's phrases); the exact
			// call, its output and any diff wait behind Ctrl+O.
			plain := steps.Describe(tc.Function.Name, tc.Function.Arguments)
			lastPlain = plain
			a.setNow(plain)
			if !a.Quiet {
				ui.SetTurnStep(len(act.Steps) + 1)
				a.spin.Start(plain)
			}
			t0 := time.Now()
			a.diffed = false
			st := ui.ActStep{Plain: plain, Detail: steps.Detail(tc.Function.Name, tc.Function.Arguments)}
			a.cur = &st
			res := a.exec(ctx, tc.Function.Name, tc.Function.Arguments)
			a.cur = nil
			a.spin.Stop()
			st.Result, st.OK, st.Took = res, succeeded(res), time.Since(t0)
			act.Steps = append(act.Steps, st)
			if !a.Quiet && a.ShowSteps && tc.Function.Name != "todo_write" {
				ui.StepDone(st.OK, plain, "", st.Took)
			}
			a.printLearned()
			a.push(llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: res})
		}
		// Handing work off ends the turn at once: the user gets a short line and the
		// prompt is theirs again while the agents work (Blaze Desktop's rule).
		if a.Handoff != nil && a.Handoff(r.ToolCalls) {
			final = r.Text
			if strings.TrimSpace(final) == "" {
				final = HandoffLine
				a.push(llm.Message{Role: "assistant", Content: final})
				if !a.Quiet {
					ui.Println("\n  " + ui.Body(final))
				}
			}
			if !a.Quiet {
				ui.WorkedTurn(r.Route, act, time.Since(started))
			}
			return final, nil
		}
	}
	return final, fmt.Errorf("stopped after %d steps without finishing; say \"continue\" to keep going", maxSteps)
}

// toolList is every tool the model may call now: Plan mode keeps only the read +
// scout tools (and host tools that only read).
func (a *Agent) toolList() []llm.Tool {
	all := tools.All()
	if a.Extra != nil {
		all = append(all, a.Extra()...)
	}
	if !a.Env.Plan {
		return all
	}
	out := make([]llm.Tool, 0, len(all))
	for _, t := range all {
		if a.planAllows(t.Function.Name) {
			out = append(out, t)
		}
	}
	return out
}

func (a *Agent) planAllows(name string) bool {
	return tools.PlanAllows(name) || (a.ReadOnly != nil && a.ReadOnly(name))
}

// PlanRefusal is the tool result for a call Plan mode doesn't allow.
const PlanRefusal = "Plan mode is on, so nothing can be changed yet. Keep investigating with file_read, file_list, file_search, web_search, then finish with a numbered plan. The user switches to Build to let you act."

// exec runs one tool call: the Plan-mode check, then the host's tools, then the built-ins.
func (a *Agent) exec(ctx context.Context, name, args string) string {
	if a.Env.Plan {
		if !a.planAllows(name) {
			return PlanRefusal
		}
		if name == "git" {
			var g struct {
				Args string `json:"args"`
			}
			_ = json.Unmarshal([]byte(args), &g)
			if why := tools.PlanGit(g.Args); why != "" {
				return "Plan mode is on: " + why + ", so it can't run yet. Use read-only git (status, diff, log, show) and finish with a plan."
			}
		}
	}
	if a.Checkpoints != nil {
		if paths := a.changePaths(name, args); len(paths) > 0 {
			a.Checkpoints.Before(paths)
			defer a.Checkpoints.After(paths)
		}
	}
	if a.Handle != nil {
		if res, ok := a.Handle(ctx, name, args); ok {
			if strings.TrimSpace(res) == "" {
				res = name + " finished with no output."
			}
			return res
		}
	}
	return tools.Run(a.Env, name, args)
}

// gate is the approval for risky actions. Auto asks; Autopilot approves everything
// except the catastrophic blocklist (wiping disks, the system or the home folder).
func (a *Agent) gate(title, detail string) bool {
	if st := a.cur; st != nil && !a.Quiet {
		defer a.spin.Start(st.Plain) // the step goes on: bring the working line (and the composer) back
	}
	if a.Autopilot() {
		if why := tools.Catastrophic(title, detail); why != "" {
			if !a.Quiet {
				a.spin.Stop()
				ui.StepDone(false, "Autopilot blocked: "+strings.TrimSuffix(title, "?"), why, 0)
			}
			return false
		}
		if !a.Quiet {
			a.spin.Stop()
			ui.StepDone(true, "Autopilot approved: "+strings.TrimSuffix(title, "?"), "", 0) // the command itself waits behind Ctrl+O
		}
		return true
	}
	a.spin.Stop()
	return ui.Confirm(title, detail)
}

// Describe is the one-line wording of a tool call ("$ npm test", "read README.md").
func Describe(tc llm.ToolCall) string { return describe(tc) }

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
	a.SetHistory(a.History[cut:])
	a.Dropped += cut
}

func (a *Agent) Reset() { a.SetHistory(nil); a.Dropped = 0; tools.ResetTodos() }

// push appends to the history. Only the agent's own goroutine writes it; the lock
// lets background agents read it (SystemPrompt) while a turn runs.
func (a *Agent) push(m ...llm.Message) {
	a.hmu.Lock()
	a.History = append(a.History, m...)
	a.hmu.Unlock()
}

// SetHistory replaces the history (resume, compact, trim, rewind).
func (a *Agent) SetHistory(h []llm.Message) {
	a.hmu.Lock()
	a.History = h
	a.hmu.Unlock()
}

// Push appends messages to the history (the host adds shell runs this way).
func (a *Agent) Push(m ...llm.Message) { a.push(m...) }

// historyCopy is a copy of the history that is safe to read from any goroutine.
func (a *Agent) historyCopy() []llm.Message {
	a.hmu.RLock()
	defer a.hmu.RUnlock()
	return append([]llm.Message(nil), a.History...)
}

func (a *Agent) Close() { a.Env.Jobs.StopAll() }
