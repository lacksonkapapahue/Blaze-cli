// Package cloud talks to blaze-auth, Blaze's small Cloudflare Worker (cloud/ in this
// repo). It does two things:
//
//   - one-click connectors: the Worker holds each provider's client secret, so the
//     user just presses Connect. The browser signs in, the Worker swaps the code for
//     tokens, and hands them back to a 127.0.0.1 loopback here through a one-time
//     code bound to our PKCE verifier.
//   - Blaze accounts: email + password, or Sign in with Google / GitHub.
//
// No secret lives in the desktop binary. Tokens end up in the OS keychain.
package cloud

import (
	"bytes"
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
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultBase is the deployed Worker.
const DefaultBase = "https://blaze-auth.blaze-app.workers.dev"

// BaseURL is BLAZE_CLOUD_URL, else "cloud_url" in <dir>/cloud.json, else DefaultBase.
func BaseURL(dir string) string {
	if v := strings.TrimSpace(os.Getenv("BLAZE_CLOUD_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	var f struct {
		CloudURL string `json:"cloud_url"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cloud.json")); err == nil && json.Unmarshal(b, &f) == nil {
		if u, err := url.Parse(strings.TrimSpace(f.CloudURL)); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			return strings.TrimRight(u.String(), "/")
		}
	}
	return DefaultBase
}

// Client is one Worker base URL.
type Client struct {
	Base string
	HC   *http.Client
	// Timeout is how long the user gets to finish in the browser.
	Timeout time.Duration
	// Name labels the session at the service ("Blaze CLI"; default "Blaze Desktop").
	Name string
	// Paste optionally reads a callback URL when the browser cannot reach loopback.
	Paste func(ctx context.Context) <-chan string
}

func (c *Client) name() string {
	if c.Name != "" {
		return c.Name
	}
	return "Blaze Desktop"
}

func New(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HC: &http.Client{Timeout: 30 * time.Second}, Timeout: 10 * time.Minute}
}

// ---------------- errors ----------------

// ErrNotConfigured: the Worker has no client secret for that provider yet.
var ErrNotConfigured = errors.New("not_configured")

// ErrCancelled: the sign-in was cancelled or timed out before the browser came back.
var ErrCancelled = errors.New("cancelled")

// APIError is a refusal from the Worker ({"error": "..."}).
type APIError struct {
	Status int
	Code   string
	Retry  int
}

func (e *APIError) Error() string { return e.Code }

// authRefusals are the OAuth error codes that mean a refresh token or client
// is definitively no good (as opposed to a passing outage).
var authRefusals = map[string]bool{
	"invalid_grant":       true,
	"invalid_client":      true,
	"unauthorized_client": true,
	"unauthorized":        true,
	"invalid_token":       true,
}

// AuthFailure reports whether the Worker definitively refused the credentials:
// HTTP 401, or HTTP 400 with an OAuth refusal code (invalid_grant and kin).
// Rate limits (429), server errors (5xx) and anything else are not.
func (e *APIError) AuthFailure() bool {
	if e == nil {
		return false
	}
	switch e.Status {
	case http.StatusUnauthorized:
		return true
	case http.StatusBadRequest:
		return authRefusals[e.Code]
	}
	return false
}

// IsAuthFailure reports whether err is a definitive credential refusal from the
// Worker (see APIError.AuthFailure). Network errors, cancellation, timeouts,
// 429 and 5xx answers are all transient and report false.
func IsAuthFailure(err error) bool {
	if err == nil || errors.Is(err, ErrCancelled) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var un *Unreachable
	if errors.As(err, &un) {
		return false
	}
	var ae *APIError
	return errors.As(err, &ae) && ae.AuthFailure()
}

// Unreachable wraps a network failure (offline, DNS, TLS).
type Unreachable struct{ Err error }

func (e *Unreachable) Error() string {
	return "couldn't reach Blaze's sign-in service: " + e.Err.Error()
}
func (e *Unreachable) Unwrap() error { return e.Err }

// Message is a plain sentence for an error.
func Message(err error) string {
	var ae *APIError
	var un *Unreachable
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotConfigured):
		return "One-click sign-in isn't switched on for this yet. Use your own key instead."
	case errors.Is(err, ErrCancelled):
		return "Sign-in was cancelled."
	case errors.As(err, &un):
		return "Couldn't reach Blaze's sign-in service. Check your internet connection."
	case errors.As(err, &ae):
		switch ae.Code {
		case "invalid_credentials":
			return "That email and password don't match."
		case "email_taken":
			return "There's already a Blaze account with that email. Sign in instead."
		case "invalid_email":
			return "That doesn't look like an email address."
		case "invalid_password":
			return "Use a password of at least 8 characters."
		case "rate_limited":
			return "Too many tries. Wait a few minutes and try again."
		case "unauthorized":
			return "You're signed out. Sign in again."
		case "access_denied":
			return "You declined access in the browser. Nothing changed."
		}
		return "Sign-in failed (" + ae.Code + ")."
	}
	return err.Error()
}

// ---------------- types ----------------

type Providers struct {
	Login      map[string]bool `json:"login"`
	Connectors map[string]bool `json:"connectors"`
}

type Tokens struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token,omitempty"`
	TokenType    string  `json:"token_type,omitempty"`
	Scope        string  `json:"scope,omitempty"`
	ExpiresIn    float64 `json:"expires_in,omitempty"`
	Account      string  `json:"account,omitempty"`
}

type User struct {
	ID            string   `json:"id"`
	Email         string   `json:"email"`
	Name          string   `json:"name"`
	AvatarURL     string   `json:"avatar_url"`
	EmailVerified bool     `json:"email_verified"`
	HasPassword   bool     `json:"has_password"`
	Providers     []string `json:"providers"`
	CreatedAt     int64    `json:"created_at"`
}

// Redeemed is what /oauth/redeem returns: provider tokens, or a Blaze session.
type Redeemed struct {
	Kind     string   `json:"kind"` // tokens | session
	Provider string   `json:"provider"`
	Tokens   *Tokens  `json:"tokens,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	Token    string   `json:"token,omitempty"`
	User     *User    `json:"user,omitempty"`
}

// Session is a signed-in Blaze account.
type Session struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

// ---------------- HTTP ----------------

func (c *Client) do(ctx context.Context, method, path, bearer string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", strings.ReplaceAll(c.name(), " ", "-"))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ErrCancelled
		}
		return &Unreachable{err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Error      string `json:"error"`
			RetryAfter int    `json:"retry_after"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("http_%d", resp.StatusCode)
		}
		if e.Error == "not_configured" {
			return ErrNotConfigured
		}
		return &APIError{Status: resp.StatusCode, Code: e.Error, Retry: e.RetryAfter}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("unexpected answer from the sign-in service: %v", err)
		}
	}
	return nil
}

