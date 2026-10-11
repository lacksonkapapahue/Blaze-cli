package main

// Background agents in the terminal (parity with Blaze Desktop v0.5.0). The engine
// is internal/agents; this file wires it to the CLI: the model router, the tools and
// the approval gate, and the prompt. A sub-agent never draws over the prompt: its
// approvals and questions wait in a queue and the prompt is woken (ui.Wake) so the
// main loop asks them between turns; a finished agent's result wakes the main agent
// the same way, one result per turn.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/agents"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/notify"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

func agentsDir() string { return filepath.Join(config.Dir(), "cli", "agents") }

// worktreesDir is where specialists get their own git worktree or folder copy.
func worktreesDir() string { return filepath.Join(config.Dir(), "cli", "worktrees") }

// agentPlan is true when an agent may only read and scout: a Plan specialist, or
// any agent while the main agent (the lead) is in Plan mode.
func (b *bgAgents) agentPlan(ag *agents.Agent) bool { return ag.Mode == "plan" || b.main.Env.Plan }

// writesFiles are the file tools a specialist could aim at the original folder.
var writesFiles = map[string]bool{"file_write": true, "file_edit": true, "file_delete": true}

func insideDir(dir, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// isoBlock refuses a Build specialist's write into the original folder (it works in
// its own copy; the lead applies the changes after review).
func isoBlock(ag *agents.Agent, name, rawArgs string) string {
	if ag.Iso == nil || !writesFiles[name] {
		return ""
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(rawArgs), &args)
	p, _ := args["path"].(string)
	if p != "" && filepath.IsAbs(p) && insideDir(ag.Iso.OriginalWorkdir(), p) && !insideDir(ag.Iso.Path, p) {
		return "Refused: " + p + " is in the original folder. Work only in your own copy, " + ag.Iso.Workdir() + " (the same relative path there); the lead applies your changes after review."
	}
	return ""
}

// resumeMarker is written before /update restarts Blaze while agents run.
func resumeMarker() string { return filepath.Join(config.Dir(), "cli", "resume-after-update") }

type agentKey struct{}

// ask is a sub-agent's approval or question, waiting for the main loop.
type ask struct {
	handle   string
	title    string
	detail   string
	question bool
	choices  []string
	always   bool        // connector approvals offer "Always allow"
	reply    chan string // "yes" / "always" / "no", or the answer
}

type bgAgents struct {
	main *agent.Agent
	m    *agents.Manager
	conv string

	mu      sync.Mutex
	envs    map[string]*tools.Env
	ctxs    map[string]context.Context
	results []agents.View
	asks    []*ask
}

func newBG(a *agent.Agent) *bgAgents {
	b := &bgAgents{main: a, conv: "cli:" + a.Env.Root, envs: map[string]*tools.Env{}, ctxs: map[string]context.Context{}}
	steps := a.Cfg.MaxSteps
	if steps < 40 {
		steps = 40
	}
	n := agents.DefaultCap
	if a.Env.Info != nil && a.Env.Info.Cores > 0 && a.Env.Info.Cores <= 2 {
		n = 1
	}
	b.m = agents.New(agents.Runtime{
		Complete: func(ctx context.Context, msgs []llm.Message, ts []llm.Tool) (*llm.Result, error) {
			return llm.Complete(ctx, a.Cfg, msgs, ts, nil)
		},
		Tools: b.tools,
		Known: func() []string {
			var names []string
			for _, t := range b.tools(nil) {
				names = append(names, t.Function.Name)
			}
			return names
		},
		Exec:     b.exec,
		System:   func(ag *agents.Agent) string { return a.SystemPrompt() + agents.AgentSystem(ag) },
		Describe: agent.PlainStep,
		MaxSteps: steps,
	}, agentsDir(), n)
	b.m.SetIsolationDir(worktreesDir())
	b.m.OnChange = func(agents.View) { ui.Refresh() }
	b.m.OnResult = func(v agents.View) {
		b.mu.Lock()
		delete(b.envs, v.Handle)
		delete(b.ctxs, v.Handle)
		b.results = append(b.results, v)
		b.mu.Unlock()
		mark, word := ui.Accent("■ "), "finished"
		if v.Status != agents.Done {
			mark, word = ui.Ink("■ "), v.Status
		}
		ui.Above("  " + mark + ui.Ink("agent "+v.Handle+" "+word) + ui.Weak(fmt.Sprintf(" · %d steps · %.0fs", v.N, v.Seconds)))
		if !ui.Focused() {
			body := clipText(v.Result, 200)
			if v.Status != agents.Done {
				body = clipText(v.Error, 200)
			}
			_, _ = notify.Send(notify.Note{Title: "Blaze · " + v.Handle + " " + word, Body: body, Kind: "task"})
		}
		ui.Wake()
	}
	b.m.Load()
	b.m.Prune(7 * 24 * time.Hour)
	ui.StatusLine = b.statusLine
	return b
}

// tools is a sub-agent's tool list: everything the main agent has except the
// agent-control tools and update_blaze, plus ask_user, narrowed to the scope.
func (b *bgAgents) tools(ag *agents.Agent) []llm.Tool {
	var scope []string
	if ag != nil {
		scope = ag.Scope
	}
	all := tools.All()
	all = append(all, notifyTool())
	all = append(all, getConns().Tools()...)
	if b.main.Env.Plan || (ag != nil && ag.Mode == "plan") {
		var ro []llm.Tool
		for _, t := range all {
			if tools.PlanAllows(t.Function.Name) || getConns().ReadOnly(t.Function.Name) {
				ro = append(ro, t)
			}
		}
		all = ro
	}
	if len(scope) > 0 {
		want := map[string]bool{}
		for _, s := range scope {
			want[strings.TrimSpace(s)] = true
		}
		var out []llm.Tool
		for _, t := range all {
			if want[t.Function.Name] {
				out = append(out, t)
			}
		}
		if len(out) > 0 {
			all = out
		}
	}
	return append(all, agents.AskTool())
}

func (b *bgAgents) env(ag *agents.Agent) *tools.Env {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := b.envs[ag.Handle]; e != nil {
		e.Plan = b.agentPlan(ag)
		return e
	}
	root := ag.Folder
	if st, err := os.Stat(root); root == "" || err != nil || !st.IsDir() {
		root = b.main.Env.Root
	}
	h := ag.Handle
	mainEnv := b.main.Env
	e := &tools.Env{Info: mainEnv.Info, Root: root, Plan: b.agentPlan(ag), Shell: tools.NewShell(mainEnv.Info.Shell, mainEnv.Info.ShellPath, root), Jobs: mainEnv.Jobs}
	e.Confirm = func(title, detail string) bool { return b.gate(h, title, detail) }
	e.Status = func(s string) { b.m.Status(h, s) }
	b.envs[h] = e
	return e
}

func (b *bgAgents) exec(ctx context.Context, ag *agents.Agent, name, args string) string {
	b.mu.Lock()
	b.ctxs[ag.Handle] = ctx
	b.mu.Unlock()
	if name == "ask_user" {
		return b.question(ctx, ag.Handle, args)
	}
	if agents.IsMainTool(name) || name == "update_blaze" {
		return "Error: background agents and specialists can't start or steer other agents or update Blaze. Do the work yourself."
	}
	if b.agentPlan(ag) && !(tools.PlanAllows(name) || getConns().ReadOnly(name)) {
		return agent.PlanRefusal
	}
	if why := isoBlock(ag, name, args); why != "" {
		return why
	}
	if name == "notify" {
		return runNotify(args)
	}
	if getConns().Handles(name) {
		return getConns().Run(context.WithValue(ctx, agentKey{}, ag.Handle), name, args)
	}
	return tools.Run(b.env(ag), name, args)
}

func (b *bgAgents) ctxOf(handle string) context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c := b.ctxs[handle]; c != nil {
		return c
	}
	return context.Background()
}

