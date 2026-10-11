package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

// fakeLLM answers by agent brief. Each script step either blocks until released,
// returns a tool call, or returns final text.
type fakeLLM struct {
	mu      sync.Mutex
	gates   map[string]chan struct{} // brief -> closed when the agent may finish
	calls   map[string]int
	seen    map[string][][]llm.Message
	toolFor map[string]string // brief -> tool to call on its first step
}

func newFake() *fakeLLM {
	return &fakeLLM{gates: map[string]chan struct{}{}, calls: map[string]int{}, seen: map[string][][]llm.Message{}, toolFor: map[string]string{}}
}

func (f *fakeLLM) gate(brief string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gates[brief] == nil {
		f.gates[brief] = make(chan struct{})
	}
	return f.gates[brief]
}

func (f *fakeLLM) complete(ctx context.Context, msgs []llm.Message, _ []llm.Tool) (*llm.Result, error) {
	brief := msgs[1].Content
	f.mu.Lock()
	f.calls[brief]++
	n := f.calls[brief]
	f.seen[brief] = append(f.seen[brief], msgs)
	tool := f.toolFor[brief]
	f.mu.Unlock()
	if tool != "" && n == 1 {
		r := &llm.Result{}
		var tc llm.ToolCall
		tc.ID, tc.Type = "c1", "function"
		tc.Function.Name, tc.Function.Arguments = tool, `{"path":"package.json"}`
		r.ToolCalls = []llm.ToolCall{tc}
		return r, nil
	}
	select {
	case <-f.gate(brief):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &llm.Result{Text: "Report for: " + brief}, nil
}

type harness struct {
	m        *Manager
	f        *fakeLLM
	mu       sync.Mutex
	results  []View
	execGate chan struct{}
}

func newHarness(t *testing.T, cap int, dir string) *harness {
	t.Helper()
	h := &harness{f: newFake()}
	h.m = New(Runtime{
		Complete: h.f.complete,
		Tools:    func([]string) []llm.Tool { return []llm.Tool{AskTool()} },
		Known:    func() []string { return []string{"file_read", "ask_user"} },
		Exec: func(ctx context.Context, a *Agent, name, args string) string {
			if h.execGate != nil {
				select {
				case <-h.execGate:
				case <-ctx.Done():
				}
			}
			return "Read package.json (20 lines)"
		},
		System:   func(a *Agent) string { return "system " + a.Handle },
		Describe: func(name, args string) string { return "Reading package.json" },
	}, dir, cap)
	h.m.OnResult = func(v View) { h.mu.Lock(); h.results = append(h.results, v); h.mu.Unlock() }
	return h
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func status(m *Manager, h string) string { v, _ := m.Get(h); return v.Status }

func TestDispatchReturnsImmediately(t *testing.T) {
	h := newHarness(t, 3, "")
	start := time.Now()
	r := h.m.HandleMainTool("dispatch_agent", `{"handle":"Fix Tests","task":"Run the tests and fix them"}`, "c1", "/tmp")
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Fatalf("dispatch blocked for %v", took)
	}
	if r.Dispatched != "fix-tests" || !strings.HasPrefix(r.Result, "started") {
		t.Fatalf("got %+v", r)
	}
	waitFor(t, "running", func() bool { return status(h.m, "fix-tests") == Running })
	// The model is still "thinking" (gate closed): the agent is running, not done.
	if len(h.results) != 0 {
		t.Fatal("result arrived before the agent finished")
	}
	// A duplicate handle while it runs is refused (steer instead).
	if d := h.m.HandleMainTool("dispatch_agent", `{"handle":"fix-tests","task":"again"}`, "c1", "/tmp"); !strings.Contains(d.Result, "message_agent") {
		t.Fatalf("duplicate dispatch not refused: %s", d.Result)
	}
	close(h.f.gate("Run the tests and fix them"))
	waitFor(t, "done", func() bool { return status(h.m, "fix-tests") == Done })
}

func TestConcurrencyCapAndQueue(t *testing.T) {
	h := newHarness(t, 2, "")
	for _, n := range []string{"a", "b", "c", "d"} {
		h.m.HandleMainTool("dispatch_agent", `{"handle":"`+n+`","task":"task `+n+`"}`, "c1", "")
	}
	waitFor(t, "two running", func() bool { r, q := h.m.Running(); return r == 2 && q == 2 })
	if status(h.m, "c") != Queued || status(h.m, "d") != Queued {
		t.Fatalf("c=%s d=%s, want queued", status(h.m, "c"), status(h.m, "d"))
	}
	if v, _ := h.m.Get("c"); v.Queue != 1 {
		t.Fatalf("c queue position %d, want 1", v.Queue)
	}
	close(h.f.gate("task a"))
	waitFor(t, "c starts when a finishes", func() bool { return status(h.m, "c") == Running })
	if status(h.m, "d") != Queued {
		t.Fatal("d jumped the queue")
	}
	if r, _ := h.m.Running(); r != 2 {
		t.Fatalf("running %d, want the cap of 2", r)
	}
	// Raising the cap starts the rest.
	h.m.SetCap(3)
	waitFor(t, "d starts after the cap is raised", func() bool { return status(h.m, "d") == Running })
	for _, n := range []string{"b", "c", "d"} {
		close(h.f.gate("task " + n))
	}
	waitFor(t, "all done", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.results) == 4 })
}

