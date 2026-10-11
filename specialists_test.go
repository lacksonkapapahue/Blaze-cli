package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/agents"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
)

// leadModel scripts a lead that briefs one specialist, which writes a file in its own
// worktree; the lead then applies its changes. mainCalls counts the lead's model calls.
func leadModel(t *testing.T, mainCalls *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
				Name    string `json:"name"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		text := func(s string) {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": s}}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
		}
		call := func(name, args string) {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "id": "c1", "type": "function", "function": map[string]any{"name": name, "arguments": args}}}}}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
		}
		sys, last := req.Messages[0].Content, req.Messages[len(req.Messages)-1]
		if strings.Contains(sys, "YOU ARE A SPECIALIST") {
			if last.Role == "user" {
				call("file_write", `{"path":"hello.txt","content":"hi from the specialist\n"}`)
			} else {
				text("Plan: write hello.txt. Done: wrote hello.txt.")
			}
			return
		}
		mainCalls.Add(1)
		switch {
		case last.Role == "user" && strings.HasPrefix(last.Content, agents.ResultPrefix):
			call("apply_specialist", `{"name":"writer","action":"apply"}`)
		case last.Role == "user":
			call("dispatch_specialist", `{"name":"writer","brief":"Create hello.txt saying hi."}`)
		case last.Role == "tool" && last.Name == "apply_specialist":
			text("Applied the writer's change.")
		default:
			text("unexpected")
		}
	}))
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("repo\n"), 0o644)
	run("add", ".")
	run("commit", "-qm", "init")
	return dir
}

// The main chat briefs a specialist and its turn ends at once (one model call, the
// prompt is free); the specialist works in its own worktree, never the user's
// folder; its result wakes the lead, which applies the change.
func TestSpecialistHandoffAndApply(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	var mainCalls atomic.Int32
	srv := leadModel(t, &mainCalls)
	defer srv.Close()
	saved := append([]config.Provider(nil), config.Providers...)
	defer func() { config.Providers = saved }()
	for i := range config.Providers {
		if config.Providers[i].ID == "ollama" {
			config.Providers[i].BaseURL = srv.URL + "/v1/"
		}
	}
	repo := gitRepo(t)
	cfg := &config.Config{Chain: []config.Entry{{Provider: "ollama", Model: "fake"}}, NoKeyless: true, MaxSteps: 10}
	info := &sysinfo.Info{OS: "linux", Shell: "sh", ShellPath: "/bin/sh"}
	a := agent.New(cfg, info, repo, true)
	a.Quiet = true
	bg := wire(a, true)
	defer bg.m.StopAll()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := a.Ask(ctx, "split the work")
	if err != nil || out != agent.HandoffLine {
		t.Fatalf("main turn = %q, %v", out, err)
	}
	if n := mainCalls.Load(); n != 1 {
		t.Fatalf("the lead's turn didn't end at the handoff: %d model calls", n)
	}
	deadline := time.Now().Add(20 * time.Second)
	for !bg.pending() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	v, ok := bg.nextResult()
	if !ok || v.Status != agents.Done || v.Kind != agents.Specialist {
		t.Fatalf("specialist result = %+v (ok=%v)", v, ok)
	}
	if v.Review != "pending" || len(v.Changes) != 1 || v.Changes[0].Path != "hello.txt" {
		t.Fatalf("review = %q, changes = %+v", v.Review, v.Changes)
	}
	if _, err := os.Stat(filepath.Join(repo, "hello.txt")); err == nil {
		t.Fatal("the specialist wrote into the user's folder before review")
	}
	msg := agents.ResultMessage(v)
	if !strings.Contains(msg, "apply_specialist") {
		t.Fatalf("result message doesn't ask for review: %s", msg)
	}
	out, err = a.Ask(ctx, msg)
	if err != nil || out != "Applied the writer's change." {
		t.Fatalf("result turn = %q, %v", out, err)
	}
	b, err := os.ReadFile(filepath.Join(repo, "hello.txt"))
	if err != nil || string(b) != "hi from the specialist\n" {
		t.Fatalf("applied file = %q, %v", b, err)
	}
}

func call(name string) llm.ToolCall {
	var c llm.ToolCall
	c.Function.Name = name
	return c
}

func TestHandedOff(t *testing.T) {
	cases := []struct {
		calls      []llm.ToolCall
		dispatched int
		want       bool
	}{
		{[]llm.ToolCall{call("dispatch_specialist"), call("dispatch_specialist")}, 2, true},
		{[]llm.ToolCall{call("dispatch_agent")}, 1, true},
		{[]llm.ToolCall{call("list_agents")}, 0, false},                       // a status check isn't a handoff
		{[]llm.ToolCall{call("dispatch_agent"), call("file_read")}, 1, false}, // it's still working itself
		{[]llm.ToolCall{call("dispatch_specialist")}, 0, false},               // the dispatch failed
		{nil, 1, false},
	}
	for i, c := range cases {
		if got := handedOff(c.calls, c.dispatched); got != c.want {
			t.Errorf("case %d: handedOff = %v, want %v", i, got, c.want)
		}
	}
}

// A specialist never gets more than the lead: Plan specialists and every agent under
// a Plan lead only read; a Build specialist can't write into the original folder.
func TestSpecialistPermissions(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	info := &sysinfo.Info{OS: "linux", Shell: "sh", ShellPath: "/bin/sh"}
	a := agent.New(&config.Config{}, info, t.TempDir(), false)
	bg := newBG(a)
	defer bg.m.StopAll()
	orig := filepath.Join(t.TempDir(), "repo")
	wt := filepath.Join(t.TempDir(), "wt")
	build := &agents.Agent{Handle: "b", Kind: agents.Specialist, Mode: "build", Iso: &agents.Isolation{Kind: "worktree", Source: orig, Path: wt}}
	plan := &agents.Agent{Handle: "p", Kind: agents.Specialist, Mode: "plan"}

	if r := bg.exec(context.Background(), plan, "run_shell", `{"command":"ls"}`); r != agent.PlanRefusal {
		t.Errorf("Plan specialist ran a command: %q", r)
	}
	if why := bg.block(build, "file_write", `{"path":`+jsonStr(filepath.Join(orig, "a.go"))+`}`); !strings.Contains(why, "original folder") {
		t.Errorf("Build specialist wrote into the original: %q", why)
	}
	if why := bg.block(build, "file_write", `{"path":`+jsonStr(filepath.Join(wt, "a.go"))+`}`); why != "" {
		t.Errorf("Build specialist can't write its own copy: %q", why)
	}
	if why := bg.block(build, "file_edit", `{"path":"a.go"}`); why != "" {
		t.Errorf("relative paths resolve in its copy: %q", why)
	}
	for _, n := range []string{"dispatch_specialist", "message_agent", "apply_specialist"} {
		if r := bg.exec(context.Background(), build, n, `{}`); !strings.HasPrefix(r, "Error") {
			t.Errorf("a specialist ran %s: %q", n, r)
		}
	}
	names := map[string]bool{}
	for _, tl := range bg.tools(plan) {
		names[tl.Function.Name] = true
	}
	if names["run_shell"] || names["file_write"] || !names["file_read"] {
		t.Errorf("Plan specialist tools: %v", names)
	}
	a.Env.Plan = true
	if !bg.plan(&agents.Agent{Handle: "x"}) {
		t.Error("an agent kept Build while the lead is in Plan")
	}
	if !leadPlanAllows["dispatch_specialist"] || leadPlanAllows["apply_specialist"] || leadPlanAllows["dispatch_agent"] {
		t.Error("Plan lead: may brief Plan specialists, not apply or start Build agents")
	}
	if r := bg.lead("apply_specialist", `{"name":"b","action":"apply"}`); !strings.Contains(r, "Plan mode") {
		t.Errorf("Plan lead applied changes: %q", r)
	}
	// In Plan, a dispatched specialist is a Plan specialist.
	r := bg.lead("dispatch_specialist", `{"name":"scout","brief":"look around","mode":"build"}`)
	if v, ok := bg.m.Get("scout"); !ok || v.Mode != "plan" || !strings.Contains(r, "Plan") {
		t.Errorf("Plan lead dispatched %+v: %q", v, r)
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }
