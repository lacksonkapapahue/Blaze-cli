package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if !HasGit() {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "one\n")
	write(t, filepath.Join(dir, "pkg", "b.txt"), "two\n")
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init"}} {
		if _, err := git(dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestWorktreeIsolationApply(t *testing.T) {
	repo := gitRepo(t)
	dest := filepath.Join(t.TempDir(), "wt", "spec")
	iso, err := Isolate(repo, dest, "blaze/spec")
	if err != nil {
		t.Fatal(err)
	}
	if iso.Kind != "worktree" || iso.Branch != "blaze/spec" || iso.Workdir() != dest {
		t.Fatalf("got %+v", iso)
	}
	// Work in the copy: the original stays untouched.
	write(t, filepath.Join(dest, "a.txt"), "one\nchanged\n")
	write(t, filepath.Join(dest, "new.txt"), "new\n")
	if err := os.Remove(filepath.Join(dest, "pkg", "b.txt")); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(repo, "a.txt")) != "one\n" || read(t, filepath.Join(repo, "new.txt")) != "<missing>" {
		t.Fatal("the original changed before apply")
	}
	cs, patch, err := iso.Changes(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 3 || !strings.Contains(patch, "+changed") {
		t.Fatalf("changes %+v\n%s", cs, patch)
	}
	if _, err := iso.Apply("test"); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(repo, "a.txt")) != "one\nchanged\n" || read(t, filepath.Join(repo, "new.txt")) != "new\n" || read(t, filepath.Join(repo, "pkg", "b.txt")) != "<missing>" {
		t.Fatal("apply did not bring the changes over")
	}
	// The user's branch didn't move: the changes are uncommitted for them to review.
	if log, _ := git(repo, "log", "--oneline"); strings.Count(strings.TrimSpace(log), "\n") != 0 {
		t.Fatalf("apply committed on the user's branch:\n%s", log)
	}
	if err := iso.Remove(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("worktree folder not removed")
	}
	if _, err := git(repo, "rev-parse", "--verify", "-q", "refs/heads/blaze/spec"); err == nil {
		t.Fatal("branch not removed")
	}
}

func TestWorktreeSubfolderAndParallelBranches(t *testing.T) {
	repo := gitRepo(t)
	root := t.TempDir()
	a, err := Isolate(filepath.Join(repo, "pkg"), filepath.Join(root, "a"), "blaze/x")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Isolate(filepath.Join(repo, "pkg"), filepath.Join(root, "b"), "blaze/x")
	if err != nil {
		t.Fatal(err)
	}
	if a.Branch == b.Branch {
		t.Fatalf("two specialists share branch %s", a.Branch)
	}
	if a.Sub != "pkg" || a.Workdir() != filepath.Join(root, "a", "pkg") {
		t.Fatalf("subfolder: %+v", a)
	}
	write(t, filepath.Join(a.Workdir(), "b.txt"), "from a\n")
	if read(t, filepath.Join(b.Workdir(), "b.txt")) != "two\n" {
		t.Fatal("specialist b sees specialist a's edit")
	}
	if cs, _, _ := b.Changes(0); len(cs) != 0 {
		t.Fatalf("b has changes it didn't make: %+v", cs)
	}
	_ = a.Remove(true)
	_ = b.Remove(true)
}

func TestCopyIsolationConflicts(t *testing.T) {
	src := t.TempDir() // not a git repo: a plain copy
	write(t, filepath.Join(src, "a.txt"), "a\n")
	write(t, filepath.Join(src, "b.txt"), "b\n")
	write(t, filepath.Join(src, "node_modules", "big.js"), "skip me")
	dest := filepath.Join(t.TempDir(), "copy")
	iso, err := Isolate(src, dest, "blaze/c")
	if err != nil {
		t.Fatal(err)
	}
	if iso.Kind != "copy" {
		t.Fatalf("kind %s", iso.Kind)
	}
	if _, err := os.Stat(filepath.Join(dest, "node_modules")); err == nil {
		t.Fatal("node_modules was copied")
	}
	write(t, filepath.Join(dest, "a.txt"), "a2\n")
	write(t, filepath.Join(dest, "b.txt"), "b2\n")
	write(t, filepath.Join(dest, "c.txt"), "c\n")
	// The user edits b.txt in the original meanwhile: that one must not be overwritten.
	write(t, filepath.Join(src, "b.txt"), "user\n")
	msg, err := iso.Apply("")
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(src, "a.txt")) != "a2\n" || read(t, filepath.Join(src, "c.txt")) != "c\n" {
		t.Fatal("changes not applied")
	}
	if read(t, filepath.Join(src, "b.txt")) != "user\n" || !strings.Contains(msg, "b.txt") {
		t.Fatalf("conflict not protected: %s", msg)
	}
	_ = iso.Remove(true)
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("copy not removed")
	}
}