func TestResultWakesMainLoopSeparately(t *testing.T) {
	h := newHarness(t, 3, "")
	var mu sync.Mutex
	var turns []Input
	main := &Mainline{Run: func(in Input) { mu.Lock(); turns = append(turns, in); mu.Unlock(); time.Sleep(20 * time.Millisecond) }}
	h.m.OnResult = func(v View) {
		main.Submit(Input{Kind: "agent", Text: ResultMessage(v), Handle: v.Handle, Conv: v.Conv})
	}
	h.m.HandleMainTool("dispatch_agent", `{"handle":"fast","task":"fast task"}`, "c1", "")
	h.m.HandleMainTool("dispatch_agent", `{"handle":"slow","task":"slow task"}`, "c1", "")
	close(h.f.gate("fast task"))
	waitFor(t, "fast result wakes the main agent", func() bool { mu.Lock(); defer mu.Unlock(); return len(turns) == 1 })
	if status(h.m, "slow") != Running {
		t.Fatal("slow agent should still be running")
	}
	mu.Lock()
	first := turns[0]
	mu.Unlock()
	if first.Handle != "fast" || first.Kind != "agent" || !strings.Contains(first.Text, "Report for: fast task") || !strings.HasPrefix(first.Text, ResultPrefix) {
		t.Fatalf("bad wake input: %+v", first)
	}
	// A user message during the run waits its turn; it is never refused.
	main.Submit(Input{Kind: "user", Text: "hi"})
	close(h.f.gate("slow task"))
	waitFor(t, "slow result wakes it again", func() bool { mu.Lock(); defer mu.Unlock(); return len(turns) == 3 })
	mu.Lock()
	defer mu.Unlock()
	if turns[2].Handle != "slow" && turns[1].Handle != "slow" {
		t.Fatalf("slow result missing: %+v", turns)
	}
}

func TestMainlineQueuesInOrder(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var got []string
	main := &Mainline{Run: func(in Input) {
		if in.Text == "first" {
			<-release
		}
		mu.Lock()
		got = append(got, in.Text)
		mu.Unlock()
	}}
	if main.Submit(Input{Kind: "user", Text: "first"}) {
		t.Fatal("first input should run at once")
	}
	if !main.Submit(Input{Kind: "agent", Text: "second"}) || !main.Submit(Input{Kind: "user", Text: "third"}) {
		t.Fatal("inputs during a turn should queue")
	}
	close(release)
	waitFor(t, "queue drains", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 3 })
	if strings.Join(got, ",") != "first,second,third" {
		t.Fatalf("order %v", got)
	}
	waitFor(t, "idle", func() bool { return !main.Busy() })
}

func TestMessageReachesRunningAgent(t *testing.T) {
	h := newHarness(t, 3, "")
	h.execGate = make(chan struct{})
	h.f.toolFor["Set up the project"] = "file_read"
	h.m.HandleMainTool("dispatch_agent", `{"handle":"setup","task":"Set up the project"}`, "c1", "")
	waitFor(t, "first step running", func() bool { v, _ := h.m.Get("setup"); return v.N == 1 })
	if v, _ := h.m.Get("setup"); v.Now != "Reading package.json" {
		t.Fatalf("plain step %q", v.Now)
	}
	r := h.m.HandleMainTool("message_agent", `{"handle":"setup","message":"Use pnpm, not npm."}`, "c1", "")
	if !strings.HasPrefix(r.Result, "Delivered") {
		t.Fatalf("message_agent: %s", r.Result)
	}
	close(h.execGate)
	close(h.f.gate("Set up the project"))
	waitFor(t, "done", func() bool { return status(h.m, "setup") == Done })
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	calls := h.f.seen["Set up the project"]
	if len(calls) != 2 {
		t.Fatalf("model calls %d, want 2", len(calls))
	}
	last := calls[1][len(calls[1])-1]
	if last.Role != "user" || last.Content != "[Update from the main agent] Use pnpm, not npm." {
		t.Fatalf("steering not delivered at the next step: %+v", last)
	}
	// Too late once it's done.
	if r := h.m.HandleMainTool("message_agent", `{"handle":"setup","message":"x"}`, "c1", ""); !strings.HasPrefix(r.Result, "Error") {
		t.Fatalf("message to a finished agent should fail: %s", r.Result)
	}
}

