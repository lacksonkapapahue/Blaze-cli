// Package agents runs background agents for the main chat, the way Hark does it:
// the main agent hands a self-contained task to a sub-agent and its turn ends at
// once, so the user keeps chatting. Each sub-agent runs in its own goroutine with
// its own history, at most Cap at a time (the rest queue). When one finishes, its
// result goes back to the main conversation through OnResult, which wakes the main
// agent (see Mainline). Agents persist to disk so a restart shows them as
// interrupted, with Resume / Discard.
//
// The package knows nothing about Wails or the UI: the host (app.go / agents.go in
// package main) supplies the model call, the tool runner and the event sinks.
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

// Status of an agent.
const (
	Queued      = "queued"
	Running     = "running"
	Waiting     = "waiting" // blocked on the user: an approval card or a question
	Done        = "done"
	Failed      = "failed"
	Cancelled   = "cancelled"
	Interrupted = "interrupted" // the app closed while it ran; Resume or Discard
)

// Active reports whether a status still has work ahead of it.
func Active(s string) bool { return s == Queued || s == Running || s == Waiting }

// DefaultCap is how many sub-agents run at once unless the user changes it.
const DefaultCap = 3

// MaxCap is the most the setting allows.
const MaxCap = 6

// Runtime is what an agent needs from the host.
type Runtime struct {
	// Complete is one model call (the router, budget and fallbacks live behind it).
	Complete func(ctx context.Context, msgs []llm.Message, tools []llm.Tool) (*llm.Result, error)
	// Tools lists the tools an agent may call, narrowed by its scope (nil = all).
	Tools func(scope []string) []llm.Tool
	// Known lists every tool name the gatekeeper should accept.
	Known func() []string
	// Exec runs one tool call for an agent. It may block on the user (approvals).
	Exec func(ctx context.Context, a *Agent, name, args string) string
	// System is the agent's system prompt.
	System func(a *Agent) string
	// Describe is the plain step wording ("Reading package.json").
	Describe func(name, args string) string
	// MaxSteps per run (default 40).
	MaxSteps int
}

// Step is one tool call in an agent's log.
type Step struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Args   string    `json:"args"`
	Plain  string    `json:"plain"`
	Result string    `json:"result,omitempty"`
	Bad    bool      `json:"bad,omitempty"`
	Done   bool      `json:"done,omitempty"`
	At     time.Time `json:"at"`
}

// Agent is one background agent. Fields are guarded by the Manager's lock; the host
// reads them through View.
type Agent struct {
	Handle  string        `json:"handle"`
	Task    string        `json:"task"`
	Scope   []string      `json:"scope,omitempty"`
	Conv    string        `json:"conv"`             // the chat that dispatched it (results go back there)
	Folder  string        `json:"folder,omitempty"` // the project folder at dispatch
	Status  string        `json:"status"`
	Steps   []Step        `json:"steps"`
	History []llm.Message `json:"history"`
	Result  string        `json:"result,omitempty"`
	Error   string        `json:"error,omitempty"`
	Now     string        `json:"now,omitempty"` // the current plain step / status line
	Created time.Time     `json:"created"`
	Started time.Time     `json:"started,omitempty"`
	Ended   time.Time     `json:"ended,omitempty"`
	Runs    int           `json:"runs"` // 1 + times resumed

	inbox  []string
	cancel context.CancelFunc
	gen    int // bumps on every start, so a stale goroutine never writes over a resumed run
}

// View is the UI's copy of an agent.
type View struct {
	Handle  string  `json:"handle"`
	Task    string  `json:"task"`
	Conv    string  `json:"conv"`
	Status  string  `json:"status"`
	Now     string  `json:"now"`
	Steps   []Step  `json:"steps"`
	N       int     `json:"n"`
	Result  string  `json:"result,omitempty"`
	Error   string  `json:"error,omitempty"`
	Started int64   `json:"started"` // unix ms (0 = not started)
	Ended   int64   `json:"ended"`
	Seconds float64 `json:"seconds"`
	Queue   int     `json:"queue,omitempty"` // place in the queue (1 = next)
}

// Manager owns every agent.
type Manager struct {
	rt  Runtime
	dir string // where agents persist ("" = memory only)

	mu      sync.Mutex
	cap     int
	agents  map[string]*Agent
	queue   []string
	running int

	// Event sinks (any may be nil). They run outside the lock.
	OnChange func(View)                  // status, current step or queue position changed
	OnStep   func(handle string, s Step) // a step started (Done=false) or finished (Done=true)
	OnResult func(View)                  // finished with a result or an error: wake the main agent
	OnRemove func(handle string)         // discarded
}

