package connect

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ---------------- PKCE ----------------

// PKCE is one proof-key pair (RFC 7636, S256).
type PKCE struct{ Verifier, Challenge string }

func randB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewPKCE makes a 43-character verifier and its S256 challenge.
func NewPKCE() PKCE {
	v := randB64(32)
	return PKCE{v, ChallengeFor(v)}
}

// ChallengeFor is BASE64URL(SHA256(verifier)) without padding.
func ChallengeFor(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// ---------------- pending attempts ----------------

// Named rejections for a callback that can't finish a sign-in.
var (
	ErrMissingRedirectURL = errors.New("missing_redirect_url")
	ErrStateMismatch      = errors.New("state_mismatch")
	ErrExpired            = errors.New("expired")
	ErrSuperseded         = errors.New("superseded")
	ErrMissingCode        = errors.New("missing_code")
)

// ProviderError is an error= the provider put on the callback (the user said no, etc).
type ProviderError struct{ Code, Description string }

func (e *ProviderError) Error() string {
	if e.Description != "" {
		return e.Code + ": " + e.Description
	}
	return e.Code
}

// Attempt is one sign-in in flight, keyed by its OAuth state (the anti-CSRF value
// and the storage key at once).
type Attempt struct {
	State       string
	Connector   string
	PKCE        PKCE
	RedirectURI string
	Scopes      []string
	Created     time.Time
}

// Attempts is the pending-attempt table. A newer attempt for the same connector
// supersedes the older one; every attempt is single-use and expires.
type Attempts struct {
	mu         sync.Mutex
	byState    map[string]*Attempt
	latest     map[string]string    // connector -> state
	superseded map[string]time.Time // states that lost to a newer attempt
	TTL        time.Duration
	Now        func() time.Time
}

func NewAttempts() *Attempts {
	return &Attempts{byState: map[string]*Attempt{}, latest: map[string]string{}, superseded: map[string]time.Time{}, TTL: 10 * time.Minute, Now: time.Now}
}

// Begin records a new attempt and supersedes any earlier one for the connector.
func (a *Attempts) Begin(connector, redirectURI string, scopes []string) *Attempt {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.Now()
	for s, t := range a.superseded {
		if now.Sub(t) > a.TTL {
			delete(a.superseded, s)
		}
	}
	if prev, ok := a.latest[connector]; ok {
		delete(a.byState, prev)
		a.superseded[prev] = now
	}
	at := &Attempt{State: randB64(24), Connector: connector, PKCE: NewPKCE(), RedirectURI: redirectURI, Scopes: scopes, Created: now}
	a.byState[at.State] = at
	a.latest[connector] = at.State
	return at
}

// Cancel drops a connector's pending attempt.
func (a *Attempts) Cancel(connector string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.latest[connector]; ok {
		delete(a.byState, s)
		delete(a.latest, connector)
	}
}

// Pending reports whether a connector has an attempt waiting.
func (a *Attempts) Pending(connector string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.latest[connector]
	if !ok {
		return false
	}
	at := a.byState[s]
	return at != nil && a.Now().Sub(at.Created) <= a.TTL
}

// Count is the number of attempts still waiting.
func (a *Attempts) Count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, at := range a.byState {
		if a.Now().Sub(at.Created) <= a.TTL {
			n++
		}
	}
	return n
}

// Resolve matches a callback URL to its attempt and takes the attempt out of the
// table. It returns the authorization code, or a named rejection.
func (a *Attempts) Resolve(raw string) (*Attempt, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", ErrMissingRedirectURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", ErrMissingRedirectURL
	}
	q := u.Query()
	state := q.Get("state")
	a.mu.Lock()
	defer a.mu.Unlock()
	if state == "" {
		return nil, "", ErrStateMismatch
	}
	if _, ok := a.superseded[state]; ok {
		delete(a.superseded, state)
		return nil, "", ErrSuperseded
	}
	at := a.byState[state]
	if at == nil {
		return nil, "", ErrStateMismatch
	}
	delete(a.byState, state)
	if a.latest[at.Connector] == state {
		delete(a.latest, at.Connector)
	}
	if a.Now().Sub(at.Created) > a.TTL {
		return at, "", ErrExpired
	}
	if at.RedirectURI == "" {
		return at, "", ErrMissingRedirectURL
	}
	if e := q.Get("error"); e != "" {
		return at, "", &ProviderError{e, q.Get("error_description")}
	}
	code := q.Get("code")
	if code == "" {
		return at, "", ErrMissingCode
	}
	return at, code, nil
}