// gate is a sub-agent's approval: Autopilot approves (catastrophic actions stay
// blocked); Auto queues the question for the main loop and waits.
func (b *bgAgents) gate(handle, title, detail string) bool {
	if b.main.Autopilot() {
		if why := tools.Catastrophic(title, detail); why != "" {
			b.m.Status(handle, "Blocked: "+why)
			return false
		}
		return true
	}
	r := b.wait(b.ctxOf(handle), &ask{handle: handle, title: title, detail: detail})
	return r == "yes" || r == "always"
}

// confirmConnector routes a connector approval: a sub-agent's waits for the main
// loop; the main agent's is asked right away.
func (b *bgAgents) confirmConnector(ctx context.Context, title, detail string, always bool) (bool, bool) {
	if h, ok := ctx.Value(agentKey{}).(string); ok && h != "" {
		r := b.wait(ctx, &ask{handle: h, title: title, detail: detail, always: always})
		return r == "yes" || r == "always", r == "always"
	}
	b.main.StopSpinner()
	ui.Box(ui.AccentLabel(title), []string{ui.Ink(detail)})
	opts := []string{"Yes", "No"}
	if always {
		opts = []string{"Yes", "Always allow", "No"}
	}
	i := ui.Choose("  "+ui.Accent("?")+" ", opts, len(opts)-1, len(opts)-1)
	switch {
	case i == 0:
		return true, false
	case always && i == 1:
		return true, true
	}
	return false, false
}