// New makes a manager. dir may be "" for tests that don't persist.
func New(rt Runtime, dir string, cap int) *Manager {
	if rt.MaxSteps <= 0 {
		rt.MaxSteps = 40
	}
	m := &Manager{rt: rt, dir: dir, agents: map[string]*Agent{}}
	m.SetCap(cap)
	return m
}

// SetCap changes how many agents may run at once. Raising it starts queued agents.
func (m *Manager) SetCap(n int) {
	if n <= 0 {
		n = DefaultCap
	}
	if n > MaxCap {
		n = MaxCap
	}
	m.mu.Lock()
	m.cap = n
	m.mu.Unlock()
	m.pump()
}

// Cap is the current limit.
func (m *Manager) Cap() int { m.mu.Lock(); defer m.mu.Unlock(); return m.cap }

var handleRe = regexp.MustCompile(`[^a-z0-9-]+`)

// CleanHandle turns a model-chosen handle into a short slug.
func CleanHandle(h string) string {
	h = handleRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(h)), "-")
	h = strings.Trim(h, "-")
	if len(h) > 32 {
		h = strings.Trim(h[:32], "-")
	}
	return h
}

// Dispatch starts (or queues) a new agent and returns at once. The returned handle
// may differ from the one asked for when a finished agent already used it.
func (m *Manager) Dispatch(handle, task string, scope []string, conv, folder string) (string, string, error) {
	handle = CleanHandle(handle)
	task = strings.TrimSpace(task)
	if handle == "" {
		handle = "agent"
	}
	if task == "" {
		return "", "", errors.New("the task brief is empty; write a self-contained brief")
	}
	m.mu.Lock()
	if a, ok := m.agents[handle]; ok {
		if Active(a.Status) {
			m.mu.Unlock()
			return "", "", fmt.Errorf("an agent called %q is already %s; steer it with message_agent instead of starting a duplicate", handle, a.Status)
		}
		base := handle
		for i := 2; ; i++ {
			handle = fmt.Sprintf("%s-%d", base, i)
			if _, taken := m.agents[handle]; !taken {
				break
			}
		}
	}
	a := &Agent{Handle: handle, Task: task, Scope: scope, Conv: conv, Folder: folder, Status: Queued, Created: time.Now(), Runs: 1}
	a.History = []llm.Message{{Role: "user", Content: task}}
	m.agents[handle] = a
	m.queue = append(m.queue, handle)
	m.mu.Unlock()
	m.save(a)
	m.pump()
	m.mu.Lock()
	st := a.Status
	m.mu.Unlock()
	if st == Queued {
		m.changedQueue()
		return handle, "queued", nil
	}
	return handle, "started", nil
}

