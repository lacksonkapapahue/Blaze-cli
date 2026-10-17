package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

// isolate gives a test its own BLAZE_HOME, no own keys, a fresh router health and
// a session token it controls.
func isolate(t *testing.T, tok *string) {
	t.Helper()
	t.Setenv("BLAZE_HOME", t.TempDir())
	for _, p := range config.Providers {
		if p.Env != "" {
			t.Setenv(p.Env, "")
		}
	}
	hmu.Lock()
	health_ = map[string]*health{}
	hmu.Unlock()
	old := SessionToken
	SessionToken = func() string { return *tok }
	oldWait := blazeRetryWait
	blazeRetryWait = func(int, int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { SessionToken = old; blazeRetryWait = oldWait })
}

func gateway(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("BLAZE_GATEWAY_URL", srv.URL+"/v1")
}

func TestBlazeRequestShape(t *testing.T) {
	tok := "bz_first"
	isolate(t, &tok)
	var bodies []map[string]any
	var auths []string
	gateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		auths = append(auths, r.Header.Get("Authorization"))
		w.Header().Set("X-Blaze-Provider", "groq")
		w.Header().Set("X-Blaze-Model", "llama-3.3-70b")
		w.Header().Set("X-Blaze-Task", "agent")
		w.Header().Set("X-RateLimit-Remaining-Requests", "99")
		sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
	})
	cfg := &config.Config{NoKeyless: true} // no network: the keyless model stays out

	// With tools: agent, blaze-auto, the session token as Bearer.
	r, err := Complete(context.Background(), cfg, []Message{{Role: "user", Content: "hi"}}, []Tool{{Type: "function"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auths[0] != "Bearer bz_first" || bodies[0]["model"] != "blaze-auto" || bodies[0]["blaze_task"] != "agent" || bodies[0]["stream"] != true {
		t.Fatalf("request = %v auth %q", bodies[0], auths[0])
	}
	if r.Route != "Blaze · groq/llama-3.3-70b" || r.Served.Remaining != "99" || r.Served.Task != "agent" {
		t.Fatalf("route = %q served %+v", r.Route, r.Served)
	}

	// No tools: code cue → code, else chat. The token is read again (a new sign-in applies).
	tok = "bz_second"
	if _, err := Complete(context.Background(), cfg, []Message{{Role: "user", Content: "fix this python bug"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Complete(context.Background(), cfg, []Message{{Role: "user", Content: "how are you?"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if auths[1] != "Bearer bz_second" || bodies[1]["blaze_task"] != "code" || bodies[2]["blaze_task"] != "chat" {
		t.Fatalf("auth %q tasks %v %v", auths[1], bodies[1]["blaze_task"], bodies[2]["blaze_task"])
	}

	// A picked blaze-<task> model names its task itself: no blaze_task.
	if err := SaveCloudPrefs(CloudPrefs{Model: "blaze-code"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Complete(context.Background(), cfg, []Message{{Role: "user", Content: "hi"}}, []Tool{{Type: "function"}}, nil); err != nil {
		t.Fatal(err)
	}
	if bodies[3]["model"] != "blaze-code" || bodies[3]["blaze_task"] != nil {
		t.Fatalf("picked model body = %v", bodies[3])
	}

	// Signed out: no call, the sign-in line.
	tok = ""
	_, err = Complete(context.Background(), cfg, []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil || err.Error() != MsgSignedOut || len(bodies) != 4 {
		t.Fatalf("signed out: %v (%d calls)", err, len(bodies))
	}
}

func TestBlazeTask(t *testing.T) {
	long := strings.Repeat("a", longChars+1)
	cases := []struct {
		msgs  []Message
		tools []Tool
		want  string
	}{
		{[]Message{{Role: "user", Content: "hello"}}, nil, "chat"},
		{[]Message{{Role: "user", Content: "write a function"}}, nil, "code"},
		{[]Message{{Role: "user", Content: "```x```"}}, nil, "code"},
		{[]Message{{Role: "user", Content: "hello"}}, []Tool{{}}, "agent"},
		{[]Message{{Role: "user", Content: long}}, []Tool{{}}, "long"},
		{[]Message{{Role: "user", Content: "debug it"}, {Role: "assistant", Content: "ok"}, {Role: "user", Content: "thanks"}}, nil, "chat"},
	}
	for i, c := range cases {
		if got := BlazeTask(c.msgs, c.tools); got != c.want {
			t.Errorf("case %d: %q, want %q", i, got, c.want)
		}
	}
}

func TestGatewayErrorMapping(t *testing.T) {
	type tc struct {
		status   int
		body     string
		retry    string
		want     string
		expired  bool
		retryN   int
		wantCode string
	}
	cases := []tc{
		{401, `{"error":{"message":"Your Blaze session has expired. Sign in again.","type":"invalid_api_key","code":"invalid_api_key"}}`, "", MsgExpired, true, 0, "invalid_api_key"},
		{429, `{"error":{"message":"You've used today's message allowance on the free plan.","type":"quota_exceeded","code":"quota_exceeded"}}`, "11520", "You've used today's free Blaze allowance. It resets in 3h 12m.", false, 11520, "quota_exceeded"},
		{429, `{"error":{"message":"x","type":"quota_exceeded","code":"quota_exceeded"}}`, "", "You've used today's free Blaze allowance. It resets at 00:00 UTC.", false, 0, "quota_exceeded"},
		{503, `{"error":{"message":"Every model for 'chat' is busy right now.","type":"blaze_models_busy","code":"blaze_models_busy"}}`, "42", "Blaze's models are all busy right now. Try again in about 42s.", false, 42, "blaze_models_busy"},
		{403, `{"error":{"message":"You're on the Blaze waiting list.","type":"waitlisted","code":"waitlisted"}}`, "", MsgWaitlisted, false, 0, "waitlisted"},
		{403, `{"error":{"message":"no","type":"access_rejected","code":"access_rejected"}}`, "", MsgRejected, false, 0, "access_rejected"},
		{500, `oops`, "", "Blaze Cloud is having trouble on its side right now. Try again in a moment.", false, 0, ""},
		{400, `{"error":{"message":"bad messages","type":"invalid_request_error","code":"invalid_request_error"}}`, "", "Blaze Cloud couldn't answer that (400): bad messages", false, 0, "invalid_request_error"},
	}
	for _, c := range cases {
		h := http.Header{}
		if c.retry != "" {
			h.Set("Retry-After", c.retry)
		}
		ge := gatewayError(c.status, h, []byte(c.body))
		if ge.Error() != c.want || ge.Expired() != c.expired || ge.RetryAfter != c.retryN || ge.Code != c.wantCode {
			t.Errorf("%d %s: %q expired=%v retry=%d code=%q", c.status, c.wantCode, ge.Error(), ge.Expired(), ge.RetryAfter, ge.Code)
		}
	}
	if (&GatewayError{Err: errors.New("dial")}).Error() != MsgOffline {
		t.Error("network error line")
	}
	for s, want := range map[int]string{45: "45s", 60: "1m", 61: "2m", 3600: "1h 0m", 3661: "1h 2m", 7199: "2h 0m", 86399: "24h 0m"} {
		if got := FormatWait(s); got != want {
			t.Errorf("FormatWait(%d) = %q, want %q", s, got, want)
		}
	}
}

func TestGatewayErrorsThroughComplete(t *testing.T) {
	tok := "bz_x"
	isolate(t, &tok)
	var status atomic.Int32
	var calls atomic.Int32
	gateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch status.Load() {
		case 401:
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"expired","type":"invalid_api_key","code":"invalid_api_key"}}`)
		case 429:
			w.Header().Set("Retry-After", "3700")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"allowance","type":"quota_exceeded","code":"quota_exceeded"}}`)
		case 502:
			w.WriteHeader(502)
		default:
			sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
		}
	})
	cfg := &config.Config{NoKeyless: true}
	ask := func() error {
		_, err := Complete(context.Background(), cfg, []Message{{Role: "user", Content: "hi"}}, nil, nil)
		return err
	}

	status.Store(401)
	err := ask()
	ge, ok := AsGateway(err)
	if !ok || !ge.Expired() || err.Error() != MsgExpired || calls.Load() != 1 {
		t.Fatalf("401: %v (calls %d)", err, calls.Load())
	}
	// Signing in again (a new token) isn't held back by the old token's cool-down.
	tok = "bz_y"
	status.Store(200)
	if err := ask(); err != nil {
		t.Fatalf("after new sign-in: %v", err)
	}

	// The daily allowance: no quiet retry, said with the time left.
	calls.Store(0)
	status.Store(429)
	err = ask()
	if ge, ok = AsGateway(err); !ok || !ge.Quota() || !strings.Contains(err.Error(), "resets in 1h 2m") || calls.Load() != 1 {
		t.Fatalf("429: %v (calls %d)", err, calls.Load())
	}

	// 5xx is retried once quietly, and never reads as an expired sign-in.
	tok = "bz_z"
	calls.Store(0)
	status.Store(502)
	err = ask()
	if ge, ok = AsGateway(err); !ok || ge.Expired() || calls.Load() != 2 {
		t.Fatalf("502: %v (calls %d)", err, calls.Load())
	}
}

func TestRouteChainOrder(t *testing.T) {
	tok := ""
	isolate(t, &tok)
	t.Setenv("GROQ_API_KEY", "g")
	cfg := &config.Config{Chain: []config.Entry{{Provider: "groq", Model: "llama"}}}

	// Signed out: the user's own chain plus the keyless last resort; no Blaze.
	r := RouteChain(cfg)
	if len(r) != 2 || r[0].Provider != "groq" || r[1].Provider != "pollinations" {
		t.Fatalf("signed out: %v", r)
	}

	// Signed in: Blaze first, own keys after, the keyless model dropped.
	tok = "bz_1"
	r = RouteChain(cfg)
	if len(r) != 2 || r[0] != (config.Entry{Provider: BlazeID, Model: BlazeModel}) || r[1].Provider != "groq" {
		t.Fatalf("signed in: %v", r)
	}

	// A one-run --model goes before Blaze.
	cfg.Override = &config.Entry{Provider: "groq", Model: "other"}
	if r = RouteChain(cfg); r[0].Model != "other" || r[1].Provider != BlazeID {
		t.Fatalf("override: %v", r)
	}
	// --model blaze-code is the Blaze entry itself (no second one).
	cfg.Override = &config.Entry{Provider: BlazeID, Model: "blaze-code"}
	r = RouteChain(cfg)
	n := 0
	for _, e := range r {
		if e.Provider == BlazeID {
			n++
		}
	}
	if r[0].Model != "blaze-code" || n != 1 {
		t.Fatalf("blaze override: %v", r)
	}
	cfg.Override = nil

	// An own model picked to go first: it, then Blaze, then the rest.
	cfg.Default = &config.Entry{Provider: "groq", Model: "llama"}
	_ = SaveCloudPrefs(CloudPrefs{OwnFirst: true})
	if r = RouteChain(cfg); r[0].Provider != "groq" || r[1].Provider != BlazeID {
		t.Fatalf("own first: %v", r)
	}
	// Without own_first the picked model stays behind Blaze.
	_ = SaveCloudPrefs(CloudPrefs{})
	if r = RouteChain(cfg); r[0].Provider != BlazeID {
		t.Fatalf("default without own_first: %v", r)
	}
}

func TestOwnKeysFollowBlaze(t *testing.T) {
	tok := "bz_1"
	isolate(t, &tok)
	gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":{"message":"busy","type":"blaze_models_busy","code":"blaze_models_busy"}}`)
	})
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":{"message":"down"}}`)
	}))
	defer own.Close()
	// Ollama needs no key: point it at the failing "own" server.
	cfg := &config.Config{Chain: []config.Entry{{Provider: "ollama", Model: "m"}}}
	old := config.Providers
	config.Providers = append([]config.Provider{}, old...)
	for i := range config.Providers {
		if config.Providers[i].ID == "ollama" {
			config.Providers[i].BaseURL = own.URL
		}
	}
	defer func() { config.Providers = old }()

	_, err := Complete(context.Background(), cfg, []Message{{Role: "user", Content: "hi"}}, nil, nil)
	var ce *ChainError
	ge, ok := AsGateway(err)
	if !errors.As(err, &ce) || !ok || !ge.Busy() || !strings.Contains(err.Error(), "Blaze: Blaze's models are all busy") || !strings.Contains(err.Error(), "Ollama") {
		t.Fatalf("chain error = %v", err)
	}
	// Blaze now cools; the next turn still names its state.
	_, err = Complete(context.Background(), cfg, []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if ge, ok = AsGateway(err); !ok || !ge.Busy() {
		t.Fatalf("cooling: %v", err)
	}
}

func TestBlazeModelsList(t *testing.T) {
	tok := ""
	isolate(t, &tok)
	if ids, live := BlazeModels(context.Background()); live || len(ids) != 7 || ids[0] != "blaze-auto" {
		t.Fatalf("signed out: %v %v", ids, live)
	}
	tok = "bz_1"
	gateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer bz_1" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"object":"list","data":[{"id":"blaze-auto"},{"id":"blaze-code","description":"Programming"},{"id":"blaze-new","description":"New thing"}]}`)
	})
	ids, live := BlazeModels(context.Background())
	if !live || len(ids) != 3 || !IsBlazeModel("blaze-new") || BlazeModelDesc("blaze-new") != "New thing" {
		t.Fatalf("live: %v %v", ids, live)
	}
	modelsMu.Lock()
	modelsCache = nil
	modelsMu.Unlock()
}
