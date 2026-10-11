package connect

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------- PKCE ----------------

func TestPKCE(t *testing.T) {
	// RFC 7636 appendix B.
	if got := ChallengeFor("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge = %s", got)
	}
	p := NewPKCE()
	if len(p.Verifier) < 43 || len(p.Verifier) > 128 || strings.ContainsAny(p.Verifier, "+/=") {
		t.Fatalf("bad verifier %q", p.Verifier)
	}
	if p.Challenge != ChallengeFor(p.Verifier) || NewPKCE().Verifier == p.Verifier {
		t.Fatal("pkce pair wrong or not random")
	}
}

// ---------------- the attempt state machine ----------------

func cb(state, code string) string {
	v := url.Values{}
	if state != "" {
		v.Set("state", state)
	}
	if code != "" {
		v.Set("code", code)
	}
	return "http://127.0.0.1:43117/callback?" + v.Encode()
}

func TestAttempts(t *testing.T) {
	a := NewAttempts()
	first := a.Begin("google", "http://127.0.0.1:1/callback", nil)
	second := a.Begin("google", "http://127.0.0.1:1/callback", nil)
	if first.State == second.State {
		t.Fatal("states must differ")
	}
	if _, _, err := a.Resolve(cb(first.State, "c1")); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("old attempt: want superseded, got %v", err)
	}
	if _, _, err := a.Resolve(cb("forged", "c1")); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("unknown state: want state_mismatch, got %v", err)
	}
	if _, _, err := a.Resolve(cb("", "c1")); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("no state: want state_mismatch, got %v", err)
	}
	if _, _, err := a.Resolve(""); !errors.Is(err, ErrMissingRedirectURL) {
		t.Fatalf("empty url: want missing_redirect_url, got %v", err)
	}
	at, code, err := a.Resolve(cb(second.State, "c2"))
	if err != nil || code != "c2" || at.Connector != "google" {
		t.Fatalf("good callback: %v %q", err, code)
	}
	if _, _, err := a.Resolve(cb(second.State, "c2")); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("replay must fail, got %v", err)
	}
	// Other connectors don't supersede each other.
	g, s := a.Begin("google", "x", nil), a.Begin("spotify", "x", nil)
	if _, _, err := a.Resolve(cb(g.State, "a")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Resolve(cb(s.State, "b")); err != nil {
		t.Fatal(err)
	}
	// Missing redirect on the attempt itself.
	n := a.Begin("x", "", nil)
	if _, _, err := a.Resolve(cb(n.State, "a")); !errors.Is(err, ErrMissingRedirectURL) {
		t.Fatalf("want missing_redirect_url, got %v", err)
	}
	// Expiry.
	now := time.Now()
	a.Now = func() time.Time { return now }
	e := a.Begin("google", "x", nil)
	a.Now = func() time.Time { return now.Add(11 * time.Minute) }
	if _, _, err := a.Resolve(cb(e.State, "a")); !errors.Is(err, ErrExpired) {
		t.Fatalf("want expired, got %v", err)
	}
	// Provider error and missing code.
	a.Now = time.Now
	d := a.Begin("google", "x", nil)
	var pe *ProviderError
	if _, _, err := a.Resolve(cb(d.State, "") + "&error=access_denied"); !errors.As(err, &pe) || pe.Code != "access_denied" {
		t.Fatalf("want access_denied, got %v", err)
	}
	mc := a.Begin("google", "x", nil)
	if _, _, err := a.Resolve(cb(mc.State, "")); !errors.Is(err, ErrMissingCode) {
		t.Fatalf("want missing_code, got %v", err)
	}
	for _, err := range []error{ErrSuperseded, ErrStateMismatch, ErrExpired, ErrMissingRedirectURL, pe} {
		if RejectMessage(err) == "" {
			t.Fatal("empty message")
		}
	}
}

// ---------------- a fake provider ----------------

type fakeProvider struct {
	srv         *httptest.Server
	mu          sync.Mutex
	codes       map[string]string // code -> challenge
	access      string
	refresh     string
	refreshes   int
	failRefresh bool
	expiresIn   int
	scope       string
	apiCalls    atomic.Int32
}