// pump starts queued agents while there is room.
func (m *Manager) pump() {
	for {
		m.mu.Lock()
		if m.running >= m.cap || len(m.queue) == 0 {
			m.mu.Unlock()
			return
		}
		h := m.queue[0]
		m.queue = m.queue[1:]
		a := m.agents[h]
		if a == nil || a.Status != Queued {
			m.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		a.cancel = cancel
		a.Status = Running
		a.gen++
		gen := a.gen
		if a.Started.IsZero() {
			a.Started = time.Now()
		}
		a.Now = "Starting"
		m.running++
		v := m.view(a)
		m.mu.Unlock()
		m.save(a)
		m.emit(v)
		m.changedQueue()
		go m.run(ctx, a, gen)
	}
}

func (m *Manager) changedQueue() {
	m.mu.Lock()
	var vs []View
	for _, h := range m.queue {
		if a := m.agents[h]; a != nil && a.Status == Queued {
			vs = append(vs, m.view(a))
		}
	}
	m.mu.Unlock()
	for _, v := range vs {
		m.emit(v)
	}
}

func (m *Manager) emit(v View) {
	if m.OnChange != nil {
		m.OnChange(v)
	}
}

// run is one agent's loop: the same shape as the main chat loop, without the UI.
func (m *Manager) run(ctx context.Context, a *Agent, gen int) {
	status, result, errText := Failed, "", ""
	defer func() {
		if r := recover(); r != nil {
			status, errText = Failed, fmt.Sprint("internal error: ", r)
		}
		m.finish(a, gen, status, result, errText)
	}()
	for step := 0; step < m.rt.MaxSteps; step++ {
		if ctx.Err() != nil {
			status = Cancelled
			return
		}
		m.mu.Lock()
		// Steering: instructions from the main agent arrive at the next step.
		for _, in := range a.inbox {
			a.History = append(a.History, llm.Message{Role: "user", Content: "[Update from the main agent] " + in})
		}
		a.inbox = nil
		hist := trim(a.History)
		scope := a.Scope
		m.mu.Unlock()
		msgs := append([]llm.Message{{Role: "system", Content: m.rt.System(a)}}, hist...)
		r, err := m.rt.Complete(ctx, msgs, m.rt.Tools(scope))
		if err != nil {
			if ctx.Err() != nil {
				status = Cancelled
			} else {
				errText = llm.Plain(err)
			}
			return
		}
		n := llm.NormalizeCalls(r.ToolCalls, m.rt.Known())
		m.mu.Lock()
		if strings.TrimSpace(r.Text) != "" || len(n.Calls) > 0 {
			a.History = append(a.History, llm.Message{Role: "assistant", Content: r.Text, ToolCalls: n.Calls})
		}
		pending := len(a.inbox) > 0
		m.mu.Unlock()
		if len(n.Calls) == 0 {
			if pending || (len(n.Dropped) > 0 && step < m.rt.MaxSteps-1) {
				if len(n.Dropped) > 0 {
					m.mu.Lock()
					a.History = append(a.History, llm.Message{Role: "user", Content: "Blaze couldn't run " + strings.Join(n.Dropped, "; ") + ". Call the tool again with its exact name and a complete JSON object."})
					m.mu.Unlock()
				}
				continue
			}
			status, result = Done, strings.TrimSpace(r.Text)
			if result == "" {
				result = "(The agent finished without a written report.)"
			}
			return
		}
		for _, tc := range n.Calls {
			if ctx.Err() != nil {
				status = Cancelled
				return
			}
			s := Step{ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments, At: time.Now()}
			if m.rt.Describe != nil {
				s.Plain = m.rt.Describe(s.Name, s.Args)
			}
			m.mu.Lock()
			a.Steps = append(a.Steps, s)
			idx := len(a.Steps) - 1
			a.Now = s.Plain
			v := m.view(a)
			m.mu.Unlock()
			if m.OnStep != nil {
				m.OnStep(a.Handle, s)
			}
			m.emit(v)
			res := m.rt.Exec(ctx, a, s.Name, s.Args)
			s.Result, s.Done, s.Bad = res, true, isBad(res)
			m.mu.Lock()
			if idx < len(a.Steps) {
				a.Steps[idx] = s
			}
			a.History = append(a.History, llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: res})
			m.mu.Unlock()
			if m.OnStep != nil {
				m.OnStep(a.Handle, s)
			}
			m.save(a)
		}
	}
	errText = fmt.Sprintf("stopped after %d steps without finishing", m.rt.MaxSteps)
}

var badRe = regexp.MustCompile(`^(Error|exit [1-9]|Refused|Blaze couldn't)|did NOT approve`)

func isBad(r string) bool { return badRe.MatchString(r) }

func (m *Manager) finish(a *Agent, gen int, status, result, errText string) {
	m.mu.Lock()
	if a.gen != gen { // resumed or discarded meanwhile
		m.mu.Unlock()
		return
	}
	m.running--
	if a.Status == Cancelled {
		status = Cancelled // cancel_agent / Stop got there first
	}
	a.Status, a.Result, a.Error, a.Ended, a.Now = status, result, errText, time.Now(), ""
	a.cancel = nil
	v := m.view(a)
	m.mu.Unlock()
	m.save(a)
	m.emit(v)
	if (status == Done || status == Failed) && m.OnResult != nil {
		m.OnResult(v)
	}
	m.pump()
}

// Message steers an agent: the text reaches it at its next step.
func (m *Manager) Message(handle, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[CleanHandle(handle)]
	if a == nil {
		return fmt.Errorf("no agent called %q", handle)
	}
	if !Active(a.Status) {
		return fmt.Errorf("agent %q already %s; dispatch a new one for more work", a.Handle, a.Status)
	}
	a.inbox = append(a.inbox, strings.TrimSpace(text))
	return nil
}

