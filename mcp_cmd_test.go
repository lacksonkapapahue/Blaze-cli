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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/mcp"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
)

// fakeMCPServer is a Streamable HTTP MCP server with a read-only "lookup" and a
// "post" that changes things; posts counts the posts that ran.
func fakeMCPServer(posts *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		if len(m.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		var res string
		switch m.Method {
		case "initialize":
			res = `{"protocolVersion":"2025-06-18","serverInfo":{"name":"web","version":"1"}}`
		case "tools/list":
			res = `{"tools":[{"name":"lookup","description":"Look something up","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}},{"name":"post","description":"Post a message","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}]}`
		case "tools/call":
			if m.Params.Name == "post" {
				posts.Add(1)
			}
			res = `{"content":[{"type":"text","text":"` + m.Params.Name + ` ok"}]}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, m.ID, res)
	}))
}

// mcpModel calls mcp__web__post on a user message and then repeats the tool's
// answer; it records the tool names it was offered.
func mcpModel(offered *[]string, mu *sync.Mutex) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		*offered = (*offered)[:0]
		for _, t := range req.Tools {
			*offered = append(*offered, t.Function.Name)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		last := req.Messages[len(req.Messages)-1]
		var b []byte
		if last.Role == "user" {
			b, _ = json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "id": "c1", "type": "function", "function": map[string]any{"name": "mcp__web__post", "arguments": `{"text":"hi"}`}}}}}}})
		} else {
			b, _ = json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "TOOL SAID: " + last.Content}}}})
		}
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
	}))
}

func TestMCPToolsThroughTheAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BLAZE_HOME", home)
	var posts atomic.Int32
	srv := fakeMCPServer(&posts)
	defer srv.Close()
	if err := mcp.Add(mcp.GlobalFile(home), mcp.Server{Name: "web", URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	var offered []string
	var mu sync.Mutex
	model := mcpModel(&offered, &mu)
	defer model.Close()
	saved := append([]config.Provider(nil), config.Providers...)
	defer func() { config.Providers = saved }()
	for i := range config.Providers {
		if config.Providers[i].ID == "ollama" {
			config.Providers[i].BaseURL = model.URL + "/v1/"
		}
	}
	savedHook := confirmHook
	defer func() { confirmHook = savedHook }()
	mcpOnce, mcpMgr = sync.Once{}, nil
	defer func() { stopMCP(); mcpOnce, mcpMgr = sync.Once{}, nil }()

	cfg := &config.Config{Chain: []config.Entry{{Provider: "ollama", Model: "fake"}}, NoKeyless: true, MaxSteps: 5}
	info := &sysinfo.Info{OS: "linux", Shell: "sh", ShellPath: "/bin/sh"}
	dir := t.TempDir()
	t.Chdir(dir)
	a := agent.New(cfg, info, dir, false)
	a.Quiet = true
	wire(a, false)
	startMCP()
	getMCP().Wait(10 * time.Second)

	ask := func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := a.Ask(ctx, "post hi")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	has := func(name string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, n := range offered {
			if n == name {
				return true
			}
		}
		return false
	}

	// Plan: only the read-only tool is offered, and the other is refused.
	a.Env.Plan = true
	if out := ask(); !strings.Contains(out, "Plan mode is on") || posts.Load() != 0 {
		t.Fatalf("plan: %q posts=%d", out, posts.Load())
	}
	if !has("mcp__web__lookup") || has("mcp__web__post") {
		t.Fatalf("plan offered %v", offered)
	}

	// Auto: asks first; No declines.
	a.Env.Plan = false
	asks := 0
	answer := [2]bool{false, false}
	confirmHook = func(ctx context.Context, title, detail string, always bool) (bool, bool) {
		asks++
		if !always || !strings.Contains(title, "web") {
			t.Errorf("prompt %q always=%v", title, always)
		}
		return answer[0], answer[1]
	}
	if out := ask(); !strings.Contains(out, "Declined") || posts.Load() != 0 || asks != 1 {
		t.Fatalf("auto no: %q posts=%d asks=%d", out, posts.Load(), asks)
	}
	if !has("mcp__web__post") {
		t.Fatalf("build offered %v", offered)
	}
	// Always allow runs it and is remembered.
	answer = [2]bool{true, true}
	if out := ask(); !strings.Contains(out, "post ok") || posts.Load() != 1 || asks != 2 {
		t.Fatalf("auto always: %q posts=%d asks=%d", out, posts.Load(), asks)
	}
	if out := ask(); !strings.Contains(out, "post ok") || posts.Load() != 2 || asks != 2 {
		t.Fatalf("remembered: %q posts=%d asks=%d", out, posts.Load(), asks)
	}

	// Autopilot: runs without asking.
	_ = getMCP().Forget("web")
	a.Approval = "autopilot"
	if out := ask(); !strings.Contains(out, "post ok") || posts.Load() != 3 || asks != 2 {
		t.Fatalf("autopilot: %q posts=%d asks=%d", out, posts.Load(), asks)
	}
}

func TestParseMCPAdd(t *testing.T) {
	s, project, err := parseMCPAdd([]string{"gh", "--env", "TOKEN=abc", "--project", "--", "npx", "-y", "@modelcontextprotocol/server-github", "--flag"})
	if err != nil || !project || s.Command != "npx" || len(s.Args) != 3 || s.Args[2] != "--flag" || s.Env["TOKEN"] != "abc" {
		t.Fatalf("%+v %v %v", s, project, err)
	}
	s, _, err = parseMCPAdd([]string{"docs", "--url", "https://x.dev/mcp", "--header", "Authorization=Bearer t", "-H", "X-Team: blaze"})
	if err != nil || s.URL != "https://x.dev/mcp" || s.Headers["Authorization"] != "Bearer t" || s.Headers["X-Team"] != "blaze" {
		t.Fatalf("%+v %v", s, err)
	}
	for _, bad := range [][]string{{}, {"x"}, {"x", "--url", "ftp://y"}, {"x", "--url", "https://y", "--", "cmd"}, {"bad name", "--", "cmd"}, {"x", "cmd"}} {
		if _, _, err := parseMCPAdd(bad); err == nil {
			t.Errorf("%v should fail", bad)
		}
	}
}

func TestMCPCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BLAZE_HOME", home)
	dir := t.TempDir()
	t.Chdir(dir)
	mcpOnce, mcpMgr = sync.Once{}, nil
	defer func() { mcpOnce, mcpMgr = sync.Once{}, nil }()
	if err := cmdMCP([]string{"add", "files", "--", "blaze-no-such-server", "/tmp"}, false); err != nil {
		t.Fatal(err)
	}
	if err := cmdMCP([]string{"add", "local", "--project", "--url", "http://127.0.0.1:1/mcp"}, false); err != nil {
		t.Fatal(err)
	}
	if err := cmdMCP([]string{"disable", "files"}, false); err != nil {
		t.Fatal(err)
	}
	ss, _ := mcp.Load(home, dir)
	if len(ss) != 2 || !ss[0].Disabled || ss[0].Name != "files" || ss[1].Source != filepath.Join(dir, ".blaze", "mcp.json") {
		t.Fatalf("%+v", ss)
	}
	if err := cmdMCP([]string{"remove", "local"}, false); err != nil {
		t.Fatal(err)
	}
	if err := cmdMCP([]string{"remove", "local"}, false); err == nil {
		t.Fatal("removing twice should fail")
	}
	if b, _ := os.ReadFile(mcp.GlobalFile(home)); !strings.Contains(string(b), `"disabled": true`) {
		t.Fatalf("global file: %s", b)
	}
}