// Providers says which sign-ins and connectors the Worker has secrets for.
func (c *Client) Providers(ctx context.Context) (*Providers, error) {
	var p Providers
	if err := c.do(ctx, "GET", "/v1/providers", "", nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *Client) SignUp(ctx context.Context, name, email, password string) (*Session, error) {
	var s Session
	err := c.do(ctx, "POST", "/v1/auth/signup", "", map[string]string{"name": name, "email": email, "password": password, "client": c.name()}, &s)
	return &s, err
}

func (c *Client) SignIn(ctx context.Context, email, password string) (*Session, error) {
	var s Session
	err := c.do(ctx, "POST", "/v1/auth/login", "", map[string]string{"email": email, "password": password, "client": c.name()}, &s)
	return &s, err
}

func (c *Client) Me(ctx context.Context, token string) (*User, error) {
	var r struct {
		User User `json:"user"`
	}
	if err := c.do(ctx, "GET", "/v1/me", token, nil, &r); err != nil {
		return nil, err
	}
	return &r.User, nil
}

func (c *Client) Logout(ctx context.Context, token string) error {
	return c.do(ctx, "POST", "/v1/logout", token, map[string]string{}, nil)
}

func (c *Client) DeleteAccount(ctx context.Context, token string) error {
	return c.do(ctx, "POST", "/v1/account/delete", token, map[string]string{"confirm": "delete"}, nil)
}

// Refresh swaps a provider refresh token for new tokens (the Worker adds the secret).
func (c *Client) Refresh(ctx context.Context, provider, refreshToken string) (*Tokens, error) {
	var r Redeemed
	if err := c.do(ctx, "POST", "/oauth/"+url.PathEscape(provider)+"/refresh", "", map[string]string{"refresh_token": refreshToken}, &r); err != nil {
		return nil, err
	}
	if r.Tokens == nil || r.Tokens.AccessToken == "" {
		return nil, &APIError{Status: 502, Code: "no_token"}
	}
	return r.Tokens, nil
}

// ---------------- browser sign-in through the loopback ----------------

func randB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// PKCE makes a verifier and its S256 challenge.
func PKCE() (verifier, challenge string) {
	verifier = randB64(32)
	h := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(h[:])
}

// Connect runs the one-click flow for a connector and returns its tokens.
func (c *Client) Connect(ctx context.Context, provider string, scopes []string, open func(string)) (*Redeemed, error) {
	return c.authorize(ctx, "/oauth/"+url.PathEscape(provider)+"/start", scopes, open)
}

// SignInWith runs "Sign in with Google/GitHub" and returns a Blaze session.
func (c *Client) SignInWith(ctx context.Context, provider string, open func(string)) (*Session, error) {
	r, err := c.authorize(ctx, "/v1/auth/"+url.PathEscape(provider)+"/start", nil, open)
	if err != nil {
		return nil, err
	}
	if r.Kind != "session" || r.Token == "" || r.User == nil {
		return nil, &APIError{Status: 502, Code: "no_session"}
	}
	return &Session{Token: r.Token, User: *r.User}, nil
}

type cbResult struct {
	code, errCode string
}

var errCallbackState = errors.New("callback state does not match")

func parseCallback(line, state string) (cbResult, error) {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, `"'`)
	line = strings.TrimSpace(line)
	if line == "" {
		return cbResult{}, errors.New("empty callback")
	}
	var q url.Values
	if u, err := url.Parse(line); err == nil && (u.Scheme != "" || u.Host != "") {
		q = u.Query()
	} else {
		q, err = url.ParseQuery(strings.TrimPrefix(line, "?"))
		if err != nil {
			return cbResult{}, err
		}
	}
	if q.Get("state") != state {
		return cbResult{}, errCallbackState
	}
	res := cbResult{code: q.Get("code"), errCode: q.Get("error")}
	if res.errCode == "" && res.code == "" {
		res.errCode = "missing_code"
	}
	return res, nil
}