// Cancel stops an agent (queued or running).
func (m *Manager) Cancel(handle string) error {
	m.mu.Lock()
	a := m.agents[CleanHandle(handle)]
	if a == nil {
		m.mu.Unlock()
		return fmt.Errorf("no agent called %q", handle)
	}
	if !Active(a.Status) {
		m.mu.Unlock()
		return fmt.Errorf("agent %q already %s", a.Handle, a.Status)
	}
	wasQueued := a.Status == Queued
	a.Status = Cancelled
	if a.cancel != nil {
		a.cancel()
	}
	if wasQueued {
		a.Ended = time.Now()
		for i, h := range m.queue {
			if h == a.Handle {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
	}
	v := m.view(a)
	m.mu.Unlock()
	m.save(a)
	m.emit(v)
	if wasQueued {
		m.changedQueue()
	}
	return nil
}

// SetWaiting marks an agent as blocked on the user (or back to running).
func (m *Manager) SetWaiting(handle string, waiting bool, line string) {
	m.mu.Lock()
	a := m.agents[handle]
	if a == nil || (a.Status != Running && a.Status != Waiting) {
		m.mu.Unlock()
		return
	}
	if waiting {
		a.Status = Waiting
	} else {
		a.Status = Running
	}
	if line != "" {
		a.Now = line
	}
	v := m.view(a)
	m.mu.Unlock()
	m.emit(v)
}

// Status sets an agent's current line (a tool's progress, e.g. "Running: npm test").
func (m *Manager) Status(handle, line string) {
	m.mu.Lock()
	a := m.agents[handle]
	if a == nil || !Active(a.Status) || strings.TrimSpace(line) == "" {
		m.mu.Unlock()
		return
	}
	a.Now = line
	v := m.view(a)
	m.mu.Unlock()
	m.emit(v)
}

// Resume restarts an interrupted agent from its saved history.
func (m *Manager) Resume(handle string) error {
	m.mu.Lock()
	a := m.agents[CleanHandle(handle)]
	if a == nil {
		m.mu.Unlock()
		return fmt.Errorf("no agent called %q", handle)
	}
	if a.Status != Interrupted && a.Status != Cancelled && a.Status != Failed {
		m.mu.Unlock()
		return fmt.Errorf("agent %q is %s", a.Handle, a.Status)
	}
	a.History = append(sanitizeTail(a.History), llm.Message{Role: "user", Content: "[Blaze] You were stopped before finishing (the app closed or the run was cut short). Check what's already done, then carry on with the task and finish with your report."})
	a.Status, a.Error, a.Ended = Queued, "", time.Time{}
	a.Runs++
	m.queue = append(m.queue, a.Handle)
	m.mu.Unlock()
	m.save(a)
	m.pump()
	m.changedQueue()
	return nil
}

// sanitizeTail drops a trailing assistant tool call whose results never arrived.
func sanitizeTail(h []llm.Message) []llm.Message {
	answered := map[string]bool{}
	for _, msg := range h {
		if msg.Role == "tool" {
			answered[msg.ToolCallID] = true
		}
	}
	out := append([]llm.Message(nil), h...)
	for i := len(out) - 1; i >= 0; i-- {
		msg := out[i]
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}
		var keep []llm.ToolCall
		for _, c := range msg.ToolCalls {
			if answered[c.ID] {
				keep = append(keep, c)
			}
		}
		if len(keep) != len(msg.ToolCalls) {
			out[i].ToolCalls = keep
			if len(keep) == 0 && strings.TrimSpace(msg.Content) == "" {
				out = append(out[:i], out[i+1:]...)
			}
		}
		break
	}
	return out
}

// Discard forgets an agent that isn't running.
func (m *Manager) Discard(handle string) error {
	m.mu.Lock()
	a := m.agents[CleanHandle(handle)]
	if a == nil {
		m.mu.Unlock()
		return fmt.Errorf("no agent called %q", handle)
	}
	if Active(a.Status) {
		m.mu.Unlock()
		return fmt.Errorf("agent %q is %s; cancel it first", a.Handle, a.Status)
	}
	delete(m.agents, a.Handle)
	a.gen++
	m.mu.Unlock()
	if m.dir != "" {
		_ = os.Remove(filepath.Join(m.dir, a.Handle+".json"))
	}
	if m.OnRemove != nil {
		m.OnRemove(a.Handle)
	}
	return nil
}

// Get returns one agent's view.
func (m *Manager) Get(handle string) (View, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[CleanHandle(handle)]
	if a == nil {
		return View{}, false
	}
	return m.view(a), true
}

// List returns agents, oldest first. conv "" = every chat.
func (m *Manager) List(conv string) []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []View
	for _, a := range m.agents {
		if conv == "" || a.Conv == conv {
			out = append(out, m.view(a))
		}
	}
	sort.Slice(out, func(i, j int) bool { return m.agents[out[i].Handle].Created.Before(m.agents[out[j].Handle].Created) })
	return out
}