// RejectMessage is the plain sentence for a rejected callback.
func RejectMessage(err error) string {
	var pe *ProviderError
	switch {
	case errors.Is(err, ErrSuperseded):
		return "A newer sign-in for this connector replaced that one. Finish the newest browser tab."
	case errors.Is(err, ErrStateMismatch):
		return "That sign-in link doesn't match one Blaze started (state mismatch). Start Connect again."
	case errors.Is(err, ErrExpired):
		return "That sign-in took too long and expired. Start Connect again."
	case errors.Is(err, ErrMissingRedirectURL):
		return "The sign-in came back without its redirect URL. Start Connect again."
	case errors.Is(err, ErrMissingCode):
		return "The provider didn't send a sign-in code back."
	case errors.As(err, &pe):
		if pe.Code == "access_denied" {
			return "You declined access in the browser. Nothing was connected."
		}
		return "The provider said: " + pe.Error()
	}
	return err.Error()
}

// ---------------- token endpoint ----------------

// ErrReauth means the connection expired and refreshing failed: the user must Reconnect.
var ErrReauth = errors.New("reauth_required")

// TokenError is a refused token request (invalid_grant, bad client, ...).
type TokenError struct {
	Status      int
	Code, Desc  string
	Unreachable bool
}

func (e *TokenError) Error() string {
	if e.Code != "" {
		s := e.Code
		if e.Desc != "" {
			s += ": " + e.Desc
		}
		return s
	}
	return fmt.Sprintf("token endpoint answered HTTP %d", e.Status)
}

func postToken(ctx context.Context, hc *http.Client, tokenURL string, form url.Values) (*Token, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, &TokenError{Unreachable: true, Desc: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		AccessToken  string          `json:"access_token"`
		RefreshToken string          `json:"refresh_token"`
		TokenType    string          `json:"token_type"`
		Scope        string          `json:"scope"`
		ExpiresIn    json.RawMessage `json:"expires_in"`
		Error        string          `json:"error"`
		ErrorDesc    string          `json:"error_description"`
	}
	if json.Unmarshal(body, &r) != nil {
		// Some providers answer form-encoded.
		if v, e := url.ParseQuery(string(body)); e == nil {
			r.AccessToken, r.RefreshToken, r.TokenType, r.Scope = v.Get("access_token"), v.Get("refresh_token"), v.Get("token_type"), v.Get("scope")
			r.Error, r.ErrorDesc = v.Get("error"), v.Get("error_description")
			if ex := v.Get("expires_in"); ex != "" {
				r.ExpiresIn = json.RawMessage(ex)
			}
		}
	}
	if r.Error != "" || resp.StatusCode >= 400 || r.AccessToken == "" {
		return nil, &TokenError{Status: resp.StatusCode, Code: r.Error, Desc: r.ErrorDesc}
	}
	t := &Token{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, TokenType: r.TokenType, Scope: r.Scope}
	var secs float64
	if len(r.ExpiresIn) > 0 {
		s := strings.Trim(string(r.ExpiresIn), `"`)
		fmt.Sscan(s, &secs)
	}
	if secs > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(secs) * time.Second).UnixMilli()
	}
	return t, nil
}

// splitScopes reads "a b", "a,b" or "a, b".
func splitScopes(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' })
}

// ---------------- loopback server ----------------

// LoopbackPort is the fixed callback port (Spotify needs the exact redirect URI).
const LoopbackPort = 43117

// SchemeCallback is the custom-protocol redirect for providers that allow one.
const SchemeCallback = "blaze://connector/oauth_callback"

type loopback struct {
	srv  *http.Server
	port int
}

func startLoopback(port int, handle func(raw string) (title, msg string, ok bool)) (*loopback, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		full := "http://" + r.Host + r.URL.RequestURI()
		title, msg, ok := handle(full)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		mark := "#1F4BD8"
		if !ok {
			mark = "#111110"
		}
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Blaze</title><body style="font:15px/1.5 system-ui,sans-serif;background:#FBFBFA;color:#111110;display:grid;place-items:center;height:100vh;margin:0"><div style="max-width:440px;padding:32px;border:1px solid rgba(17,17,16,.18);background:#fff;box-shadow:inset 3px 0 0 %s"><div style="font:500 11px monospace;letter-spacing:.12em;text-transform:uppercase;color:%s">Blaze</div><h2 style="margin:8px 0 6px;font-weight:600">%s</h2><p style="margin:0;color:rgba(17,17,16,.6)">%s</p></div></body>`,
			mark, mark, htmlEsc(title), htmlEsc(msg))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return &loopback{srv: srv, port: ln.Addr().(*net.TCPAddr).Port}, nil
}

func (l *loopback) stop() {
	if l != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = l.srv.Shutdown(ctx)
	}
}

func htmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
