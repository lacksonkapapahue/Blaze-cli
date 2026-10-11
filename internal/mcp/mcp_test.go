package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The test binary doubles as a fake stdio MCP server: with BLAZE_FAKE_MCP set it
// speaks MCP on stdin/stdout instead of running the tests.
func TestMain(m *testing.M) {
	if mode := os.Getenv("BLAZE_FAKE_MCP"); mode != "" {
		fakeServer(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeServer(mode string) {
	if mode == "crash" {
		fmt.Fprintln(os.Stderr, "fake server: missing API_TOKEN")
		os.Exit(3)
	}
	fmt.Println("fake server banner (not JSON, must be skipped)")
	out := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m message
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if len(m.ID) == 0 {
			continue // notifications/initialized
		}
		r := message{JSONRPC: "2.0", ID: m.ID}
		switch m.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if mode == "oldproto" && p.ProtocolVersion != "2024-11-05" {
				r.Error = &RPCError{Code: -32602, Message: "Unsupported protocol version"}
				break
			}
			r.Result = json.RawMessage(fmt.Sprintf(`{"protocolVersion":%q,"capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1.0"}}`, p.ProtocolVersion))
		case "tools/list":
			var p struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if p.Cursor == "" {
				r.Result = json.RawMessage(`{"tools":[{"name":"echo","description":"Echo text","inputSchema":{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"text":{"type":"string"}},"required":["text"]},"annotations":{"readOnlyHint":true}}],"nextCursor":"page2"}`)
			} else {
				r.Result = json.RawMessage(`{"tools":[{"name":"write.file","description":"Write a file","inputSchema":{"type":"object"}},{"name":"picture","description":"Returns an image","inputSchema":{"type":"object"}}]}`)
			}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(m.Params, &p)
			switch p.Name {
			case "echo":
				r.Result = json.RawMessage(fmt.Sprintf(`{"content":[{"type":"text","text":"echo: %v"}]}`, p.Arguments["text"]))
			case "write.file":
				r.Result = json.RawMessage(`{"content":[{"type":"text","text":"disk full"}],"isError":true}`)
			case "picture":
				r.Result = json.RawMessage(`{"content":[{"type":"text","text":"here it is"},{"type":"image","data":"AAAA","mimeType":"image/png"}]}`)
			case "die":
				os.Exit(1)
			default:
				r.Error = &RPCError{Code: -32602, Message: "Unknown tool: " + p.Name}
			}
		default:
			r.Error = &RPCError{Code: -32601, Message: "Method not found"}
		}
		_ = out.Encode(r)
	}
}

func fakeStdio(t *testing.T, mode string) Server {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Server{Name: "fake", Command: exe, Args: []string{"-test.run=^$"}, Env: map[string]string{"BLAZE_FAKE_MCP": mode}}
}

func TestStdioHandshakeListCall(t *testing.T) {
	ctx := context.Background()
	c, err := Connect(ctx, fakeStdio(t, "normal"), t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Protocol != ProtocolVersion || c.Info != "fake 1.0" {
		t.Fatalf("handshake: protocol %q info %q", c.Protocol, c.Info)
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 || tools[0].Name != "echo" || !tools[0].ReadOnly() || tools[1].ReadOnly() {
		t.Fatalf("tools (with pagination): %+v", tools)
	}
	r, err := c.CallTool(ctx, "echo", map[string]any{"text": "hi"})
	if err != nil || r.Text != "echo: hi" || r.IsError {
		t.Fatalf("call: %+v %v", r, err)
	}
	r, err = c.CallTool(ctx, "write.file", nil)
	if err != nil || !r.IsError || r.Text != "disk full" {
		t.Fatalf("isError: %+v %v", r, err)
	}
	r, _ = c.CallTool(ctx, "picture", nil)
	if !strings.Contains(r.Text, "here it is") || !strings.Contains(r.Text, "[image content (image/png) returned, not shown]") {
		t.Fatalf("non-text content: %q", r.Text)
	}
	if _, err := c.CallTool(ctx, "nope", nil); err == nil || !strings.Contains(err.Error(), "Unknown tool") {
		t.Fatalf("rpc error: %v", err)
	}
}

func TestStdioProtocolFallback(t *testing.T) {
	c, err := Connect(context.Background(), fakeStdio(t, "oldproto"), t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Protocol != "2024-11-05" {
		t.Fatalf("fell back to %q", c.Protocol)
	}
}

func TestStdioCrashReportsStderr(t *testing.T) {
	_, err := Connect(context.Background(), fakeStdio(t, "crash"), t.TempDir(), "test")
	if err == nil || !strings.Contains(err.Error(), "missing API_TOKEN") {
		t.Fatalf("want the server's stderr in the error, got %v", err)
	}
}

func TestMissingCommand(t *testing.T) {
	_, err := Connect(context.Background(), Server{Name: "x", Command: "blaze-no-such-mcp-server"}, "", "test")
	if err == nil || !strings.Contains(err.Error(), "couldn't find") {
		t.Fatalf("got %v", err)
	}
}

// ---------------- HTTP ----------------

type fakeHTTP struct {
	mu       sync.Mutex
	sse      bool
	protoHdr []string
	deleted  bool
	sessions []string
}

func (f *fakeHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer secret" {
		http.Error(w, "no token", http.StatusUnauthorized)
		return
	}
	if r.Method == "DELETE" {
		f.deleted = r.Header.Get("Mcp-Session-Id") == "sess-1"
		return
	}
	var m message
	_ = json.NewDecoder(r.Body).Decode(&m)
	f.protoHdr = append(f.protoHdr, r.Header.Get("MCP-Protocol-Version"))
	f.sessions = append(f.sessions, r.Header.Get("Mcp-Session-Id"))
	if len(m.ID) == 0 {
		w.WriteHeader(202)
		return
	}
	res := message{JSONRPC: "2.0", ID: m.ID}
	switch m.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "sess-1")
		res.Result = json.RawMessage(`{"protocolVersion":"2025-06-18","serverInfo":{"name":"web","version":"2"}}`)
	case "tools/list":
		res.Result = json.RawMessage(`{"tools":[{"name":"search","description":"Search","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}},"annotations":{"readOnlyHint":true}}]}`)
	case "tools/call":
		res.Result = json.RawMessage(`{"content":[{"type":"text","text":"found 3"}]}`)
	}
	b, _ := json.Marshal(res)
	if f.sse {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, ": comment\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\nevent: message\ndata: %s\n\n", b)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func TestHTTP(t *testing.T) {
	for _, sse := range []bool{false, true} {
		f := &fakeHTTP{sse: sse}
		ts := httptest.NewServer(f)
		s := Server{Name: "web", URL: ts.URL, Headers: map[string]string{"Authorization": "Bearer ${BLAZE_TEST_TOKEN}"}}
		t.Setenv("BLAZE_TEST_TOKEN", "secret")
		ctx := context.Background()
		c, err := Connect(ctx, s, "", "test")
		if err != nil {
			t.Fatalf("sse=%v: %v", sse, err)
		}
		tools, err := c.ListTools(ctx)
		if err != nil || len(tools) != 1 || tools[0].Name != "search" {
			t.Fatalf("sse=%v list: %+v %v", sse, tools, err)
		}
		r, err := c.CallTool(ctx, "search", map[string]any{"q": "x"})
		if err != nil || r.Text != "found 3" {
			t.Fatalf("sse=%v call: %+v %v", sse, r, err)
		}
		c.Close()
		f.mu.Lock()
		if f.protoHdr[0] != "" || f.protoHdr[len(f.protoHdr)-1] != "2025-06-18" {
			t.Errorf("sse=%v MCP-Protocol-Version headers: %v", sse, f.protoHdr)
		}
		if f.sessions[0] != "" || f.sessions[len(f.sessions)-1] != "sess-1" {
			t.Errorf("sse=%v session headers: %v", sse, f.sessions)
		}
		if !f.deleted {
			t.Errorf("sse=%v: close didn't DELETE the session", sse)
		}
		f.mu.Unlock()
		ts.Close()
	}
}

func TestHTTPUnauthorized(t *testing.T) {
	ts := httptest.NewServer(&fakeHTTP{})
	defer ts.Close()
	_, err := Connect(context.Background(), Server{Name: "web", URL: ts.URL}, "", "test")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("got %v", err)
	}
}

// ---------------- config ----------------

func writeJSON(t *testing.T, path, s string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMerge(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	writeJSON(t, GlobalFile(home), `{"mcpServers":{"a":{"command":"a-global"},"b":{"url":"https://b"},"c":{"command":"c","disabled":true}}}`)
	writeJSON(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"a":{"command":"a-dotmcp"},"d":{"command":"d","args":["x"]}}}`)
	writeJSON(t, filepath.Join(dir, ".blaze", "mcp.json"), `{"mcpServers":{"a":{"command":"a-project"}}}`)
	ss, warns := Load(home, dir)
	if len(warns) != 0 || len(ss) != 4 {
		t.Fatalf("%+v %v", ss, warns)
	}
	got := map[string]Server{}
	for _, s := range ss {
		got[s.Name] = s
	}
	if got["a"].Command != "a-project" || got["b"].URL != "https://b" || !got["c"].Disabled || got["d"].Args[0] != "x" {
		t.Fatalf("merge: %+v", got)
	}
	writeJSON(t, filepath.Join(dir, ".mcp.json"), `{oops`)
	if _, warns := Load(home, dir); len(warns) != 1 {
		t.Fatalf("bad file should warn: %v", warns)
	}
}

func TestEditKeepsOtherKeys(t *testing.T) {
	f := filepath.Join(t.TempDir(), "mcp.json")
	writeJSON(t, f, `{"desktopOnly":{"x":1},"mcpServers":{"old":{"command":"o","autoApprove":["t"]}}}`)
	if err := Add(f, Server{Name: "gh", Command: "npx", Args: []string{"-y", "server-github"}, Env: map[string]string{"T": "1"}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := SetDisabled(f, "old", true); !ok || err != nil {
		t.Fatal(ok, err)
	}
	b, _ := os.ReadFile(f)
	s := string(b)
	for _, want := range []string{`"desktopOnly"`, `"autoApprove"`, `"disabled": true`, `"server-github"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
	if ok, _ := Remove(f, "gh"); !ok {
		t.Fatal("remove")
	}
	if ok, _ := Remove(f, "gh"); ok {
		t.Fatal("removed twice")
	}
	if err := Add(f, Server{Name: "bad name"}); err == nil {
		t.Fatal("bad name accepted")
	}
}

func TestToolName(t *testing.T) {
	if n := ToolName("git-hub", "list.issues"); n != "mcp__git-hub__list_issues" {
		t.Fatal(n)
	}
	long := ToolName("server", strings.Repeat("x", 80))
	if len(long) > 64 || long == ToolName("server", strings.Repeat("x", 81)) {
		t.Fatalf("long names must be short and distinct: %s", long)
	}
}

// ---------------- manager + gating ----------------

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	home, dir := t.TempDir(), t.TempDir()
	fs := fakeStdio(t, "normal")
	cfg := map[string]any{"mcpServers": map[string]any{
		"fake":   map[string]any{"command": fs.Command, "args": fs.Args, "env": fs.Env},
		"broken": map[string]any{"command": fs.Command, "args": fs.Args, "env": map[string]string{"BLAZE_FAKE_MCP": "crash"}},
		"off":    map[string]any{"command": fs.Command, "disabled": true},
	}}
	b, _ := json.Marshal(cfg)
	writeJSON(t, GlobalFile(home), string(b))
	m := New(home, dir, "test")
	var mu sync.Mutex
	var logs []string
	m.Log = func(s string) { mu.Lock(); logs = append(logs, s); mu.Unlock() }
	m.Start()
	t.Cleanup(m.Close)
	m.Wait(20 * time.Second)
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], "broken") {
		t.Fatalf("the failing server should be logged and skipped: %v", logs)
	}
	return m
}

