package connect

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeBroker struct {
	mu        sync.Mutex
	on        map[string]bool
	scopes    []string
	refreshed int
	refreshTo string
	fail      error
}

func (b *fakeBroker) Known() bool              { return true }
func (b *fakeBroker) Available(id string) bool { b.mu.Lock(); defer b.mu.Unlock(); return b.on[id] }
func (b *fakeBroker) Message(err error) string { return err.Error() }
func (b *fakeBroker) Connect(ctx context.Context, id string, scopes []string, open func(string)) (*Token, []string, string, error) {
	b.mu.Lock()
	b.scopes = scopes
	b.mu.Unlock()
	open("https://broker.example/oauth/" + id + "/start")
	if b.fail != nil {
		return nil, nil, "", b.fail
	}
	return &Token{AccessToken: "acc-1", RefreshToken: "ref-1", ExpiresAt: time.Now().Add(-time.Minute).UnixMilli()}, []string{"chat:write"}, "Acme team", nil
}
func (b *fakeBroker) Refresh(ctx context.Context, id, rt string) (*Token, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refreshed++
	b.refreshTo = rt
	return &Token{AccessToken: "acc-2", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}, nil
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}

func TestBrokerOneClick(t *testing.T) {
	rec := &recorder{}
	m := NewWith(t.TempDir(), &MemSecrets{}, rec.host(), Catalog, BuiltinTools())
	slack := m.cat["slack"]
	slack.WhoAmI = nil // no network in tests: the broker's hint names the account

	// Without the broker, Slack is a token login and Connect asks for the token.
	if r := m.Connect("slack"); r.Status != "error" {
		t.Fatalf("manual slack connect should need a token, got %+v", r)
	}
	if v := m.connView(slack); v.OneClick || v.Cloud != "" {
		t.Fatalf("no broker: %+v", v)
	}

	b := &fakeBroker{on: map[string]bool{"slack": true, "google": true}}
	m.SetBroker(b)
	if v := m.connView(m.cat["spotify"]); v.Cloud != "soon" || v.Status != "needs_setup" {
		t.Fatalf("spotify without a broker secret: cloud=%q status=%q", v.Cloud, v.Status)
	}
	if v := m.connView(m.cat["google"]); !v.OneClick || v.Status != "not_connected" {
		t.Fatalf("google one-click: one_click=%v status=%q", v.OneClick, v.Status)
	}

	r := m.Connect("slack")
	if r.Status != "waiting" || r.URL != "https://broker.example/oauth/slack/start" {
		t.Fatalf("connect: %+v", r)
	}
	waitFor(t, "slack connected", func() bool { return m.store.HasToken("slack") && m.store.Conn("slack").Connected })
	st := m.store.Conn("slack")
	if st.Via != "broker" || st.Account != "Acme team" || len(st.Scopes) != 1 {
		t.Fatalf("state after one-click: %+v", st)
	}
	v := m.connView(slack)
	if v.Status != "connected" || v.Via != "broker" {
		t.Fatalf("view: %+v", v)
	}

	// The token is already expired: access refreshes through the broker, not the provider.
	tok, err := m.access(context.Background(), "slack", false)
	if err != nil || tok != "acc-2" || b.refreshed != 1 || b.refreshTo != "ref-1" {
		t.Fatalf("refresh: tok=%q err=%v n=%d rt=%q", tok, err, b.refreshed, b.refreshTo)
	}
	if got := m.store.Token("slack"); got.RefreshToken != "ref-1" {
		t.Fatalf("refresh token should be kept, got %+v", got)
	}

	m.Disconnect("slack")
	if st := m.store.Conn("slack"); st.Via != "" || st.Connected {
		t.Fatalf("after disconnect: %+v", st)
	}
}

func TestBrokerFailureClearsPending(t *testing.T) {
	rec := &recorder{}
	m := NewWith(t.TempDir(), &MemSecrets{}, rec.host(), Catalog, BuiltinTools())
	m.SetBroker(&fakeBroker{on: map[string]bool{"notion": true}, fail: errors.New("access_denied")})
	m.Connect("notion")
	waitFor(t, "pending cleared", func() bool { return m.connView(m.cat["notion"]).Status == "not_connected" })
	if m.store.HasToken("notion") {
		t.Fatal("no token after a failed sign-in")
	}
}

func TestManualStillWorksWithBroker(t *testing.T) {
	rec := &recorder{}
	m := NewWith(t.TempDir(), &MemSecrets{}, rec.host(), Catalog, BuiltinTools())
	m.SetBroker(&fakeBroker{on: map[string]bool{"spotify": true}})
	// ConnectManual ignores the broker and asks for the user's own client ID.
	if r := m.ConnectManual("spotify"); r.Status != "error" {
		t.Fatalf("manual spotify without a client id: %+v", r)
	}
}