// authorize: listen on 127.0.0.1:<random>, open the Worker's start URL, wait for the
// redirect back with our state, then redeem the one-time code with the verifier.
func (c *Client) authorize(ctx context.Context, startPath string, scopes []string, open func(string)) (*Redeemed, error) {
	authCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("Blaze couldn't open a local port for the sign-in: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	state := randB64(24)
	verifier, challenge := PKCE()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/cb", port)

	got := make(chan cbResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/cb", func(w http.ResponseWriter, r *http.Request) {
		res, err := parseCallback(r.URL.RawQuery, state)
		if errors.Is(err, errCallbackState) {
			writePage(w, false, "Sign-in didn't finish", "That link doesn't match the sign-in Blaze started. Start again from Blaze.")
			return
		}
		if err != nil {
			res = cbResult{errCode: "missing_code"}
		}
		if res.errCode != "" {
			msg := "The sign-in came back with an error (" + res.errCode + "). Go back to Blaze and try again."
			if res.errCode == "access_denied" {
				msg = "You declined access. Nothing changed; you can close this tab."
			}
			writePage(w, false, "Sign-in didn't finish", msg)
		} else {
			writePage(w, true, "You're signed in", "You can close this tab and go back to Blaze.")
		}
		select {
		case got <- res:
		default:
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		// Let the browser finish loading the "you're signed in" page first.
		go func() {
			time.Sleep(1500 * time.Millisecond)
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
	}()

	v := url.Values{}
	v.Set("state", state)
	v.Set("code_challenge", challenge)
	v.Set("code_challenge_method", "S256")
	v.Set("redirect", redirect)
	if len(scopes) > 0 {
		v.Set("scope", strings.Join(scopes, " "))
	}
	open(c.Base + startPath + "?" + v.Encode())

	var paste <-chan string
	if c.Paste != nil {
		paste = c.Paste(authCtx)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	var res cbResult
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case res = <-got:
			return c.redeem(authCtx, res, verifier)
		case line, ok := <-paste:
			if !ok {
				paste = nil
				continue
			}
			var err error
			res, err = parseCallback(line, state)
			if errors.Is(err, errCallbackState) {
				fmt.Fprintln(os.Stderr, "That link is from a different sign-in. Paste the address from this sign-in.")
				if c.Paste != nil {
					paste = c.Paste(authCtx)
				}
				continue
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "That doesn't look like a sign-in address. Paste the full address from the browser.")
				if c.Paste != nil {
					paste = c.Paste(authCtx)
				}
				continue
			}
			return c.redeem(authCtx, res, verifier)
		case <-authCtx.Done():
			return nil, ErrCancelled
		case <-timer.C:
			return nil, ErrCancelled
		}
	}
}

func (c *Client) redeem(ctx context.Context, res cbResult, verifier string) (*Redeemed, error) {
	if res.errCode != "" {
		if res.errCode == "not_configured" {
			return nil, ErrNotConfigured
		}
		return nil, &APIError{Status: 400, Code: res.errCode}
	}
	var out Redeemed
	if err := c.do(ctx, "POST", "/oauth/redeem", "", map[string]string{"code": res.code, "code_verifier": verifier, "client": c.name()}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func writePage(w http.ResponseWriter, ok bool, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	mark := "#1F4BD8"
	if !ok {
		mark = "#111110"
	}
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Blaze</title><body style="font:15px/1.5 system-ui,sans-serif;background:#FBFBFA;color:#111110;display:grid;place-items:center;height:100vh;margin:0"><div style="max-width:440px;padding:32px;border:1px solid rgba(17,17,16,.18);background:#fff;box-shadow:inset 3px 0 0 %s"><div style="font:500 11px monospace;letter-spacing:.12em;text-transform:uppercase;color:%s">Blaze</div><h2 style="margin:8px 0 6px;font-weight:600">%s</h2><p style="margin:0;color:rgba(17,17,16,.6)">%s</p></div></body>`,
		mark, mark, esc(title), esc(msg))
}