func newFake(t *testing.T) *fakeProvider {
	f := &fakeProvider{codes: map[string]string{}, access: "at-1", refresh: "rt-1", expiresIn: 3600, scope: "read write"}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := "code-" + q.Get("state")[:6]
		f.mu.Lock()
		f.codes[code] = q.Get("code_challenge")
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			ch, ok := f.codes[r.Form.Get("code")]
			if !ok || ChallengeFor(r.Form.Get("code_verifier")) != ch || r.Form.Get("client_id") != "cid" {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"invalid_grant","error_description":"bad code or verifier"}`)
				return
			}
			delete(f.codes, r.Form.Get("code"))
		case "refresh_token":
			if f.failRefresh || r.Form.Get("refresh_token") != f.refresh {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
				return
			}
			f.refreshes++
			f.access = fmt.Sprintf("at-%d", f.refreshes+1)
		default:
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": f.access, "refresh_token": f.refresh, "expires_in": f.expiresIn, "scope": f.scope, "token_type": "Bearer"})
	})
	mux.HandleFunc("/api/me", func(w http.ResponseWriter, r *http.Request) {
		f.apiCalls.Add(1)
		f.mu.Lock()
		ok := r.Header.Get("Authorization") == "Bearer "+f.access
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"unauthorized"}`)
			return
		}
		io.WriteString(w, `{"name":"luke@example.com"}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type recorder struct {
	mu     sync.Mutex
	events []string
	asks   int
	answer func(title string) (bool, bool)
	opened chan string
}

func (r *recorder) host() Host {
	return Host{
		Emit: func(ev string, d any) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if ev == "toast" {
				ev += ":" + d.(map[string]string)["text"]
			}
			r.events = append(r.events, ev)
		},
		OpenURL: func(u string) {
			if r.opened != nil {
				r.opened <- u
			}
		},
		Confirm: func(title, detail string, always bool) (bool, bool) {
			r.mu.Lock()
			r.asks++
			fn := r.answer
			r.mu.Unlock()
			if fn == nil {
				return false, false
			}
			return fn(title)
		},
	}
}

func (r *recorder) has(prefix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

func testManager(t *testing.T, f *fakeProvider, rec *recorder) *Manager {
	who := func(c *call) (string, error) {
		var r struct{ Name string }
		err := c.get(f.srv.URL+"/api/me", &r)
		return r.Name, err
	}
	conn := &Connector{ID: "fake", Name: "Fake", Auth: AuthOAuth, AuthURL: f.srv.URL + "/authorize", TokenURL: f.srv.URL + "/token",
		BaseScopes: []string{"read"}, Groups: []string{"fake"}, AnyPort: true, WhoAmI: who}
	tools := []*Tool{
		{Name: "fake_me", Connector: "fake", Group: "fake", Risk: Read, Scopes: []string{"read"}, Run: func(c *call) (string, error) { return who(c) }},
		{Name: "fake_post", Connector: "fake", Group: "fake", Risk: Write, Scopes: []string{"write"},
			Ask: func(a args) (string, string) { return "Post it?", a.s("text") },
			Run: func(c *call) (string, error) { return "posted " + c.a.s("text"), nil }},
		{Name: "fake_admin", Connector: "fake", Group: "fake", Risk: Read, Scopes: []string{"admin"}, Run: func(c *call) (string, error) { return "admin", nil }},
	}
	m := NewWith(t.TempDir(), &MemSecrets{}, rec.host(), []*Connector{conn}, tools)
	m.Port = 0
	t.Cleanup(m.Close)
	return m
}

// connectFake runs the whole browser sign-in: Connect opens the "browser", which
// follows the provider's redirect back to Blaze's loopback server.
func connectFake(t *testing.T, m *Manager, rec *recorder) {
	t.Helper()
	if err := m.SetField("fake", "client_id", "cid"); err != nil {
		t.Fatal(err)
	}
	rec.opened = make(chan string, 1)
	r := m.Connect("fake")
	if r.Status != "waiting" {
		t.Fatalf("connect: %+v", r)
	}
	u := <-rec.opened
	pu, _ := url.Parse(u)
	if pu.Query().Get("code_challenge_method") != "S256" || pu.Query().Get("state") == "" {
		t.Fatalf("auth url missing PKCE/state: %s", u)
	}
	resp, err := http.Get(u) // the "browser"
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Connected to Fake") {
		t.Fatalf("callback page: %s", body)
	}
}

func TestOAuthLoopbackFlow(t *testing.T) {
	f, rec := newFake(t), &recorder{}
	m := testManager(t, f, rec)
	if m.Views().Connectors[0].Status != "needs_setup" {
		t.Fatal("should need setup before a client id")
	}
	connectFake(t, m, rec)
	v := m.Views().Connectors[0]
	if v.Status != "connected" || v.Account != "luke@example.com" {
		t.Fatalf("view after connect: %+v", v)
	}
	if !rec.has("toast:Finishing Fake setup") || !rec.has("toast:Fake is now connected") {
		t.Fatalf("toasts: %v", rec.events)
	}
	if tok := m.Store().Token("fake"); tok == nil || tok.RefreshToken != "rt-1" {
		t.Fatal("token not stored in secrets")
	}
	// Nothing secret in the plain file.
	b, _ := os.ReadFile(m.Store().path)
	if strings.Contains(string(b), "at-1") || strings.Contains(string(b), "rt-1") {
		t.Fatal("token leaked into connectors.json")
	}
	// A second callback with the same state is refused.
	if _, _, ok := m.HandleCallback(cb("whatever", "x")); ok {
		t.Fatal("forged callback accepted")
	}
	// Superseded: two Connects, the first browser tab loses.
	rec.opened = make(chan string, 2)
	m.Connect("fake")
	old := <-rec.opened
	m.Connect("fake")
	newer := <-rec.opened
	resp, _ := http.Get(old)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "newer sign-in") {
		t.Fatalf("superseded page: %s", body)
	}
	resp, _ = http.Get(newer)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Connected") {
		t.Fatalf("newer attempt should win: %s", body)
	}
}

func TestBlazeSchemeRouting(t *testing.T) {
	f, rec := newFake(t), &recorder{}
	m := testManager(t, f, rec)
	m.cat["fake"].CustomScheme = true
	_ = m.SetField("fake", "client_id", "cid")
	_ = m.SetRedirect("fake", "scheme")
	rec.opened = make(chan string, 1)
	m.Connect("fake")
	u := <-rec.opened
	if !strings.Contains(u, url.QueryEscape(SchemeCallback)) {
		t.Fatalf("redirect should be blaze://: %s", u)
	}
	// The provider redirects to blaze://…; the OS hands it to Blaze.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	if !strings.HasPrefix(loc, "blaze://connector/oauth_callback?") || !m.HandleURL(loc) {
		t.Fatalf("not routed: %s", loc)
	}
	if m.Views().Connectors[0].Status != "connected" {
		t.Fatal("scheme callback didn't connect")
	}
	if m.HandleURL("blaze://something/else") || m.HandleURL("https://x") {
		t.Fatal("routed a URL that isn't the callback")
	}
}

func TestRefreshAndReauth(t *testing.T) {
	f, rec := newFake(t), &recorder{}
	m := testManager(t, f, rec)
	connectFake(t, m, rec)
	ctx := context.Background()
	// Expired access token -> refreshed before the call.
	tok := m.Store().Token("fake")
	tok.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
	_ = m.Store().SetToken("fake", tok)
	if out := m.Run(ctx, "fake_me", "{}"); out != "luke@example.com" {
		t.Fatalf("after refresh: %s", out)
	}
	if f.refreshes != 1 || m.Store().Token("fake").AccessToken != "at-2" || m.Store().Token("fake").RefreshToken != "rt-1" {
		t.Fatalf("refresh didn't store new token (refreshes=%d)", f.refreshes)
	}
	// A 401 mid-life (revoked at the provider) -> one refresh and a retry.
	f.mu.Lock()
	f.access = "rotated"
	f.mu.Unlock()
	if out := m.Run(ctx, "fake_me", "{}"); out != "luke@example.com" {
		t.Fatalf("after 401 retry: %s", out)
	}
	// Refresh refused -> expired, the model is told, the UI gets a Reconnect card.
	f.mu.Lock()
	f.failRefresh = true
	f.access = "rotated-again"
	f.mu.Unlock()
	out := m.Run(ctx, "fake_me", "{}")
	if !strings.Contains(out, "expired") || !rec.has("connector_reauth") {
		t.Fatalf("want reauth, got %q / %v", out, rec.events)
	}
	if m.Views().Connectors[0].Status != "expired" {
		t.Fatal("status should be expired")
	}
	before := f.apiCalls.Load()
	if out := m.Run(ctx, "fake_me", "{}"); !strings.Contains(out, "expired") || f.apiCalls.Load() != before {
		t.Fatal("an expired connector must not call the API")
	}
	// Reconnect clears it.
	f.mu.Lock()
	f.failRefresh = false
	f.mu.Unlock()
	connectFake(t, m, rec)
	if m.Views().Connectors[0].Status != "connected" {
		t.Fatal("reconnect should clear expired")
	}
}

func TestIncrementalConsent(t *testing.T) {
	f, rec := newFake(t), &recorder{}
	m := testManager(t, f, rec)
	connectFake(t, m, rec) // granted "read write"
	out := m.Run(context.Background(), "fake_admin", "{}")
	if !strings.Contains(out, "permission") || !rec.has("connector_consent") {
		t.Fatalf("want consent request, got %q", out)
	}
	// The grant re-runs OAuth with the extra scope added to what's granted.
	rec.opened = make(chan string, 1)
	m.GrantScopes("fake", "fake_admin")
	u, _ := url.Parse(<-rec.opened)
	sc := u.Query().Get("scope")
	for _, s := range []string{"read", "write", "admin"} {
		if !strings.Contains(sc, s) {
			t.Fatalf("scope %q missing from %q", s, sc)
		}
	}
	m.CancelConnect("fake")
}

func TestApprovalPersistence(t *testing.T) {
	f, rec := newFake(t), &recorder{}
	m := testManager(t, f, rec)
	connectFake(t, m, rec)
	ctx := context.Background()
	// Declined: nothing runs.
	if out := m.Run(ctx, "fake_post", `{"text":"hi"}`); !strings.Contains(out, "declined") || rec.asks != 1 {
		t.Fatalf("decline: %q asks=%d", out, rec.asks)
	}
	// Allow once: runs, still asks next time.
	rec.answer = func(string) (bool, bool) { return true, false }
	if out := m.Run(ctx, "fake_post", `{"text":"a"}`); out != "posted a" {
		t.Fatal(out)
	}
	m.Run(ctx, "fake_post", `{"text":"b"}`)
	if rec.asks != 3 {
		t.Fatalf("allow once should ask again, asks=%d", rec.asks)
	}
	// Auto-approve never skips the card for a write tool.
	m.host.Yolo = func() bool { return true }
	m.Run(ctx, "fake_post", `{"text":"c"}`)
	if rec.asks != 4 {
		t.Fatal("write tools must ask even with auto-approve")
	}
	// Always allow: persisted per connector+tool, survives a restart.
	rec.answer = func(string) (bool, bool) { return true, true }
	m.Run(ctx, "fake_post", `{"text":"d"}`)
	m2 := NewWith(filepathDir(m.Store().path), m.Store().sec, rec.host(), m.conns, BuiltinToolsFrom(m))
	if got := m2.Store().Conn("fake").Modes["fake_post"]; got != "always" {
		t.Fatalf("mode not persisted: %q", got)
	}
	rec.answer = nil
	if out := m2.Run(ctx, "fake_post", `{"text":"e"}`); out != "posted e" || rec.asks != 5 {
		t.Fatalf("always allow should not ask: %q asks=%d", out, rec.asks)
	}
	// Off: the tool leaves the model's list and refuses to run.
	_ = m2.SetToolMode("fake", "fake_post", "off")
	m2.markLoaded("fake")
	for _, tl := range m2.Tools() {
		if tl.Function.Name == "fake_post" {
			t.Fatal("an off tool must not be offered")
		}
	}
	if out := m2.Run(ctx, "fake_post", `{}`); !strings.Contains(out, "off") {
		t.Fatal(out)
	}
	if m2.SetToolMode("fake", "fake_post", "sometimes") == nil {
		t.Fatal("bad mode accepted")
	}
}

func filepathDir(p string) string { return p[:strings.LastIndex(p, string(os.PathSeparator))] }

// BuiltinToolsFrom reuses a manager's tools for a fresh manager (restart test).
func BuiltinToolsFrom(m *Manager) []*Tool {
	var out []*Tool
	for _, n := range m.order {
		out = append(out, m.tools[n])
	}
	return out
}

func TestLazyToolGroups(t *testing.T) {
	f, rec := newFake(t), &recorder{}
	m := testManager(t, f, rec)
	names := func() []string {
		var l []string
		for _, t := range m.Tools() {
			l = append(l, t.Function.Name)
		}
		return l
	}
	if l := names(); len(l) != 1 || l[0] != metaSuggest {
		t.Fatalf("nothing connected: only suggest_connector, got %v", l)
	}
	if out := m.Run(context.Background(), "fake_me", "{}"); !strings.Contains(out, "isn't connected") || !rec.has("connector_suggest") {
		t.Fatalf("not connected: %q", out)
	}
	connectFake(t, m, rec)
	if l := names(); len(l) != 2 {
		t.Fatalf("connected, not loaded: want 2 meta tools, got %v", l)
	}
	if out := m.Run(context.Background(), metaLoad, `{"group":"fake"}`); !strings.Contains(out, "fake_me") {
		t.Fatal(out)
	}
	if l := names(); len(l) != 5 {
		t.Fatalf("loaded: want 5, got %v", l)
	}
	_ = m.SetEnabled("fake", false)
	if l := names(); len(l) != 1 {
		t.Fatalf("disabled: want only suggest, got %v", l)
	}
	if !strings.Contains(m.PromptBlock(), "Turned off") {
		t.Fatal("prompt should say it's off")
	}
}

func TestSaveTokenAndDisconnect(t *testing.T) {
	f, rec := newFake(t), &recorder{}
	m := testManager(t, f, rec)
	m.cat["fake"].Auth = AuthToken
	if err := m.SaveToken("fake", "wrong"); err == nil {
		t.Fatal("a bad token must be refused")
	}
	if err := m.SaveToken("fake", "at-1"); err != nil {
		t.Fatal(err)
	}
	if v := m.Views().Connectors[0]; v.Status != "connected" || v.Account != "luke@example.com" {
		t.Fatalf("%+v", v)
	}
	m.Disconnect("fake")
	if m.Store().Token("fake") != nil || m.Views().Connectors[0].Status == "connected" {
		t.Fatal("disconnect left the token")
	}
}

// ---------------- MCP ----------------

// fakeMCP is a tiny MCP server: it speaks JSON-RPC over Streamable HTTP (JSON or
// SSE replies) and, on /sse, the older HTTP+SSE transport.
type fakeMCP struct {
	srv      *httptest.Server
	sseReply bool
	session  string
	expire   atomic.Bool
	auth     string
	streams  sync.Map // session -> chan []byte
}

func mcpHandle(m rpcMsg) *rpcMsg {
	if len(m.ID) == 0 {
		return nil
	}
	r := &rpcMsg{JSONRPC: "2.0", ID: m.ID}
	switch m.Method {
	case "initialize":
		r.Result = json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}`)
	case "tools/list":
		var p struct{ Cursor string }
		_ = json.Unmarshal(m.Params, &p)
		if p.Cursor == "" {
			r.Result = json.RawMessage(`{"tools":[{"name":"echo","description":"Echo text","inputSchema":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]},"annotations":{"readOnlyHint":true}}],"nextCursor":"p2"}`)
		} else {
			r.Result = json.RawMessage(`{"tools":[{"name":"wipe.all","description":"Delete everything","inputSchema":{"type":"object"},"annotations":{"destructiveHint":true}}]}`)
		}
	case "tools/call":
		var p struct {
			Name      string
			Arguments map[string]any
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.Name == "echo" {
			b, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo: " + fmt.Sprint(p.Arguments["text"])}}})
			r.Result = b
		} else {
			r.Result = json.RawMessage(`{"content":[{"type":"text","text":"wiped"}],"isError":true}`)
		}
	default:
		r.Error = &rpcError{Code: -32601, Message: "no such method"}
	}
	return r
}

