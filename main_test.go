package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/agents"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
)

// fakeModel is an OpenAI-compatible streaming endpoint scripted for the test.
func fakeModel(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
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
		switch {
		case strings.Contains(sys, "YOU ARE A BACKGROUND AGENT"):
			text("sub report: 42")
		case last.Role == "user" && strings.HasPrefix(last.Content, agents.ResultPrefix):
			text("Your agent found 42.")
		case last.Role == "user":
			call("dispatch_agent", `{"handle":"calc","task":"Work out the answer and report it."}`)
		default:
			text("On it, I'll report back.")
		}
	}))
}

func TestBackgroundAgentEndToEnd(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	srv := fakeModel(t)
	defer srv.Close()
	saved := append([]config.Provider(nil), config.Providers...)
	defer func() { config.Providers = saved }()
	for i := range config.Providers {
		if config.Providers[i].ID == "ollama" {
			config.Providers[i].BaseURL = srv.URL + "/v1/"
		}
	}
	cfg := &config.Config{Chain: []config.Entry{{Provider: "ollama", Model: "fake"}}, NoKeyless: true, MaxSteps: 10}
	info := &sysinfo.Info{OS: "linux", Shell: "sh", ShellPath: "/bin/sh"}
	a := agent.New(cfg, info, t.TempDir(), false)
	a.Quiet = true
	bg := wire(a, true)
	defer bg.m.StopAll()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := a.Ask(ctx, "work it out in the background")
	if err != nil || !strings.Contains(out, "On it") {
		t.Fatalf("main turn = %q, %v", out, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !bg.pending() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	v, ok := bg.nextResult()
	if !ok || v.Status != agents.Done || v.Result != "sub report: 42" {
		t.Fatalf("agent result = %+v (ok=%v)", v, ok)
	}
	out, err = a.Ask(ctx, agents.ResultMessage(v))
	if err != nil || out != "Your agent found 42." {
		t.Fatalf("result turn = %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(agentsDir(), "calc.json")); err != nil {
		t.Fatalf("agent wasn't saved for resume: %v", err)
	}
}

func TestCycleModeAndPrefs(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	info := &sysinfo.Info{OS: "linux", Shell: "sh", ShellPath: "/bin/sh"}
	a := agent.New(&config.Config{}, info, t.TempDir(), false)
	want := []string{"build · autopilot", "plan", "build · auto"}
	for _, w := range want {
		cycleMode(a)
		if got := modeName(a); got != w {
			t.Fatalf("after shift+tab: %q, want %q", got, w)
		}
	}
	setWork(a, true)
	setApproval(a, true)
	p := loadPrefs()
	if p.WorkMode != "plan" || p.Approval != "autopilot" {
		t.Fatalf("prefs = %+v", p)
	}
}

func TestBackgroundAgentAutopilotGate(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	info := &sysinfo.Info{OS: "linux", Shell: "sh", ShellPath: "/bin/sh"}
	a := agent.New(&config.Config{}, info, t.TempDir(), true)
	bg := newBG(a)
	if !bg.gate("x", "Run a risky command?", "rm -rf ./dist") {
		t.Fatal("Autopilot should approve for a background agent")
	}
	if bg.gate("x", "Run a risky command?", "mkfs.ext4 /dev/sda1") {
		t.Fatal("catastrophic command must stay blocked")
	}
	if r := bg.exec(context.Background(), &agents.Agent{Handle: "x"}, "dispatch_agent", `{}`); !strings.HasPrefix(r, "Error") {
		t.Fatalf("sub-agent dispatch = %q", r)
	}
	if r := bg.question(context.Background(), "x", `{"question":"which one?"}`); !strings.Contains(r, "away") {
		t.Fatalf("Autopilot question reply = %q", r)
	}
}

func TestAttachFiles(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello\nworld"), 0o644)
	info := &sysinfo.Info{OS: "linux", Shell: "sh", ShellPath: "/bin/sh"}
	a := agent.New(&config.Config{}, info, dir, false)
	out := attachFiles(a, "summarise @notes.txt please")
	if !strings.Contains(out, `<file path=`) || !strings.Contains(out, "hello\nworld") {
		t.Fatalf("attachment missing: %q", out)
	}
}