// specHarness runs specialists whose fake model edits a file in its workdir on step 1.
func specHarness(t *testing.T, cap int) (*Manager, *fakeLLM, *[]View) {
	f := newFake()
	var results []View
	m := New(Runtime{
		Complete: func(ctx context.Context, msgs []llm.Message, ts []llm.Tool) (*llm.Result, error) {
			brief := msgs[1].Content
			f.mu.Lock()
			f.calls[brief]++
			n := f.calls[brief]
			f.seen[brief] = append(f.seen[brief], msgs)
			f.mu.Unlock()
			if n == 1 {
				r := &llm.Result{Text: "Plan: edit a.txt, then report."}
				var tc llm.ToolCall
				tc.ID, tc.Type = "c1", "function"
				tc.Function.Name, tc.Function.Arguments = "file_write", `{"path":"a.txt"}`
				r.ToolCalls = []llm.ToolCall{tc}
				return r, nil
			}
			select {
			case <-f.gate(brief):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &llm.Result{Text: "Done: " + brief}, nil
		},
		Tools: func(*Agent) []llm.Tool { return nil },
		Known: func() []string { return []string{"file_write"} },
		Exec: func(ctx context.Context, a *Agent, name, args string) string {
			// The host's env roots tools at a.Folder, which is the specialist's own copy.
			write(t, filepath.Join(a.Folder, "a.txt"), "by "+a.Handle+"\n")
			return "Wrote a.txt"
		},
		System: func(a *Agent) string { return AgentSystem(a) },
	}, "", cap)
	m.SetIsolationDir(t.TempDir())
	var mu = &f.mu
	m.OnResult = func(v View) { mu.Lock(); results = append(results, v); mu.Unlock() }
	return m, f, &results
}

func TestSpecialistLifecycle(t *testing.T) {
	repo := gitRepo(t)
	m, f, results := specHarness(t, 2)
	lead := Lead{Conv: "c1", Folder: repo, Mode: "build", MaxSteps: 12}
	for _, n := range []string{"one", "two", "three"} {
		r := m.HandleLeadTool("dispatch_specialist", `{"name":"`+n+`","brief":"brief `+n+`"}`, lead)
		if r.Dispatched != n {
			t.Fatalf("dispatch %s: %s", n, r.Result)
		}
	}
	// The cap holds: two run, the third queues.
	waitFor(t, "two running", func() bool { r, q := m.Running(); return r == 2 && q == 1 })
	if v, _ := m.Get("three"); v.Status != Queued || v.Kind != Specialist || v.Mode != "build" {
		t.Fatalf("three: %+v", v)
	}
	// Each running one has its own worktree with its own edit; the repo is untouched.
	waitFor(t, "edits", func() bool {
		a, _ := m.Get("one")
		b, _ := m.Get("two")
		return a.Path != "" && b.Path != "" && read(t, filepath.Join(a.Path, "a.txt")) == "by one\n" && read(t, filepath.Join(b.Path, "a.txt")) == "by two\n"
	})
	if read(t, filepath.Join(repo, "a.txt")) != "one\n" {
		t.Fatal("a specialist wrote into the original repo")
	}
	// Check shows status, last action and plan.
	chk := m.CheckSpecialist("one", "c1", false)
	if !strings.Contains(chk, "plan: edit a.txt") || !strings.Contains(chk, "running") || !strings.Contains(chk, "worktree") {
		t.Fatalf("check: %s", chk)
	}
	// Steering reaches it at its next step.
	if r := m.HandleLeadTool("message_specialist", `{"name":"one","message":"also add a test"}`, lead); !strings.Contains(r.Result, "Delivered") {
		t.Fatal(r.Result)
	}
	// Cancel two: three starts in its slot.
	if r := m.HandleLeadTool("cancel_specialist", `{"name":"two"}`, lead); !strings.Contains(r.Result, "Stopped") {
		t.Fatal(r.Result)
	}
	waitFor(t, "three starts", func() bool { return status(m, "three") == Running })
	close(f.gate("brief one"))
	waitFor(t, "one done", func() bool { return status(m, "one") == Done })
	waitFor(t, "result", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(*results) == 1 })
	f.mu.Lock()
	v := (*results)[0]
	f.mu.Unlock()
	if v.Review != "pending" || len(v.Changes) != 1 {
		t.Fatalf("result view: %+v", v)
	}
	msg := ResultMessage(v)
	if !strings.Contains(msg, "apply_specialist") || !strings.Contains(msg, "Nothing has reached the user's folder") {
		t.Fatalf("result message: %s", msg)
	}
	// The steer arrived in its history.
	f.mu.Lock()
	seen := f.seen["brief one"]
	f.mu.Unlock()
	last := seen[len(seen)-1]
	if !strings.Contains(last[len(last)-1].Content, "also add a test") {
		t.Fatal("steer message not delivered")
	}
	// Review: the diff, then apply.
	if d := m.CheckSpecialist("one", "c1", true); !strings.Contains(d, "+by one") {
		t.Fatalf("diff missing: %s", d)
	}
	if r := m.HandleLeadTool("apply_specialist", `{"name":"one","action":"apply"}`, lead); !strings.Contains(r.Result, "Applied 1 file") {
		t.Fatal(r.Result)
	}
	if read(t, filepath.Join(repo, "a.txt")) != "by one\n" {
		t.Fatal("apply didn't reach the repo")
	}
	if r := m.HandleLeadTool("apply_specialist", `{"name":"one","action":"apply"}`, lead); !strings.Contains(r.Result, "already applied") {
		t.Fatal("double apply not refused: " + r.Result)
	}
	// The cancelled one can be discarded: its worktree goes away.
	tw, _ := m.Get("two")
	if r := m.HandleLeadTool("apply_specialist", `{"name":"two","action":"discard"}`, lead); !strings.Contains(r.Result, "Discarded") {
		t.Fatal(r.Result)
	}
	if _, err := os.Stat(tw.Path); !os.IsNotExist(err) {
		t.Fatal("discarded worktree still there")
	}
	close(f.gate("brief three"))
	waitFor(t, "three done", func() bool { return status(m, "three") == Done })
	_, _ = m.ApplySpecialist("three", "discard")
}

