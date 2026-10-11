package agents

// Specialists (v0.6.0): the main agent works like a lead. It splits a big task into
// independent pieces and briefs a specialist for each; they run in parallel (sharing
// the background agents' concurrency cap), each in its own worktree or copy when the
// work changes code. Their results come back to the lead automatically; the lead
// reviews each one's changes and applies or discards them before it tells the user.
//
// A specialist is an Agent with Kind == Specialist, so it reuses everything background
// agents have: the queue, Stop / Resume, steering, approvals on its own row, the
// result wake-up and persistence. What it adds: a mode it can't raise above the lead's,
// the lead's step budget, its isolated copy and a review step.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

// Specialist is Agent.Kind for a lead-briefed specialist.
const Specialist = "specialist"

// SpecToolNames are the lead's specialist tools.
var SpecToolNames = []string{"dispatch_specialist", "check_specialist", "message_specialist", "cancel_specialist", "apply_specialist"}

// IsSpecTool reports whether name is a specialist tool.
func IsSpecTool(name string) bool {
	for _, n := range SpecToolNames {
		if n == name {
			return true
		}
	}
	return false
}

// SetIsolationDir sets where specialists' worktrees and copies are made ("" = off).
func (m *Manager) SetIsolationDir(dir string) { m.mu.Lock(); m.isoDir = dir; m.mu.Unlock() }

// Lead is what a specialist inherits from the lead at dispatch.
type Lead struct {
	Conv     string // the chat
	Folder   string // the lead's folder (default workdir)
	Mode     string // plan | build: a specialist never gets more than this
	MaxSteps int    // the lead's step budget (Autopilot's when it is on)
}

// SpecTools are the lead's tools for specialists.
func SpecTools() []llm.Tool {
	mode := map[string]any{"type": "string", "enum": []string{"build", "plan"}, "description": "build (default): may change files, in its own isolated copy. plan: read and scout only, returns a plan. Never more than you have: in Plan mode every specialist is plan."}
	return []llm.Tool{
		tool("dispatch_specialist", "Brief a specialist for one independent piece of a bigger task. It starts at once and runs in parallel with other specialists (a few at a time; extras queue). For code it works in its own git worktree (or a copy of the folder), so specialists never collide and nothing reaches the user's folder until you review it and call apply_specialist. Its result comes back to you automatically. Dispatch all the independent pieces in one reply.",
			map[string]any{
				"name":    str("Short lowercase name for this specialist, e.g. api-tests or docs-update."),
				"brief":   str("A self-contained brief: the goal, the files and constraints, what done looks like, how to verify it and what to report. The specialist can't see this chat or the other specialists."),
				"workdir": str("Folder it works on (absolute, or relative to the current folder). Default: the current folder."),
				"mode":    mode,
			}, "name", "brief"),
		tool("check_specialist", "Status of a specialist (or all of this chat's specialists when name is empty): state, steps, its last action, its short plan, and once finished the files it changed. diff: true adds the diff of its changes for review.",
			map[string]any{"name": str("The specialist's name; empty for all."), "diff": map[string]any{"type": "boolean", "description": "Include the diff of its changes (finished specialists)."}}),
		tool("message_specialist", "Steer a running specialist: the instruction reaches it at its next step.",
			map[string]any{"name": str("The specialist's name."), "message": str("The instruction, self-contained.")}, "name", "message"),
		tool("cancel_specialist", "Stop a specialist (running or queued). Its copy stays until you apply or discard it.",
			map[string]any{"name": str("The specialist's name.")}, "name"),
		tool("apply_specialist", "After reviewing a finished specialist's changes (check_specialist with diff: true): apply brings them into the original folder as uncommitted changes; discard throws its copy away.",
			map[string]any{"name": str("The specialist's name."), "action": map[string]any{"type": "string", "enum": []string{"apply", "discard"}}}, "name", "action"),
	}
}