func TestCancel(t *testing.T) {
	h := newHarness(t, 1, "")
	h.m.HandleMainTool("dispatch_agent", `{"handle":"one","task":"task one"}`, "c1", "")
	h.m.HandleMainTool("dispatch_agent", `{"handle":"two","task":"task two"}`, "c1", "")
	waitFor(t, "one running", func() bool { return status(h.m, "one") == Running })
	if r := h.m.HandleMainTool("cancel_agent", `{"handle":"one"}`, "c1", ""); !strings.HasPrefix(r.Result, "Stopped") {
		t.Fatalf("cancel: %s", r.Result)
	}
	waitFor(t, "two takes the free slot", func() bool { return status(h.m, "two") == Running })
	if status(h.m, "one") != Cancelled {
		t.Fatalf("one is %s", status(h.m, "one"))
	}
	// A queued agent can be cancelled too.
	h.m.HandleMainTool("dispatch_agent", `{"handle":"three","task":"task three"}`, "c1", "")
	h.m.Cancel("three")
	if status(h.m, "three") != Cancelled {
		t.Fatal("queued agent not cancelled")
	}
	close(h.f.gate("task two"))
	waitFor(t, "two done", func() bool { return status(h.m, "two") == Done })
	time.Sleep(30 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.results) != 1 || h.results[0].Handle != "two" {
		t.Fatalf("cancelled agents must not wake the main agent: %+v", h.results)
	}
	if !strings.Contains(h.m.Summary("c1"), "one · cancelled") {
		t.Fatalf("summary: %s", h.m.Summary("c1"))
	}
}

func TestRestartMarksInterrupted(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, 3, dir)
	h.execGate = make(chan struct{})
	h.f.toolFor["Long job"] = "file_read"
	h.m.HandleMainTool("dispatch_agent", `{"handle":"long","task":"Long job"}`, "c9", "/work")
	waitFor(t, "mid-step", func() bool { v, _ := h.m.Get("long"); return v.N == 1 })
	h.m.HandleMainTool("dispatch_agent", `{"handle":"finished","task":"Quick job"}`, "c9", "/work")
	close(h.f.gate("Quick job"))
	waitFor(t, "quick done", func() bool { return status(h.m, "finished") == Done })
	h.m.StopAll() // the app quits mid-run
	close(h.execGate)

	b, err := os.ReadFile(filepath.Join(dir, "long.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Agent
	_ = json.Unmarshal(b, &saved)
	if saved.Status != Running || saved.Conv != "c9" {
		t.Fatalf("saved state %s/%s", saved.Status, saved.Conv)
	}

	h2 := newHarness(t, 3, dir)
	if n := h2.m.Load(); n != 1 {
		t.Fatalf("Load marked %d interrupted, want 1", n)
	}
	if status(h2.m, "long") != Interrupted || status(h2.m, "finished") != Done {
		t.Fatalf("after restart: long=%s finished=%s", status(h2.m, "long"), status(h2.m, "finished"))
	}
	if v := h2.m.List("c9"); len(v) != 2 || v[0].Handle != "long" {
		t.Fatalf("list %+v", v)
	}
	// Resume picks up from the saved history (the half-run step is dropped).
	close(h2.f.gate("Long job"))
	if err := h2.m.Resume("long"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resumed run finishes", func() bool { return status(h2.m, "long") == Done })
	h2.f.mu.Lock()
	msgs := h2.f.seen["Long job"][0]
	h2.f.mu.Unlock()
	if last := msgs[len(msgs)-1]; !strings.Contains(last.Content, "You were stopped before finishing") {
		t.Fatalf("resume note missing: %+v", last)
	}
	// Discard removes it from disk.
	if err := h2.m.Discard("finished"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "finished.json")); !os.IsNotExist(err) {
		t.Fatal("discarded agent still on disk")
	}
}

func TestSanitizeTailDropsUnansweredCall(t *testing.T) {
	var tc llm.ToolCall
	tc.ID = "x"
	h := []llm.Message{{Role: "user", Content: "brief"}, {Role: "assistant", ToolCalls: []llm.ToolCall{tc}}}
	if out := sanitizeTail(h); len(out) != 1 {
		t.Fatalf("got %d messages", len(out))
	}
}

func TestCleanHandle(t *testing.T) {
	for in, want := range map[string]string{"Fix Tests!": "fix-tests", "  research/laptops ": "research-laptops", "": ""} {
		if got := CleanHandle(in); got != want {
			t.Errorf("CleanHandle(%q) = %q, want %q", in, got, want)
		}
	}
}