func (b *bgAgents) question(ctx context.Context, handle, raw string) string {
	var q struct {
		Question string   `json:"question"`
		Choices  []string `json:"choices"`
	}
	_ = json.Unmarshal([]byte(raw), &q)
	if strings.TrimSpace(q.Question) == "" {
		return "Error in ask_user: write the question."
	}
	if b.main.Autopilot() {
		return tools.AutoReply
	}
	r := b.wait(ctx, &ask{handle: handle, title: q.Question, question: true, choices: q.Choices})
	if r == "" {
		return "The user didn't answer. Decide yourself pragmatically, note the assumption and continue."
	}
	return "The user answered: " + r
}

// wait queues an ask, wakes the prompt and blocks this agent until it's answered.
func (b *bgAgents) wait(ctx context.Context, q *ask) string {
	q.reply = make(chan string, 1)
	b.mu.Lock()
	b.asks = append(b.asks, q)
	b.mu.Unlock()
	b.m.SetWaiting(q.handle, true, "Waiting for you")
	defer b.m.SetWaiting(q.handle, false, "Working on it")
	what := "needs your approval"
	if q.question {
		what = "has a question"
	}
	ui.Above("  " + ui.Accent("? ") + ui.Ink("agent "+q.handle+" "+what))
	if !ui.Focused() {
		_, _ = notify.Send(notify.Note{Title: "Blaze · " + q.handle + " " + what, Body: q.title, Kind: "task", Urgency: "critical"})
	}
	ui.Wake()
	select {
	case r := <-q.reply:
		return r
	case <-ctx.Done():
		b.mu.Lock()
		for i, x := range b.asks {
			if x == q {
				b.asks = append(b.asks[:i], b.asks[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		return ""
	}
}

// pending reports whether anything waits for the main loop.
func (b *bgAgents) pending() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.asks) > 0 || len(b.results) > 0
}

// drainAsks asks the user every queued approval and question (main loop only).
func (b *bgAgents) drainAsks() {
	for {
		b.mu.Lock()
		if len(b.asks) == 0 {
			b.mu.Unlock()
			return
		}
		q := b.asks[0]
		b.asks = b.asks[1:]
		b.mu.Unlock()
		ui.Println("")
		if q.question {
			ui.Box(ui.AccentLabel("agent "+q.handle+" asks"), []string{ui.Ink(q.title)})
			if len(q.choices) > 0 {
				i := ui.Choose("  "+ui.Accent("?")+" ", q.choices, 0, -1)
				if i >= 0 && i < len(q.choices) {
					q.reply <- q.choices[i]
					continue
				}
			}
			ans, _ := ui.ReadLine("  " + ui.Accent("› "))
			q.reply <- strings.TrimSpace(ans)
			continue
		}
		ui.Box(ui.AccentLabel("agent "+q.handle+" · "+q.title), []string{ui.Ink(q.detail)})
		opts := []string{"Yes", "No"}
		if q.always {
			opts = []string{"Yes", "Always allow", "No"}
		}
		i := ui.Choose("  "+ui.Accent("?")+" ", opts, len(opts)-1, len(opts)-1)
		switch {
		case i == 0:
			q.reply <- "yes"
		case q.always && i == 1:
			q.reply <- "always"
		default:
			q.reply <- "no"
		}
	}
}

// nextResult pops the oldest finished agent's result.
func (b *bgAgents) nextResult() (agents.View, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.results) == 0 {
		return agents.View{}, false
	}
	v := b.results[0]
	b.results = b.results[1:]
	return v, true
}

func (b *bgAgents) statusLine() string {
	r, q := b.m.Running()
	switch {
	case r == 0 && q == 0:
		return ""
	case q == 0:
		return ui.Accent(fmt.Sprintf("%d agent%s working", r, plural(r)))
	}
	return ui.Accent(fmt.Sprintf("%d agent%s working · %d queued", r, plural(r), q))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// startupNotice resumes agents an update interrupted, or offers to.
func (b *bgAgents) startupNotice() {
	var inter []agents.View
	for _, v := range b.m.List(b.conv) {
		if v.Status == agents.Interrupted {
			inter = append(inter, v)
		}
	}
	if len(inter) == 0 {
		return
	}
	if st, err := os.Stat(resumeMarker()); err == nil {
		_ = os.Remove(resumeMarker())
		if time.Since(st.ModTime()) < time.Hour {
			n := 0
			for _, v := range inter {
				if b.m.Resume(v.Handle) == nil {
					n++
				}
			}
			ui.Println("  " + ui.Accent("■ ") + ui.Ink(fmt.Sprintf("Resumed %d background agent%s after the update.", n, plural(n))))
			return
		}
	}
	var hs []string
	for _, v := range inter {
		hs = append(hs, v.Handle)
	}
	ui.Println("  " + ui.Accent("■ ") + ui.Ink(fmt.Sprintf("%d agent%s stopped when Blaze closed: %s. ", len(inter), plural(len(inter)), strings.Join(hs, ", "))) +
		ui.Accent("/agents resume <name>") + ui.Sub(" or ") + ui.Accent("/agents discard <name>"))
}

// markResume remembers that agents were running when /update restarts Blaze.
func (b *bgAgents) markResume() {
	if r, q := b.m.Running(); r+q > 0 {
		_ = os.MkdirAll(filepath.Dir(resumeMarker()), 0o700)
		_ = os.WriteFile(resumeMarker(), []byte(time.Now().Format(time.RFC3339)), 0o600)
	}
}

// command is /agents [list | <name> | msg <name> <text> | cancel | resume | discard | new <name> <task> | cap N].
func (b *bgAgents) command(f []string) {
	if len(f) == 0 || f[0] == "list" || f[0] == "ls" {
		b.list()
		return
	}
	need := func(n int, use string) bool {
		if len(f) < n {
			ui.Step("use: /agents " + use)
			return false
		}
		return true
	}
	var err error
	switch f[0] {
	case "msg", "message", "tell":
		if !need(3, "msg <name> <instruction>") {
			return
		}
		if err = b.m.Message(f[1], strings.Join(f[2:], " ")); err == nil {
			ui.StepDone(true, "Sent to "+f[1], "it reads it at its next step", 0)
		}
	case "cancel", "stop":
		if !need(2, "cancel <name>") {
			return
		}
		if err = b.m.Cancel(f[1]); err == nil {
			ui.StepDone(true, "Stopped "+f[1], "", 0)
		}
	case "resume":
		if !need(2, "resume <name>") {
			return
		}
		if err = b.m.Resume(f[1]); err == nil {
			ui.StepDone(true, "Resumed "+f[1], "", 0)
		}
	case "discard", "rm":
		if !need(2, "discard <name>") {
			return
		}
		if err = b.m.Discard(f[1]); err == nil {
			ui.StepDone(true, "Discarded "+f[1], "", 0)
		}
	case "new", "run":
		if !need(3, "new <name> <task>") {
			return
		}
		var h, st string
		if h, st, err = b.m.Dispatch(f[1], strings.Join(f[2:], " "), nil, b.conv, b.main.Env.Shell.Cwd()); err == nil {
			ui.StepDone(true, "Agent "+h+" "+st, "its result comes back here", 0)
		}
	case "cap":
		if !need(2, "cap <1-6>") {
			return
		}
		var n int
		fmt.Sscan(f[1], &n)
		b.m.SetCap(n)
		ui.Step(fmt.Sprintf("Up to %d agents run at once (this session).", b.m.Cap()))
	default:
		b.show(f[0])
	}
	if err != nil {
		ui.Step(err.Error())
	}
}

func (b *bgAgents) list() {
	vs := b.m.List(b.conv)
	if len(vs) == 0 {
		ui.Step("No background agents here yet. Ask Blaze to do something in the background, or /agents new <name> <task>.")
		return
	}
	var rows []string
	for _, v := range vs {
		mark := ui.Weak("□ ")
		switch v.Status {
		case agents.Running, agents.Waiting:
			mark = ui.Accent("▸ ")
		case agents.Done:
			mark = ui.Accent("■ ")
		case agents.Failed, agents.Interrupted:
			mark = ui.Ink("■ ")
		}
		line := mark + ui.Ink(fmt.Sprintf("%-18s", v.Handle)) + ui.Sub(fmt.Sprintf(" %-11s %3d steps %5.0fs", v.Status, v.N, v.Seconds))
		if agents.Active(v.Status) && v.Now != "" {
			line += ui.Weak("  " + clipText(v.Now, 40))
		}
		rows = append(rows, line)
	}
	r, q := b.m.Running()
	ui.Box(ui.AccentLabel(fmt.Sprintf("agents · %d running · %d queued · cap %d", r, q, b.m.Cap())), rows)
	ui.Println("  " + ui.Weak("/agents <name> details · msg <name> <text> · cancel · resume · discard · new <name> <task>"))
}

func (b *bgAgents) show(handle string) {
	v, ok := b.m.Get(handle)
	if !ok {
		ui.Step("No agent called " + handle + ". /agents lists them.")
		return
	}
	rows := []string{ui.Sub("status: ") + ui.Ink(v.Status) + ui.Sub(fmt.Sprintf(" · %d steps · %.0fs", v.N, v.Seconds)), ui.Sub("task: ") + ui.Ink(clipText(v.Task, 200))}
	steps := v.Steps
	if len(steps) > 10 {
		steps = steps[len(steps)-10:]
	}
	for _, s := range steps {
		mark := ui.Accent("■ ")
		if s.Bad {
			mark = ui.Ink("■ ")
		} else if !s.Done {
			mark = ui.Accent("▸ ")
		}
		rows = append(rows, mark+ui.Sub(clipText(nonEmptyStr(s.Plain, s.Name), 80)))
	}
	if v.Error != "" {
		rows = append(rows, ui.Ink("error: "+clipText(v.Error, 200)))
	}
	ui.Box(ui.AccentLabel("agent "+v.Handle), rows)
	if v.Result != "" {
		ui.Println("")
		md := &ui.MD{}
		md.Write(v.Result + "\n")
		md.Flush()
	}
}

func describeCall(name, args string) string {
	var tc llm.ToolCall
	tc.Function.Name, tc.Function.Arguments = name, args
	return agent.Describe(tc)
}

func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func nonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
