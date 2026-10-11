package connect

import (
	"context"
	"errors"
	"time"
)

// Broker is the one-click path: Blaze's sign-in service (internal/cloud) holds each
// provider's client secret, so the user just presses Connect instead of making a
// developer app. Manual setup (your own client ID / token) stays as the fallback.
type Broker interface {
	// Available is whether one-click works for this connector right now.
	Available(id string) bool
	// Known is whether the service is reachable at all (false: offline or not checked).
	Known() bool
	// Connect signs in through the browser and returns the tokens, the granted
	// scopes and a display hint (Notion workspace, Slack team).
	Connect(ctx context.Context, id string, scopes []string, open func(url string)) (tok *Token, granted []string, hint string, err error)
	// Refresh swaps a refresh token for a new access token.
	Refresh(ctx context.Context, id, refreshToken string) (*Token, error)
	// Message is a plain sentence for a broker error.
	Message(err error) string
}

// ErrBrokerUnreachable is returned by a Broker's Refresh when the service can't be reached
// (the connection stays; Blaze tries again later).
var ErrBrokerUnreachable = errors.New("broker_unreachable")

// SetBroker plugs in the one-click sign-in service (nil turns it off).
func (m *Manager) SetBroker(b Broker) {
	m.mu.Lock()
	m.broker = b
	m.mu.Unlock()
	m.changed()
}

func (m *Manager) getBroker() Broker {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.broker
}

// oneClick is whether this connector can connect through the broker now.
func (m *Manager) oneClick(c *Connector) bool {
	b := m.getBroker()
	return b != nil && b.Available(c.ID)
}

// cloudState is "ready" (one-click Connect), "soon" (the service is up but this
// provider isn't switched on yet) or "" (no service / offline).
func (m *Manager) cloudState(c *Connector) string {
	b := m.getBroker()
	switch {
	case b == nil || !b.Known():
		return ""
	case b.Available(c.ID):
		return "ready"
	}
	return "soon"
}

// manualReady is whether the user's own developer app is filled in.
func (m *Manager) manualReady(c *Connector, st ConnState) bool {
	switch c.Auth {
	case AuthOAuth, AuthDevice:
		return st.ClientID != "" && (!c.SecretRequired || m.store.ClientSecret(c.ID) != "")
	}
	return false
}

// useBroker decides Connect's path: one-click unless this is a reconnect of a
// connection the user made with their own developer app.
func (m *Manager) useBroker(c *Connector, st ConnState) bool {
	if !m.oneClick(c) {
		return false
	}
	if st.Via == "" && st.Connected && m.manualReady(c, st) {
		return false
	}
	return true
}

// startBroker runs the one-click sign-in in the background. The browser opens on the
// broker's start page; the connector shows "connecting" until it comes back.
func (m *Manager) startBroker(c *Connector, scopes []string) ConnectResult {
	b := m.getBroker()
	if b == nil {
		return fail(errors.New("one-click sign-in isn't available"))
	}
	m.mu.Lock()
	if prev := m.device[c.ID]; prev != nil {
		prev() // a newer attempt supersedes the older one
	}
	ctx, cancel := context.WithTimeout(context.Background(), 11*time.Minute)
	m.device[c.ID] = cancel
	m.mu.Unlock()

	urlCh := make(chan string, 1)
	open := func(u string) {
		m.setPending(c.ID, &PendingView{Kind: "browser", URL: u, OneClick: true})
		m.host.OpenURL(u)
		select {
		case urlCh <- u:
		default:
		}
	}
	go func() {
		defer cancel()
		tok, granted, hint, err := b.Connect(ctx, c.ID, scopes, open)
		m.mu.Lock()
		mine := m.device[c.ID] != nil && ctx.Err() == nil
		if mine {
			delete(m.device, c.ID)
		}
		m.mu.Unlock()
		if err != nil {
			if ctx.Err() == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				m.setPending(c.ID, nil)
				m.toast(c.Name+": "+b.Message(err), "error")
			}
			return
		}
		m.setPending(c.ID, nil)
		m.toast("Finishing "+c.Name+" setup", "")
		if len(granted) == 0 {
			granted = scopes
		}
		fctx, fcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer fcancel()
		if err := m.finishConnectVia(fctx, c, tok, granted, "broker", hint); err != nil {
			m.toast(err.Error(), "error")
			return
		}
		go m.host.Focus()
	}()
	select {
	case u := <-urlCh:
		return ConnectResult{Status: "waiting", URL: u}
	case <-time.After(3 * time.Second):
		return ConnectResult{Status: "waiting"}
	}
}

// ConnectManual is Connect through the user's own developer app or token, even when
// one-click is available.
func (m *Manager) ConnectManual(id string) ConnectResult {
	c := m.cat[id]
	if c == nil {
		return fail(errors.New("no connector " + id))
	}
	return m.connectManual(c, m.store.Conn(id))
}