func newFakeMCP(t *testing.T) *fakeMCP {
	f := &fakeMCP{session: "s-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if f.auth != "" && r.Header.Get("Authorization") != f.auth {
			w.WriteHeader(401)
			return
		}
		if r.Method == "DELETE" {
			return
		}
		var m rpcMsg
		_ = json.NewDecoder(r.Body).Decode(&m)
		if m.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", f.session)
		} else if f.expire.Swap(false) || r.Header.Get("Mcp-Session-Id") != f.session {
			w.WriteHeader(404)
			return
		}
		reply := mcpHandle(m)
		if reply == nil {
			w.WriteHeader(202)
			return
		}
		b, _ := json.Marshal(reply)
		if f.sseReply {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, ": keepalive\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\nevent: message\ndata: %s\n\n", b)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		ch := make(chan []byte, 8)
		f.streams.Store("x", ch)
		fmt.Fprintf(w, "event: endpoint\ndata: /messages?sid=x\n\n")
		fl.Flush()
		for {
			select {
			case b := <-ch:
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
				fl.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		v, ok := f.streams.Load(r.URL.Query().Get("sid"))
		if !ok {
			w.WriteHeader(404)
			return
		}
		var m rpcMsg
		_ = json.NewDecoder(r.Body).Decode(&m)
		w.WriteHeader(202)
		if reply := mcpHandle(m); reply != nil {
			b, _ := json.Marshal(reply)
			v.(chan []byte) <- b
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func mcpManager(t *testing.T, rec *recorder) *Manager {
	m := NewWith(t.TempDir(), &MemSecrets{}, rec.host(), nil, nil)
	t.Cleanup(m.Close)
	return m
}

func checkMCP(t *testing.T, m *Manager, rec *recorder, v MCPView) {
	t.Helper()
	if v.Status != "ready" || len(v.Tools) != 2 {
		t.Fatalf("server view: %+v", v)
	}
	if v.Tools[0].Risk != Read || v.Tools[1].Risk != Write {
		t.Fatalf("annotations not mapped to risk: %+v", v.Tools)
	}
	ctx := context.Background()
	if out := m.Run(ctx, metaLoad, `{"group":"mcp:`+v.ID+`"}`); !strings.Contains(out, "mcp_"+v.ID+"_echo") || !strings.Contains(out, "mcp_"+v.ID+"_wipe_all") {
		t.Fatal(out)
	}
	if out := m.Run(ctx, "mcp_"+v.ID+"_echo", `{"text":"hello"}`); out != "echo: hello" {
		t.Fatalf("echo: %q", out)
	}
	asks := rec.asks
	if out := m.Run(ctx, "mcp_"+v.ID+"_wipe_all", `{}`); !strings.Contains(out, "declined") || rec.asks != asks+1 {
		t.Fatalf("destructive MCP tool must ask: %q", out)
	}
	rec.answer = func(string) (bool, bool) { return true, false }
	if out := m.Run(ctx, "mcp_"+v.ID+"_wipe_all", `{}`); out != "Tool error: wiped" {
		t.Fatalf("isError: %q", out)
	}
	rec.answer = nil
}

func TestMCPStreamableHTTP(t *testing.T) {
	for _, sse := range []bool{false, true} {
		f, rec := newFakeMCP(t), &recorder{}
		f.sseReply = sse
		f.auth = "Bearer sekret"
		m := mcpManager(t, rec)
		v, err := m.AddMCP(MCPSpec{Name: "Fake Tools", Transport: "http", URL: f.srv.URL + "/mcp", Token: "sekret"})
		if err != nil {
			t.Fatal(err)
		}
		checkMCP(t, m, rec, v)
		// The server forgets the session: Blaze re-initializes and retries once.
		f.expire.Store(true)
		if out := m.Run(context.Background(), "mcp_"+v.ID+"_echo", `{"text":"again"}`); out != "echo: again" {
			t.Fatalf("session recovery: %q", out)
		}
		b, _ := os.ReadFile(m.Store().path)
		if strings.Contains(string(b), "sekret") {
			t.Fatal("MCP token leaked into connectors.json")
		}
		// Wrong token -> a plain error on the server, not a crash.
		_ = m.Store().SetMCPToken(v.ID, "nope")
		v2, err := m.RefreshMCP(context.Background(), v.ID)
		if err == nil || v2.Status != "error" {
			t.Fatal("bad token should mark the server as error")
		}
	}
}

func TestMCPLegacySSE(t *testing.T) {
	f, rec := newFakeMCP(t), &recorder{}
	m := mcpManager(t, rec)
	v, err := m.AddMCP(MCPSpec{Name: "old", Transport: "sse", URL: f.srv.URL + "/sse"})
	if err != nil {
		t.Fatal(err)
	}
	checkMCP(t, m, rec, v)
}

// The test binary doubles as a stdio MCP server.
func TestMain(m *testing.M) {
	if os.Getenv("BLAZE_FAKE_MCP") == "1" {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			var msg rpcMsg
			if json.Unmarshal(sc.Bytes(), &msg) != nil {
				continue
			}
			if r := mcpHandle(msg); r != nil {
				b, _ := json.Marshal(r)
				os.Stdout.Write(append(b, '\n'))
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestMCPStdio(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	t.Setenv("BLAZE_FAKE_MCP", "1")
	rec := &recorder{}
	m := mcpManager(t, rec)
	v, err := m.AddMCP(MCPSpec{Name: "local", Transport: "stdio", Command: exe, Args: "-test.run=none"})
	if err != nil {
		t.Fatal(err)
	}
	checkMCP(t, m, rec, v)
	_ = m.SetMCPEnabled(v.ID, false)
	if out := m.Run(context.Background(), "mcp_"+v.ID+"_echo", `{"text":"x"}`); !strings.Contains(out, "off") {
		t.Fatal(out)
	}
	if _, err := m.AddMCP(MCPSpec{Transport: "http", URL: "http://example.com/mcp"}); err == nil {
		t.Fatal("plain http to a remote host must be refused")
	}
}

func TestSplitArgsAndNames(t *testing.T) {
	got := splitArgs(`-y @modelcontextprotocol/server-filesystem "C:\My Files" ''`)
	if len(got) != 4 || got[2] != `C:\My Files` || got[3] != "" {
		t.Fatalf("%q", got)
	}
	if n := mcpToolName("srv", "a.b c"); n != "mcp_srv_a_b_c" {
		t.Fatal(n)
	}
	if len(mcpToolName("srv", strings.Repeat("x", 100))) != 64 {
		t.Fatal("names must fit 64 chars")
	}
}

func TestBuiltinCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, tl := range BuiltinTools() {
		if seen[tl.Name] || tl.Run == nil || tl.Desc == "" || byID(tl.Connector) == nil {
			t.Fatalf("bad tool %s", tl.Name)
		}
		if tl.Risk == Write && tl.Ask == nil {
			t.Fatalf("%s writes but has no approval card text", tl.Name)
		}
		seen[tl.Name] = true
	}
	for _, c := range Catalog {
		if c.Console == "" || len(c.Steps) == 0 || len(c.Fields) == 0 {
			t.Fatalf("%s needs setup steps", c.ID)
		}
	}
}

// Real tool code against a fake API: Gmail send builds a raw message and asks first.
func TestGmailSendThroughGate(t *testing.T) {
	var got map[string]string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gmail/v1/users/me/messages/send" {
			_ = json.NewDecoder(r.Body).Decode(&got)
			io.WriteString(w, `{"id":"m1"}`)
			return
		}
		w.WriteHeader(404)
	}))
	defer api.Close()
	old := gmailAPI
	gmailAPI = api.URL + "/gmail/v1/users/me"
	defer func() { gmailAPI = old }()
	rec := &recorder{}
	m := NewWith(t.TempDir(), &MemSecrets{}, rec.host(), Catalog, BuiltinTools())
	_ = m.Store().SetToken("google", &Token{AccessToken: "x"})
	_ = m.Store().Update("google", func(s *ConnState) {
		s.Connected = true
		s.Scopes = []string{gScope + "gmail.readonly", gScope + "gmail.send"}
	})
	var title string
	rec.answer = func(t string) (bool, bool) { title = t; return true, false }
	out := m.Run(context.Background(), "gmail_send", `{"to":"a@b.c","subject":"Hi ✓","body":"line1\nline2"}`)
	if !strings.Contains(out, "Email sent") || !strings.Contains(title, "Send") {
		t.Fatalf("%q %q", out, title)
	}
	raw := b64url(got["raw"])
	if !strings.Contains(raw, "To: a@b.c") || !strings.Contains(raw, "line1\r\nline2") || !strings.Contains(raw, "=?utf-8?q?") {
		t.Fatalf("raw message: %q", raw)
	}
	// Missing drafts scope -> incremental consent, never a silent re-grant.
	out = m.Run(context.Background(), "gmail_draft", `{"to":"a@b.c","subject":"s","body":"b"}`)
	if !strings.Contains(out, "permission") || !rec.has("connector_consent") {
		t.Fatalf("draft without compose scope: %q", out)
	}
}