func TestManager(t *testing.T) {
	m := newTestManager(t)
	st := map[string]State{}
	for _, s := range m.States() {
		st[s.Server.Name] = s
	}
	if st["fake"].Status != Connected || len(st["fake"].Tools) != 3 || st["broken"].Status != Failed || st["off"].Status != Disabled {
		t.Fatalf("states: %+v", st)
	}
	ts := m.Tools()
	if len(ts) != 3 || ts[0].Function.Name != "mcp__fake__echo" {
		t.Fatalf("tools: %+v", ts)
	}
	if _, has := ts[0].Function.Parameters["$schema"]; has || !strings.Contains(ts[0].Function.Description, "MCP server fake") {
		t.Fatalf("schema/description: %+v", ts[0].Function)
	}
	if !m.Handles("mcp__fake__echo") || m.Handles("mcp__broken__echo") || m.Handles("file_read") {
		t.Fatal("Handles")
	}
	if !m.ReadOnly("mcp__fake__echo") || m.ReadOnly("mcp__fake__write_file") {
		t.Fatal("ReadOnly")
	}
	if !strings.Contains(m.Summary(), "- fake: 3 tools") {
		t.Fatal(m.Summary())
	}
}

func TestGating(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	asked := 0
	answer := [2]bool{}
	ask := func(ctx context.Context, title, detail string) (bool, bool) {
		asked++
		if !strings.Contains(title, "fake") || !strings.Contains(detail, "echo") {
			t.Errorf("prompt: %q %q", title, detail)
		}
		return answer[0], answer[1]
	}
	auto := Policy{Ask: ask}

	// Plan: read-only may run (Plan + Autopilot: without asking); anything else is refused.
	if r := m.Call(ctx, "mcp__fake__echo", `{"text":"p"}`, Policy{Plan: true, Autopilot: true, Ask: ask}); r != "echo: p" || asked != 0 {
		t.Fatalf("plan read-only: %q asked=%d", r, asked)
	}
	if r := m.Call(ctx, "mcp__fake__write_file", `{}`, Policy{Plan: true, Ask: ask}); r != PlanRefusal || asked != 0 {
		t.Fatalf("plan write: %q", r)
	}
	// Auto: asks; No declines.
	if r := m.Call(ctx, "mcp__fake__echo", `{"text":"a"}`, auto); !strings.HasPrefix(r, "Declined") || asked != 1 {
		t.Fatalf("auto no: %q", r)
	}
	// Yes runs once and asks again next time.
	answer = [2]bool{true, false}
	if r := m.Call(ctx, "mcp__fake__echo", `{"text":"b"}`, auto); r != "echo: b" || asked != 2 {
		t.Fatalf("auto yes: %q", r)
	}
	// Always allow is remembered (in the file) for that tool.
	answer = [2]bool{true, true}
	m.Call(ctx, "mcp__fake__echo", `{"text":"c"}`, auto)
	if r := m.Call(ctx, "mcp__fake__echo", `{"text":"d"}`, auto); r != "echo: d" || asked != 3 {
		t.Fatalf("always: %q asked=%d", r, asked)
	}
	if !New(m.Home, "", "").Allowed("fake", "echo") {
		t.Fatal("always allow wasn't saved")
	}
	// Autopilot: never asks; tool errors come back as errors.
	if r := m.Call(ctx, "mcp__fake__write_file", `{}`, Policy{Autopilot: true, Ask: ask}); r != "Error from fake (write.file): disk full" || asked != 3 {
		t.Fatalf("autopilot: %q asked=%d", r, asked)
	}
	// No one to ask: declined.
	if r := m.Call(ctx, "mcp__fake__picture", `{}`, Policy{}); !strings.HasPrefix(r, "Declined") {
		t.Fatalf("no asker: %q", r)
	}
	if r := m.Call(ctx, "mcp__fake__echo", `not json`, Policy{Autopilot: true}); !strings.HasPrefix(r, "Error in") {
		t.Fatalf("bad args: %q", r)
	}
	if err := m.Forget("fake"); err != nil || m.Allowed("fake", "echo") {
		t.Fatal("forget")
	}
}

func TestReconnectAfterServerDies(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	// Make "die" callable by pretending the server listed it.
	m.mu.Lock()
	r := m.routes["mcp__fake__echo"]
	r.tool.Name = "die"
	m.routes["mcp__fake__die"] = r
	m.mu.Unlock()
	if out := m.Call(ctx, "mcp__fake__die", `{}`, Policy{Autopilot: true}); !strings.HasPrefix(out, "Error from the MCP server fake") {
		t.Fatalf("die: %q", out)
	}
	if out := m.Call(ctx, "mcp__fake__echo", `{"text":"back"}`, Policy{Autopilot: true}); out != "echo: back" {
		t.Fatalf("after reconnect: %q", out)
	}
}

func TestPlanReadOnlyStillAsksInAuto(t *testing.T) {
	m := newTestManager(t)
	asked := 0
	r := m.Call(context.Background(), "mcp__fake__echo", `{"text":"x"}`, Policy{Plan: true, Ask: func(context.Context, string, string) (bool, bool) {
		asked++
		return true, false
	}})
	if r != "echo: x" || asked != 1 {
		t.Fatalf("%q asked=%d", r, asked)
	}
}