// HandleLeadTool runs any agent-control tool: the background-agent ones and the
// specialist ones.
func (m *Manager) HandleLeadTool(name, rawArgs string, lead Lead) ToolCall {
	if !IsSpecTool(name) {
		return m.HandleMainTool(name, rawArgs, lead.Conv, lead.Folder)
	}
	var a struct {
		Name    string `json:"name"`
		Handle  string `json:"handle"`
		Brief   string `json:"brief"`
		Task    string `json:"task"`
		Workdir string `json:"workdir"`
		Mode    string `json:"mode"`
		Message string `json:"message"`
		Action  string `json:"action"`
		Diff    any    `json:"diff"`
	}
	if strings.TrimSpace(rawArgs) != "" {
		if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return ToolCall{Result: "Error in " + name + ": the arguments weren't valid JSON. Call it again with a JSON object."}
		}
	}
	if a.Name == "" {
		a.Name = a.Handle
	}
	if a.Brief == "" {
		a.Brief = a.Task
	}
	switch name {
	case "dispatch_specialist":
		return m.dispatchSpecialist(a.Name, a.Brief, a.Workdir, a.Mode, lead)
	case "check_specialist":
		d := a.Diff == true || a.Diff == "true"
		return ToolCall{Result: m.CheckSpecialist(a.Name, lead.Conv, d)}
	case "message_specialist":
		if err := m.Message(a.Name, a.Message); err != nil {
			return ToolCall{Result: "Error in message_specialist: " + err.Error()}
		}
		return ToolCall{Result: fmt.Sprintf("Delivered to %q; it reads it at its next step.", CleanHandle(a.Name))}
	case "cancel_specialist":
		if err := m.Cancel(a.Name); err != nil {
			return ToolCall{Result: "Error in cancel_specialist: " + err.Error()}
		}
		return ToolCall{Result: fmt.Sprintf("Stopped %q. Its copy is kept: apply_specialist applies or discards what it did.", CleanHandle(a.Name))}
	case "apply_specialist":
		msg, err := m.ApplySpecialist(a.Name, a.Action)
		if err != nil {
			return ToolCall{Result: "Error in apply_specialist: " + err.Error()}
		}
		return ToolCall{Result: msg}
	}
	return ToolCall{Result: "Error: there is no tool called " + name}
}

func (m *Manager) dispatchSpecialist(name, brief, workdir, mode string, lead Lead) ToolCall {
	if strings.TrimSpace(name) == "" {
		f := strings.Fields(brief)
		if len(f) > 3 {
			f = f[:3]
		}
		name = strings.Join(f, "-")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "plan" {
		mode = "build"
	}
	capped := false
	if lead.Mode == "plan" && mode != "plan" {
		mode, capped = "plan", true
	}
	wd := strings.TrimSpace(workdir)
	if wd == "" {
		wd = lead.Folder
	} else if !filepath.IsAbs(wd) && lead.Folder != "" {
		wd = filepath.Join(lead.Folder, wd)
	}
	if wd != "" {
		wd = filepath.Clean(wd)
		if st, err := os.Stat(wd); err != nil || !st.IsDir() {
			return ToolCall{Result: fmt.Sprintf("Error in dispatch_specialist: the workdir %q isn't a folder.", wd)}
		}
	}
	spec := &Agent{Handle: name, Task: brief, Conv: lead.Conv, Folder: wd, Kind: Specialist, Mode: mode, Workdir: wd, MaxSteps: lead.MaxSteps}
	h, st, err := m.DispatchAgent(spec)
	if err != nil {
		return ToolCall{Result: "Error in dispatch_specialist: " + err.Error()}
	}
	m.mu.Lock()
	iso := m.isoDir != "" && mode == "build" && wd != ""
	m.mu.Unlock()
	where := "It reads " + orNone(wd) + " (Plan: read and scout only)."
	if iso {
		where = "It works in its own isolated copy of " + wd + " (a git worktree when it's a repo); nothing reaches that folder until you apply_specialist."
	} else if mode == "build" {
		where = "It works in " + orNone(wd) + "."
	}
	if capped {
		where += " You are in Plan mode, so it is a Plan specialist too."
	}
	msg := fmt.Sprintf("%s: specialist %q (%s). %s Its result comes back to you automatically; dispatch the other independent pieces now, then end your turn with one short line to the user.", st, h, mode, where)
	if st == "queued" {
		msg = fmt.Sprintf("queued: specialist %q (%s) starts when a slot frees up (%d run at once). %s Its result comes back to you automatically.", h, mode, m.Cap(), where)
	}
	return ToolCall{Result: msg, Dispatched: h}
}

func orNone(s string) string {
	if s == "" {
		return "the current folder"
	}
	return s
}

// prepare makes a Build specialist's isolated copy before its first step (and checks a
// resumed one still has it).
func (m *Manager) prepare(a *Agent) error {
	m.mu.Lock()
	need := a.Kind == Specialist && a.Mode == "build" && a.Workdir != "" && m.isoDir != ""
	iso, root := a.Iso, m.isoDir
	m.mu.Unlock()
	if !need {
		return nil
	}
	if iso != nil {
		if _, err := os.Stat(iso.Workdir()); err != nil {
			return fmt.Errorf("its isolated copy at %s is gone", iso.Path)
		}
		return nil
	}
	m.Status(a.Handle, "Making its own copy of the folder")
	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	dest := filepath.Join(root, a.Handle+"-"+hex.EncodeToString(rnd[:]))
	iso, err := Isolate(a.Workdir, dest, "blaze/"+a.Handle)
	if err != nil {
		return fmt.Errorf("couldn't make an isolated copy of %s: %v", a.Workdir, err)
	}
	m.mu.Lock()
	a.Iso = iso
	a.Folder = iso.Workdir()
	m.mu.Unlock()
	m.save(a)
	m.Status(a.Handle, "Starting")
	return nil
}

// CheckSpecialist is check_specialist's text.
func (m *Manager) CheckSpecialist(name, conv string, diff bool) string {
	if strings.TrimSpace(name) == "" {
		var b strings.Builder
		for _, v := range m.List(conv) {
			if v.Kind == Specialist {
				b.WriteString(m.CheckSpecialist(v.Handle, conv, false) + "\n")
			}
		}
		if b.Len() == 0 {
			return "No specialists in this chat."
		}
		return strings.TrimRight(b.String(), "\n")
	}
	m.mu.Lock()
	a := m.agents[CleanHandle(name)]
	if a == nil || a.Kind != Specialist {
		m.mu.Unlock()
		return fmt.Sprintf("Error in check_specialist: no specialist called %q", name)
	}
	v := m.view(a)
	last := a.Now
	for i := len(a.Steps) - 1; last == "" && i >= 0; i-- {
		last = a.Steps[i].Plain
		if last == "" {
			last = a.Steps[i].Name
		}
	}
	iso := a.Iso
	m.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "- %s · %s · %s · %d step(s) · %.0fs", v.Handle, v.Mode, v.Status, v.N, v.Seconds)
	if v.Queue > 0 {
		fmt.Fprintf(&b, " · #%d in queue", v.Queue)
	}
	fmt.Fprintf(&b, "\n  brief: %s", clip(v.Task, 200))
	if v.Plan != "" {
		fmt.Fprintf(&b, "\n  plan: %s", clip(v.Plan, 280))
	}
	if last != "" {
		fmt.Fprintf(&b, "\n  last action: %s", clip(last, 160))
	}
	if iso != nil {
		fmt.Fprintf(&b, "\n  works in: %s (%s", iso.Workdir(), iso.Kind)
		if iso.Branch != "" {
			fmt.Fprintf(&b, ", branch %s", iso.Branch)
		}
		b.WriteString(")")
		if iso.Dirty {
			b.WriteString("\n  note: the original had uncommitted changes when it started; its worktree began from the last commit")
		}
	}
	if v.Error != "" {
		fmt.Fprintf(&b, "\n  error: %s", clip(v.Error, 200))
	}
	if !Active(v.Status) && iso != nil {
		if v.Review != "" {
			fmt.Fprintf(&b, "\n  review: %s", v.Review)
		}
		if len(v.Changes) > 0 {
			fmt.Fprintf(&b, "\n  changed %d file(s): %s", len(v.Changes), changeList(v.Changes, 12))
		}
		if diff && v.Review != "applied" && v.Review != "discarded" {
			if _, patch, err := iso.Changes(12000); err == nil && strings.TrimSpace(patch) != "" {
				b.WriteString("\n<diff>\n" + patch + "\n</diff>")
			} else if err != nil {
				fmt.Fprintf(&b, "\n  diff: %v", err)
			}
		}
	}
	return b.String()
}