func TestSpecialistNeverExceedsLead(t *testing.T) {
	repo := gitRepo(t)
	m, f, _ := specHarness(t, 3)
	// The lead is in Plan: a Build request becomes a Plan specialist, with no copy.
	r := m.HandleLeadTool("dispatch_specialist", `{"name":"scout","brief":"scout it","mode":"build"}`, Lead{Conv: "c1", Folder: repo, Mode: "plan", MaxSteps: 5})
	if !strings.Contains(r.Result, "Plan specialist") {
		t.Fatalf("not capped: %s", r.Result)
	}
	waitFor(t, "running", func() bool { return status(m, "scout") == Running })
	v, _ := m.Get("scout")
	if v.Mode != "plan" || v.Path != "" {
		t.Fatalf("plan specialist got %+v", v)
	}
	if !strings.Contains(SpecSystem(m.agents["scout"]), "PLAN: read and scout only") {
		t.Fatal("plan prompt missing")
	}
	close(f.gate("scout it"))
	waitFor(t, "done", func() bool { return status(m, "scout") == Done })
	// It inherits the lead's step budget.
	if m.agents["scout"].MaxSteps != 5 {
		t.Fatal("step budget not inherited")
	}
	if _, err := m.ApplySpecialist("scout", "apply"); err == nil {
		t.Fatal("applying a Plan specialist should fail")
	}
}

func TestSpecialistGuardStops(t *testing.T) {
	m, f, results := specHarness(t, 1)
	m.rt.Guard = func(a *Agent) string { return "stopped: today's spend cap ($1.00) is reached" }
	m.HandleLeadTool("dispatch_specialist", `{"name":"g","brief":"guarded","mode":"plan"}`, Lead{Conv: "c1", Mode: "build"})
	waitFor(t, "failed", func() bool { return status(m, "g") == Failed })
	waitFor(t, "result callback", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(*results) == 1
	})
	v, _ := m.Get("g")
	f.mu.Lock()
	resultCount := len(*results)
	f.mu.Unlock()
	if !strings.Contains(v.Error, "spend cap") || resultCount != 1 {
		t.Fatalf("%+v", v)
	}
}