// Running counts agents that are running, waiting or queued.
func (m *Manager) Running() (running, queued int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		switch a.Status {
		case Running, Waiting:
			running++
		case Queued:
			queued++
		}
	}
	return
}

// Summary is list_agents' text for the main agent.
func (m *Manager) Summary(conv string) string {
	vs := m.List(conv)
	if len(vs) == 0 {
		return "No background agents in this chat."
	}
	var b strings.Builder
	for _, v := range vs {
		fmt.Fprintf(&b, "- %s · %s · %d step(s)", v.Handle, v.Status, v.N)
		if v.Now != "" && Active(v.Status) {
			fmt.Fprintf(&b, " · now: %s", v.Now)
		}
		if v.Queue > 0 {
			fmt.Fprintf(&b, " · #%d in queue", v.Queue)
		}
		fmt.Fprintf(&b, "\n  task: %s\n", clip(v.Task, 160))
		if v.Error != "" {
			fmt.Fprintf(&b, "  error: %s\n", clip(v.Error, 200))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

func (m *Manager) view(a *Agent) View {
	v := View{Handle: a.Handle, Task: a.Task, Conv: a.Conv, Status: a.Status, Now: a.Now, N: len(a.Steps), Result: a.Result, Error: a.Error,
		Steps: make([]Step, len(a.Steps))}
	for i, s := range a.Steps {
		if len(s.Result) > 2000 {
			s.Result = s.Result[:2000] + "\n…"
		}
		v.Steps[i] = s
	}
	if !a.Started.IsZero() {
		v.Started = a.Started.UnixMilli()
		end := time.Now()
		if !a.Ended.IsZero() {
			end = a.Ended
			v.Ended = a.Ended.UnixMilli()
		}
		v.Seconds = end.Sub(a.Started).Seconds()
	}
	if a.Status == Queued {
		for i, h := range m.queue {
			if h == a.Handle {
				v.Queue = i + 1
			}
		}
	}
	return v
}

// ---------------- persistence ----------------

func (m *Manager) save(a *Agent) {
	if m.dir == "" {
		return
	}
	m.mu.Lock()
	b, err := json.Marshal(a)
	m.mu.Unlock()
	if err != nil {
		return
	}
	_ = os.MkdirAll(m.dir, 0o700)
	tmp := filepath.Join(m.dir, a.Handle+".json.tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(m.dir, a.Handle+".json"))
	}
}

// Load reads saved agents. Any that were queued, running or waiting when the app
// closed come back as interrupted.
func (m *Manager) Load() int {
	if m.dir == "" {
		return 0
	}
	ents, _ := os.ReadDir(m.dir)
	n := 0
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(m.dir, e.Name()))
		if err != nil {
			continue
		}
		var a Agent
		if json.Unmarshal(b, &a) != nil || a.Handle == "" {
			continue
		}
		if Active(a.Status) {
			a.Status, a.Now = Interrupted, ""
			n++
		}
		m.mu.Lock()
		if _, ok := m.agents[a.Handle]; !ok {
			cp := a
			m.agents[a.Handle] = &cp
		}
		m.mu.Unlock()
		if a.Status == Interrupted {
			m.save(&a)
		}
	}
	return n
}

// Prune deletes finished agents older than age (keeps interrupted ones).
func (m *Manager) Prune(age time.Duration) {
	m.mu.Lock()
	var gone []string
	for h, a := range m.agents {
		if (a.Status == Done || a.Status == Failed || a.Status == Cancelled) && !a.Ended.IsZero() && time.Since(a.Ended) > age {
			delete(m.agents, h)
			gone = append(gone, h)
		}
	}
	m.mu.Unlock()
	for _, h := range gone {
		if m.dir != "" {
			_ = os.Remove(filepath.Join(m.dir, h+".json"))
		}
	}
}

// StopAll cancels running agents without marking them cancelled (app shutdown): their
// saved state stays "running", so the next launch shows them as interrupted.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.cancel != nil {
			a.gen++ // the goroutine's finish must not save a final status
			a.cancel()
		}
	}
}

func trim(h []llm.Message) []llm.Message {
	const max = 60
	if len(h) <= max {
		return append([]llm.Message(nil), h...)
	}
	cut := len(h) - max
	for cut < len(h) && h[cut].Role != "user" {
		cut++
	}
	// Always keep the brief.
	return append([]llm.Message{h[0]}, h[cut:]...)
}