func changeList(cs []Change, n int) string {
	var parts []string
	for i, c := range cs {
		if i == n {
			parts = append(parts, fmt.Sprintf("+%d more", len(cs)-n))
			break
		}
		parts = append(parts, c.Op[:1]+" "+c.Path)
	}
	return strings.Join(parts, ", ")
}

// ApplySpecialist applies or discards a finished specialist's changes.
func (m *Manager) ApplySpecialist(name, action string) (string, error) {
	m.mu.Lock()
	a := m.agents[CleanHandle(name)]
	if a == nil || a.Kind != Specialist {
		m.mu.Unlock()
		return "", fmt.Errorf("no specialist called %q", name)
	}
	if Active(a.Status) {
		m.mu.Unlock()
		return "", fmt.Errorf("%q is still %s; wait for its result (or cancel it) first", a.Handle, a.Status)
	}
	iso, task, review := a.Iso, a.Task, a.Review
	m.mu.Unlock()
	if iso == nil {
		return "", fmt.Errorf("%q worked without an isolated copy (Plan mode or no folder), so there is nothing to apply", a.Handle)
	}
	if review == "applied" || review == "discarded" {
		return "", fmt.Errorf("%q was already %s", a.Handle, review)
	}
	var msg string
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "apply":
		out, err := iso.Apply("Blaze specialist " + a.Handle + ": " + clip(task, 60))
		if err == ErrNothing {
			_ = iso.Remove(true)
			m.setReview(a, "none")
			return fmt.Sprintf("%q changed nothing; its copy is removed.", a.Handle), nil
		}
		if err != nil {
			return "", err
		}
		_ = iso.Remove(true)
		m.setReview(a, "applied")
		msg = out
	case "discard":
		_ = iso.Remove(true)
		m.setReview(a, "discarded")
		msg = fmt.Sprintf("Discarded %q's changes and removed its copy.", a.Handle)
	default:
		return "", fmt.Errorf("action must be apply or discard")
	}
	return msg, nil
}

