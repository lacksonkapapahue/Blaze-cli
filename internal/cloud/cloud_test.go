package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeWorker plays blaze-auth: start redirects straight back to the loopback with a
// one-time code; redeem checks the PKCE verifier against the challenge.
func fakeWorker(t *testing.T, providerErr string) *httptest.Server {
	var mu sync.Mutex
	codes := map[string]string{} // code -> challenge
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/google/start", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		rd, _ := url.Parse(q.Get("redirect"))
		if rd == nil || rd.Hostname() != "127.0.0.1" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "openid email" {
			http.Error(w, "bad", 400)
			return
		}
		v := url.Values{"state": {q.Get("state")}}
		if providerErr != "" {
			v.Set("error", providerErr)
		} else {
			mu.Lock()
			codes["onetime-code-0123456789"] = q.Get("code_challenge")
			mu.Unlock()
			v.Set("code", "onetime-code-0123456789")
		}
		http.Redirect(w, r, rd.String()+"?"+v.Encode(), http.StatusFound)
	})
	mux.HandleFunc("/oauth/redeem", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		h := sha256.Sum256([]byte(b["code_verifier"]))
		mu.Lock()
		cc, ok := codes[b["code"]]
		delete(codes, b["code"])
		mu.Unlock()
		if !ok || cc != base64.RawURLEncoding.EncodeToString(h[:]) {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_code_verifier"}`))
			return
		}
		w.Write([]byte(`{"kind":"tokens","provider":"google","tokens":{"access_token":"ya29.x","refresh_token":"1//r","expires_in":3599,"scope":"openid email"},"scopes":["openid","email"]}`))
	})
	mux.HandleFunc("/oauth/spotify/refresh", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte(`{"error":"not_configured","provider":"spotify"}`))
	})
	mux.HandleFunc("/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"invalid_credentials"}`))
	})
	return httptest.NewServer(mux)
}

// browse follows the start URL like a browser would, through to the loopback.
func browse(t *testing.T) func(string) {
	return func(u string) {
		go func() {
			r, err := http.Get(u)
			if err != nil {
				t.Errorf("browser: %v", err)
				return
			}
			r.Body.Close()
		}()
	}
}

func TestConnectRoundTrip(t *testing.T) {
	ws := fakeWorker(t, "")
	defer ws.Close()
	c := New(ws.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := c.Connect(ctx, "google", []string{"openid", "email"}, browse(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "tokens" || r.Tokens.AccessToken != "ya29.x" || r.Tokens.RefreshToken != "1//r" || r.Tokens.ExpiresIn != 3599 {
		t.Fatalf("got %+v", r)
	}
}

func TestConnectProviderError(t *testing.T) {
	ws := fakeWorker(t, "access_denied")
	defer ws.Close()
	_, err := New(ws.URL).Connect(context.Background(), "google", []string{"openid", "email"}, browse(t))
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "access_denied" {
		t.Fatalf("want access_denied, got %v", err)
	}
	if !strings.Contains(Message(err), "declined") {
		t.Fatalf("message: %q", Message(err))
	}
}

func TestCancel(t *testing.T) {
	ws := fakeWorker(t, "")
	defer ws.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := New(ws.URL).Connect(ctx, "google", nil, func(string) {})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("want cancelled, got %v", err)
	}
}

func TestErrors(t *testing.T) {
	ws := fakeWorker(t, "")
	defer ws.Close()
	c := New(ws.URL)
	if _, err := c.Refresh(context.Background(), "spotify", "x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("want not_configured, got %v", err)
	}
	_, err := c.SignIn(context.Background(), "a@b.co", "nope")
	if Message(err) != "That email and password don't match." {
		t.Fatalf("message: %q", Message(err))
	}
	if _, err := New("http://127.0.0.1:1").Providers(context.Background()); !strings.Contains(Message(err), "internet") {
		t.Fatalf("unreachable message: %q", Message(err))
	}
}

func TestBaseURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BLAZE_CLOUD_URL", "")
	if BaseURL(dir) != DefaultBase {
		t.Fatal("default")
	}
	os.WriteFile(filepath.Join(dir, "cloud.json"), []byte(`{"cloud_url":"http://127.0.0.1:8787/"}`), 0o600)
	if got := BaseURL(dir); got != "http://127.0.0.1:8787" {
		t.Fatalf("file override: %q", got)
	}
	t.Setenv("BLAZE_CLOUD_URL", "https://x.example/")
	if got := BaseURL(dir); got != "https://x.example" {
		t.Fatalf("env override: %q", got)
	}
}
