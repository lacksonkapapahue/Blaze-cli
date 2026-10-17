package connect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type authFail struct{ status int }

func (e authFail) Error() string     { return fmt.Sprintf("http_%d", e.status) }
func (e authFail) AuthFailure() bool { return e.status == 401 }

func TestAuthRefused(t *testing.T) {
	yes := []error{
		&TokenError{Status: 401},
		&TokenError{Status: 400, Code: "invalid_grant"},
		&TokenError{Status: 400, Code: "invalid_client"},
		&TokenError{Status: 401, Code: "unauthorized_client"},
		&TokenError{Status: 200, Code: "bad_refresh_token"},
		fmt.Errorf("wrapped: %w", &TokenError{Status: 400, Code: "invalid_grant"}),
		authFail{401},
	}
	no := []error{
		nil,
		&TokenError{Unreachable: true, Desc: "dial tcp: connection refused"},
		&TokenError{Status: 400},
		&TokenError{Status: 400, Code: "invalid_request"},
		&TokenError{Status: 429, Code: "rate_limited"},
		&TokenError{Status: 500, Code: "invalid_grant"},
		&TokenError{Status: 502},
		&TokenError{Status: 503, Code: "temporarily_unavailable"},
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("x: %w", context.Canceled),
		ErrBrokerUnreachable,
		errors.New("cancelled"),
		authFail{429},
		authFail{502},
	}
	for _, e := range yes {
		if !authRefused(e) {
			t.Errorf("%v should be a definitive refusal", e)
		}
	}
	for _, e := range no {
		if authRefused(e) {
			t.Errorf("%v should be transient", e)
		}
	}
}

// tokenServer answers refresh requests with whatever status/body is set.
type tokenServer struct {
	mu     sync.Mutex
	status int
	body   string
}

func newTokenServer(t *testing.T) (*tokenServer, *httptest.Server) {
	ts := &tokenServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.mu.Lock()
		st, b := ts.status, ts.body
		ts.mu.Unlock()
		w.WriteHeader(st)
		io.WriteString(w, b)
	}))
	t.Cleanup(srv.Close)
	return ts, srv
}

func (ts *tokenServer) set(st int, b string) {
	ts.mu.Lock()
	ts.status, ts.body = st, b
	ts.mu.Unlock()
}

func TestRefreshTransientErrorsKeepConnection(t *testing.T) {
	ts, srv := newTokenServer(t)
	conn := &Connector{ID: "fake", Name: "Fake", Auth: AuthOAuth, TokenURL: srv.URL, Groups: []string{"fake"}}
	m := NewWith(t.TempDir(), &MemSecrets{}, (&recorder{}).host(), []*Connector{conn}, nil)
	t.Cleanup(m.Close)
	_ = m.store.SetToken("fake", &Token{AccessToken: "old", RefreshToken: "rt", ExpiresAt: time.Now().Add(-time.Minute).UnixMilli()})
	_ = m.store.Update("fake", func(s *ConnState) { s.Connected, s.ClientID = true, "cid" })

	for _, c := range []struct {
		st   int
		body string
	}{
		{500, `{"error":"server_error"}`},
		{502, `<html>Bad gateway</html>`},
		{503, ``},
		{429, `{"error":"rate_limited"}`},
		{400, `{"error":"invalid_request"}`},
	} {
		ts.set(c.st, c.body)
		_, err := m.access(context.Background(), "fake", false)
		if err == nil || errors.Is(err, ErrReauth) {
			t.Fatalf("%d: want a transient error, got %v", c.st, err)
		}
		if m.store.Conn("fake").Expired {
			t.Fatalf("%d %s must not mark the connection expired", c.st, c.body)
		}
	}
	// a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.access(ctx, "fake", false); err == nil || errors.Is(err, ErrReauth) || m.store.Conn("fake").Expired {
		t.Fatalf("cancel: err=%v expired=%v", err, m.store.Conn("fake").Expired)
	}
	// a working refresh still works afterwards
	ts.set(200, `{"access_token":"new","expires_in":3600}`)
	if tok, err := m.access(context.Background(), "fake", false); err != nil || tok != "new" {
		t.Fatalf("refresh: %q %v", tok, err)
	}
	// a definitive refusal expires it
	tok := m.store.Token("fake")
	tok.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
	_ = m.store.SetToken("fake", tok)
	ts.set(400, `{"error":"invalid_grant","error_description":"revoked"}`)
	if _, err := m.access(context.Background(), "fake", false); !errors.Is(err, ErrReauth) || !m.store.Conn("fake").Expired {
		t.Fatalf("invalid_grant: err=%v expired=%v", err, m.store.Conn("fake").Expired)
	}
}

type errBroker struct {
	fakeBroker
	err error
}

func (b *errBroker) Refresh(ctx context.Context, id, rt string) (*Token, error) { return nil, b.err }

func TestBrokerRefreshTransientErrorsKeepConnection(t *testing.T) {
	m := NewWith(t.TempDir(), &MemSecrets{}, (&recorder{}).host(), Catalog, BuiltinTools())
	t.Cleanup(m.Close)
	b := &errBroker{fakeBroker: fakeBroker{on: map[string]bool{"slack": true}}}
	m.SetBroker(b)
	_ = m.store.SetToken("slack", &Token{AccessToken: "old", RefreshToken: "rt", ExpiresAt: time.Now().Add(-time.Minute).UnixMilli()})
	_ = m.store.Update("slack", func(s *ConnState) { s.Connected, s.Via = true, "broker" })

	for _, e := range []error{ErrBrokerUnreachable, authFail{429}, authFail{502}, errors.New("cancelled"), context.Canceled, context.DeadlineExceeded} {
		b.err = e
		_, err := m.access(context.Background(), "slack", false)
		if err == nil || errors.Is(err, ErrReauth) || m.store.Conn("slack").Expired {
			t.Fatalf("%v: err=%v expired=%v", e, err, m.store.Conn("slack").Expired)
		}
	}
	b.err = authFail{401}
	if _, err := m.access(context.Background(), "slack", false); !errors.Is(err, ErrReauth) || !m.store.Conn("slack").Expired {
		t.Fatalf("401: err=%v expired=%v", err, m.store.Conn("slack").Expired)
	}
}