func (m *Manager) setReview(a *Agent, r string) {
	m.mu.Lock()
	a.Review = r
	v := m.view(a)
	m.mu.Unlock()
	m.save(a)
	m.emit(v)
}

// shortPlan is a specialist's plan line: its first reply, the "Plan:" part when given.
func shortPlan(text string) string {
	t := strings.TrimSpace(text)
	if i := strings.Index(strings.ToLower(t), "plan:"); i >= 0 {
		t = strings.TrimSpace(t[i+5:])
	}
	return clip(t, 280)
}

// Orchestration teaches the lead when to use specialists.
const Orchestration = `
SPECIALISTS (you are the lead)
For a big task with independent parts, split it and brief specialists with dispatch_specialist; they run in parallel and report back to you.
- Delegate when the pieces are independent and each is real work: separate modules or files, tests for different packages, research on different questions, a frontend and a backend change that don't touch the same files.
- Do it yourself when the task is small, sequential (each step needs the last one's result), or touches the same few files: splitting it costs more than it saves.
- 2-4 specialists is usually right. Never give two specialists the same file. Each brief stands alone: goal, files, constraints, how to verify, what to report.
- Dispatch all independent pieces in one reply, tell the user in one short line, and end your turn. Don't poll; results come back to you as platform messages.
- Build specialists work in their own git worktree (or a copy of the folder). When one reports back, review it: check_specialist with diff: true, then apply_specialist apply (or discard, and redo or re-brief). Only apply what you have checked.
- If other specialists are still running when a result arrives, review that one, then end your turn with at most one short progress line. When the last one is in, give the user one summary of the whole task.
- Specialists get your mode and limits: in Plan mode they only scout and plan; approvals and the spend cap still apply.
`

// SpecSystem is added to a specialist's system prompt.
func SpecSystem(a *Agent) string {
	var where string
	switch {
	case a.Iso != nil:
		where = fmt.Sprintf("Work ONLY in %s: it is your own isolated copy (%s) of %s. Don't touch the original folder; the lead reviews your changes and applies them there. Don't push or merge.", a.Iso.Workdir(), a.Iso.Kind, a.Iso.OriginalWorkdir())
		if a.Iso.Branch != "" {
			where += " You're on branch " + a.Iso.Branch + "; committing is optional."
		}
	case a.Workdir != "":
		where = "Your folder: " + a.Workdir + "."
	}
	mode := "BUILD: you may change files (in your folder only) and run commands."
	if a.Mode == "plan" {
		mode = "PLAN: read and scout only. Writing files, running commands or installing anything is refused. Finish with a concrete plan: the exact changes and commands."
	}
	return fmt.Sprintf(`
YOU ARE A SPECIALIST (name: %s)
The lead agent split a bigger task and briefed you for one piece of it (the first message). Only your final reply goes back, to the lead, which reviews it.
- %s
- Mode %s
- Start your first reply with "Plan:" and up to three short steps, then work it through to the end, checking your work (build, tests) as you go.
- Stay inside your brief; other specialists handle the other pieces. Approvals still apply.
- A message starting "[Update from the main agent]" is the lead steering you; follow it.
- Use ask_user only when you truly can't continue.
- Finish with a short report: what you changed (files), how you verified it, and anything the lead must decide.
`, a.Handle, where, mode)
}

// AgentSystem is the right add-on prompt for an agent.
func AgentSystem(a *Agent) string {
	if a.Kind == Specialist {
		return SpecSystem(a)
	}
	return SubSystem(a.Handle)
}

// SpecialistFiles lists the files in the original folder that applying a finished
// specialist's changes would write or delete (absolute paths), so the lead's turn
// can checkpoint them first (v0.8.0). The specialist's own copy is never tracked.
func (m *Manager) SpecialistFiles(name string) []string {
	m.mu.Lock()
	a := m.agents[CleanHandle(name)]
	var iso *Isolation
	if a != nil && a.Kind == Specialist && !Active(a.Status) {
		iso = a.Iso
	}
	m.mu.Unlock()
	if iso == nil {
		return nil
	}
	cs, _, err := iso.Changes(0)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, filepath.Join(iso.Source, filepath.FromSlash(c.Path)))
	}
	return out
}
