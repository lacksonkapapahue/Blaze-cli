package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

// Host is what the connectors need from the app.
type Host struct {
	Emit    func(event string, data any)
	OpenURL func(url string)
	// Confirm shows an approval card. allowAlways adds "Always allow".
	Confirm func(title, detail string, allowAlways bool) (yes, always bool)
	// ConfirmCtx, when set, is used instead of Confirm and gets the tool call's
	// context, so a host can route the card to whoever made the call (the CLI shows
	// a background agent's approvals on that agent, not over the prompt).
	ConfirmCtx func(ctx context.Context, title, detail string, allowAlways bool) (yes, always bool)
	Yolo       func() bool   // the auto-approve setting
	Focus      func()        // bring Blaze to the front after a browser sign-in
	Dir        func() string // working folder for local MCP servers
	Version    string
}

// Manager owns the catalog, the sign-ins, the MCP connections and the tool proxy.
type Manager struct {
	host  Host
	store *Store
	att   *Attempts
	hc    *http.Client
	conns []*Connector
	cat   map[string]*Connector
	tools map[string]*Tool
	order []string // tool names in catalog order

	// Port is the loopback callback port (LoopbackPort; tests use 0 for any).
	Port int

	mu        sync.Mutex
	refreshMu sync.Mutex
	broker    Broker // one-click sign-in service (nil: manual setup only)
	loop      *loopback
	pending   map[string]*PendingView
	device    map[string]context.CancelFunc
	loaded    map[string]bool
	mcp       map[string]*mcpClient
	mcpLogs   map[string]*ringBuf
}

// New makes the manager with the built-in catalog.
func New(dir string, sec Secrets, host Host) *Manager {
	return NewWith(dir, sec, host, Catalog, BuiltinTools())
}

// BuiltinTools is every connector tool, in display order.
func BuiltinTools() []*Tool {
	var all []*Tool
	for _, l := range [][]*Tool{googleTools(), githubTools(), spotifyTools(), notionTools(), slackTools()} {
		all = append(all, l...)
	}
	for _, t := range all {
		switch t.Group {
		case "gmail", "calendar", "drive", "contacts":
			t.Connector = "google"
		default:
			t.Connector = t.Group
		}
	}
	return all
}

// NewWith is New with a custom catalog (tests).
func NewWith(dir string, sec Secrets, host Host, conns []*Connector, tools []*Tool) *Manager {
	m := &Manager{host: host, store: OpenStore(dir, sec), att: NewAttempts(), hc: &http.Client{Timeout: 60 * time.Second},
		conns: conns, cat: map[string]*Connector{}, tools: map[string]*Tool{}, Port: LoopbackPort,
		pending: map[string]*PendingView{}, device: map[string]context.CancelFunc{}, loaded: map[string]bool{},
		mcp: map[string]*mcpClient{}, mcpLogs: map[string]*ringBuf{}}
	for _, c := range conns {
		m.cat[c.ID] = c
	}
	for _, t := range tools {
		m.tools[t.Name] = t
		m.order = append(m.order, t.Name)
	}
	if m.host.Emit == nil {
		m.host.Emit = func(string, any) {}
	}
	if m.host.OpenURL == nil {
		m.host.OpenURL = func(string) {}
	}
	if m.host.Confirm == nil {
		m.host.Confirm = func(string, string, bool) (bool, bool) { return false, false }
	}
	if m.host.Yolo == nil {
		m.host.Yolo = func() bool { return false }
	}
	if m.host.Focus == nil {
		m.host.Focus = func() {}
	}
	if m.host.Dir == nil {
		m.host.Dir = func() string { return "" }
	}
	return m
}

// Store gives tests and the app access to saved state.
func (m *Manager) Store() *Store { return m.store }

// Attempts is the pending sign-in table.
func (m *Manager) Attempts() *Attempts { return m.att }

func (m *Manager) emit(ev string, data any) { m.host.Emit(ev, data) }
func (m *Manager) changed()                 { m.emit("connectors", nil) }
func (m *Manager) toast(text, kind string) {
	m.emit("toast", map[string]string{"text": text, "kind": kind})
}

// Close stops everything the manager started.
func (m *Manager) Close() {
	m.mu.Lock()
	for _, c := range m.device {
		c()
	}
	for _, c := range m.mcp {
		c.close()
	}
	m.mcp = map[string]*mcpClient{}
	l := m.loop
	m.loop = nil
	m.mu.Unlock()
	l.stop()
}

// ---------------- views ----------------

type ToolView struct {
	Name       string `json:"name"`
	Desc       string `json:"desc"`
	Group      string `json:"group"`
	Risk       Risk   `json:"risk"`
	Mode       string `json:"mode"`    // effective: ask | always | off
	Default    string `json:"default"` // what it is when the user hasn't chosen
	NeedsGrant bool   `json:"needs_grant"`
}

type FieldView struct {
	Field
	Value string `json:"value"` // non-secret fields only
	Set   bool   `json:"set"`
}

type PendingView struct {
	Kind     string `json:"kind"` // browser | device
	URL      string `json:"url"`
	UserCode string `json:"user_code,omitempty"`
	OneClick bool   `json:"one_click,omitempty"` // through Blaze's sign-in service
}

type ConnectorView struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Blurb        string       `json:"blurb"`
	Auth         Auth         `json:"auth"`
	Status       string       `json:"status"` // not_connected | needs_setup | connecting | connected | expired
	Account      string       `json:"account"`
	Enabled      bool         `json:"enabled"`
	Fields       []FieldView  `json:"fields"`
	Steps        []string     `json:"steps"`
	Console      string       `json:"console"`
	ConsoleName  string       `json:"console_name"`
	Redirect     string       `json:"redirect"`
	CustomScheme bool         `json:"custom_scheme"`
	RedirectURI  string       `json:"redirect_uri"`
	Granted      []string     `json:"granted"`
	Groups       []string     `json:"groups"`
	Tools        []ToolView   `json:"tools"`
	Pending      *PendingView `json:"pending"`
	Since        string       `json:"since"`
	OneClick     bool         `json:"one_click"` // Connect goes through Blaze's sign-in service
	Cloud        string       `json:"cloud"`     // ready | soon | "" (service offline or off)
	Via          string       `json:"via"`       // broker | "" (your own app or token)
}

type MCPView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Transport string     `json:"transport"`
	URL       string     `json:"url"`
	Command   string     `json:"command"`
	Args      []string   `json:"args"`
	Header    string     `json:"header"`
	HasToken  bool       `json:"has_token"`
	Enabled   bool       `json:"enabled"`
	Status    string     `json:"status"` // ready | error | unchecked | disabled
	Error     string     `json:"error"`
	Tools     []ToolView `json:"tools"`
	Checked   string     `json:"checked"`
}

type View struct {
	Connectors []ConnectorView `json:"connectors"`
	MCP        []MCPView       `json:"mcp"`
	Loopback   string          `json:"loopback"`
	Scheme     string          `json:"scheme"`
}

func effMode(override string, r Risk) string {
	if override == "ask" || override == "always" || override == "off" {
		return override
	}
	return defMode(r)
}

func defMode(r Risk) string {
	if r == Read {
		return "always"
	}
	return "ask"
}

func (m *Manager) needsSetup(c *Connector) bool {
	if c.Auth != AuthOAuth && c.Auth != AuthDevice {
		return false
	}
	if m.oneClick(c) {
		return false
	}
	st := m.store.Conn(c.ID)
	return st.ClientID == "" || (c.SecretRequired && m.store.ClientSecret(c.ID) == "")
}

func (m *Manager) statusOf(c *Connector, st ConnState) string {
	m.mu.Lock()
	p := m.pending[c.ID]
	m.mu.Unlock()
	tok := m.store.HasToken(c.ID)
	switch {
	case p != nil:
		return "connecting"
	case tok && st.Expired:
		return "expired"
	case tok:
		return "connected"
	case m.needsSetup(c):
		return "needs_setup"
	}
	return "not_connected"
}

// Views is the whole Connectors page.
func (m *Manager) Views() View {
	v := View{Loopback: fmt.Sprintf("http://127.0.0.1:%d/callback", LoopbackPort), Scheme: SchemeCallback}
	for _, c := range m.conns {
		v.Connectors = append(v.Connectors, m.connView(c))
	}
	for _, s := range m.store.MCPList() {
		v.MCP = append(v.MCP, m.mcpView(s))
	}
	return v
}

func (m *Manager) connView(c *Connector) ConnectorView {
	st := m.store.Conn(c.ID)
	cv := ConnectorView{ID: c.ID, Name: c.Name, Blurb: c.Blurb, Auth: c.Auth, Status: m.statusOf(c, st), Account: st.Account,
		Enabled: !st.Disabled, Steps: c.Steps, Console: c.Console, ConsoleName: c.ConsoleName, Redirect: st.Redirect,
		CustomScheme: c.CustomScheme, Groups: c.Groups, Cloud: m.cloudState(c), Via: st.Via}
	cv.OneClick = cv.Cloud == "ready"
	if cv.Status == "connected" || cv.Status == "expired" {
		if !st.ConnectedAt.IsZero() {
			cv.Since = st.ConnectedAt.Format("2 Jan 2006")
		}
	}
	if c.Auth == AuthOAuth {
		cv.RedirectURI = fmt.Sprintf("http://127.0.0.1:%d/callback", LoopbackPort)
		if st.Redirect == "scheme" && c.CustomScheme {
			cv.RedirectURI = SchemeCallback
		}
	}
	for _, f := range c.Fields {
		fv := FieldView{Field: f}
		switch f.Key {
		case "client_id":
			fv.Value, fv.Set = st.ClientID, st.ClientID != ""
		case "client_secret":
			fv.Set = m.store.ClientSecret(c.ID) != ""
		case "token":
			fv.Set = m.store.Token(c.ID) != nil
		}
		cv.Fields = append(cv.Fields, fv)
	}
	for _, s := range st.Scopes {
		if strings.HasPrefix(s, gScope) {
			cv.Granted = append(cv.Granted, ScopeLabel(s))
		}
	}
	for _, n := range m.order {
		t := m.tools[n]
		if t.Connector != c.ID {
			continue
		}
		cv.Tools = append(cv.Tools, ToolView{Name: t.Name, Desc: t.Desc, Group: t.Group, Risk: t.Risk, Mode: effMode(st.Modes[t.Name], t.Risk),
			Default: defMode(t.Risk), NeedsGrant: cv.Status == "connected" && len(missingScopes(t.Scopes, st.Scopes)) > 0})
	}
	m.mu.Lock()
	if p := m.pending[c.ID]; p != nil {
		cp := *p
		cv.Pending = &cp
	}
	m.mu.Unlock()
	return cv
}

func mcpRisk(t MCPTool) Risk {
	switch {
	case t.Destructive:
		return Write
	case t.ReadOnly:
		return Read
	}
	return Action
}

func (m *Manager) mcpView(s MCPServer) MCPView {
	v := MCPView{ID: s.ID, Name: s.Name, Transport: s.Transport, URL: s.URL, Command: s.Command, Args: s.Args, Header: s.Header,
		HasToken: m.store.MCPToken(s.ID) != "", Enabled: !s.Disabled, Error: s.Error}
	switch {
	case s.Disabled:
		v.Status = "disabled"
	case s.Error != "":
		v.Status = "error"
	case s.Checked.IsZero():
		v.Status = "unchecked"
	default:
		v.Status = "ready"
	}
	if !s.Checked.IsZero() {
		v.Checked = s.Checked.Format("2 Jan 15:04")
	}
	for _, t := range s.Tools {
		r := mcpRisk(t)
		v.Tools = append(v.Tools, ToolView{Name: t.Name, Desc: clip(t.Description, 240), Group: "mcp:" + s.ID, Risk: r, Mode: effMode(s.Modes[t.Name], r), Default: defMode(r)})
	}
	return v
}

// ---------------- setup ----------------

// SetField saves one setup field: client_id, client_secret or token.
func (m *Manager) SetField(id, key, value string) error {
	c := m.cat[id]
	if c == nil {
		return errors.New("no connector " + id)
	}
	value = strings.TrimSpace(value)
	defer m.changed()
	switch key {
	case "client_id":
		return m.store.Update(id, func(s *ConnState) { s.ClientID = value })
	case "client_secret":
		return m.store.SetClientSecret(id, value)
	case "token":
		return m.SaveToken(id, value)
	}
	return errors.New("unknown field " + key)
}

// SetRedirect picks loopback ("") or the blaze:// scheme for a provider that allows it.
func (m *Manager) SetRedirect(id, mode string) error {
	if mode != "scheme" {
		mode = ""
	}
	defer m.changed()
	return m.store.Update(id, func(s *ConnState) { s.Redirect = mode })
}

// SetEnabled turns a connected connector's tools on or off for Blaze.
func (m *Manager) SetEnabled(id string, on bool) error {
	defer m.changed()
	return m.store.Update(id, func(s *ConnState) { s.Disabled = !on })
}

// SetToolMode stores ask / always / off for one tool ("" = default). owner is a
// connector id or "mcp:<server>".
func (m *Manager) SetToolMode(owner, tool, mode string) error {
	switch mode {
	case "ask", "always", "off", "":
	default:
		return errors.New("mode must be ask, always or off")
	}
	defer m.changed()
	set := func(mm map[string]string) map[string]string {
		if mm == nil {
			mm = map[string]string{}
		}
		if mode == "" {
			delete(mm, tool)
		} else {
			mm[tool] = mode
		}
		return mm
	}
	if sid, ok := strings.CutPrefix(owner, "mcp:"); ok {
		return m.store.UpdateMCP(sid, func(s *MCPServer) { s.Modes = set(s.Modes) })
	}
	if m.cat[owner] == nil {
		return errors.New("no connector " + owner)
	}
	return m.store.Update(owner, func(s *ConnState) { s.Modes = set(s.Modes) })
}

// ConnectResult tells the UI what happened.
type ConnectResult struct {
	Status   string `json:"status"` // waiting | connected | error
	Error    string `json:"error,omitempty"`
	URL      string `json:"url,omitempty"`
	UserCode string `json:"user_code,omitempty"`
}

func fail(err error) ConnectResult { return ConnectResult{Status: "error", Error: err.Error()} }

// Connect starts (or restarts) a connector's sign-in.
func (m *Manager) Connect(id string) ConnectResult {
	c := m.cat[id]
	if c == nil {
		return fail(errors.New("no connector " + id))
	}
	st := m.store.Conn(id)
	if m.useBroker(c, st) {
		return m.startBroker(c, m.wantScopes(c, st, nil))
	}
	return m.connectManual(c, st)
}

// connectManual is the do-it-yourself path: your own developer app, or a token.
func (m *Manager) connectManual(c *Connector, st ConnState) ConnectResult {
	id := c.ID
	if (c.Auth == AuthOAuth || c.Auth == AuthDevice) && st.ClientID == "" {
		return fail(errors.New("Paste your " + c.Name + " client ID first (see the steps)."))
	}
	if c.SecretRequired && m.store.ClientSecret(id) == "" {
		return fail(errors.New("Paste your " + c.Name + " client secret first."))
	}
	switch c.Auth {
	case AuthOAuth:
		return m.startOAuth(c, st, m.wantScopes(c, st, nil))
	case AuthDevice:
		return m.startDevice(c, st)
	case AuthToken:
		return fail(errors.New("Paste the token, then Save."))
	}
	return ConnectResult{Status: "connected"}
}

// GrantScopes re-runs the sign-in asking for a tool's missing scopes (or every
// tool's when tool is ""). Only ever started by the user.
func (m *Manager) GrantScopes(id, tool string) ConnectResult {
	c := m.cat[id]
	if c == nil {
		return fail(errors.New("this connector can't add permissions"))
	}
	st := m.store.Conn(id)
	viaBroker := st.Via == "broker" && m.oneClick(c)
	if c.Auth != AuthOAuth && !viaBroker {
		return fail(errors.New("this connector can't add permissions"))
	}
	var extra []string
	for _, n := range m.order {
		t := m.tools[n]
		if t.Connector == id && (tool == "" || t.Name == tool) {
			extra = append(extra, t.Scopes...)
		}
	}
	if viaBroker {
		return m.startBroker(c, m.wantScopes(c, st, extra))
	}
	return m.startOAuth(c, st, m.wantScopes(c, st, extra))
}

// wantScopes: base scopes, every read tool's scopes, what's granted already, plus extra.
func (m *Manager) wantScopes(c *Connector, st ConnState, extra []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(l []string) {
		for _, s := range l {
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	add(c.BaseScopes)
	for _, n := range m.order {
		if t := m.tools[n]; t.Connector == c.ID && t.Risk == Read {
			add(t.Scopes)
		}
	}
	if m.store.Token(c.ID) != nil {
		add(st.Scopes)
	}
	add(extra)
	return out
}

func (m *Manager) setPending(id string, p *PendingView) {
	m.mu.Lock()
	if p == nil {
		delete(m.pending, id)
	} else {
		m.pending[id] = p
	}
	m.mu.Unlock()
	m.changed()
}

// CancelConnect stops a sign-in in progress.
func (m *Manager) CancelConnect(id string) {
	m.att.Cancel(id)
	m.mu.Lock()
	if c := m.device[id]; c != nil {
		c()
		delete(m.device, id)
	}
	m.mu.Unlock()
	m.setPending(id, nil)
	m.stopLoopIfIdle()
}

func (m *Manager) ensureLoopback(anyPort bool) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loop != nil {
		if m.loop.port == m.Port || anyPort {
			return m.loop.port, nil
		}
		m.loop.stop()
		m.loop = nil
	}
	l, err := startLoopback(m.Port, m.HandleCallback)
	if err != nil && anyPort {
		l, err = startLoopback(0, m.HandleCallback)
	}
	if err != nil {
		return 0, fmt.Errorf("Blaze couldn't listen on 127.0.0.1:%d for the sign-in (%v). Close whatever uses that port, or pick the blaze:// redirect", m.Port, err)
	}
	m.loop = l
	return l.port, nil
}

func (m *Manager) stopLoopIfIdle() {
	if m.att.Count() > 0 {
		return
	}
	m.mu.Lock()
	l := m.loop
	m.loop = nil
	m.mu.Unlock()
	if l != nil {
		go func() { time.Sleep(1500 * time.Millisecond); l.stop() }()
	}
}

func (m *Manager) startOAuth(c *Connector, st ConnState, scopes []string) ConnectResult {
	redirect := SchemeCallback
	if !(st.Redirect == "scheme" && c.CustomScheme) {
		port, err := m.ensureLoopback(c.AnyPort)
		if err != nil {
			return fail(err)
		}
		redirect = fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	}
	at := m.att.Begin(c.ID, redirect, scopes)
	v := url.Values{}
	v.Set("client_id", st.ClientID)
	v.Set("redirect_uri", redirect)
	v.Set("response_type", "code")
	v.Set("scope", strings.Join(scopes, " "))
	v.Set("state", at.State)
	v.Set("code_challenge", at.PKCE.Challenge)
	v.Set("code_challenge_method", "S256")
	for k, vs := range c.AuthParams {
		v[k] = vs
	}
	u := c.AuthURL + "?" + v.Encode()
	m.setPending(c.ID, &PendingView{Kind: "browser", URL: u})
	m.host.OpenURL(u)
	state := at.State
	time.AfterFunc(m.att.TTL+5*time.Second, func() {
		m.mu.Lock()
		p := m.pending[c.ID]
		m.mu.Unlock()
		if p != nil && p.URL == u && !m.att.Pending(c.ID) {
			m.setPending(c.ID, nil)
			m.stopLoopIfIdle()
		}
		_ = state
	})
	return ConnectResult{Status: "waiting", URL: u}
}

// HandleURL routes a blaze:// URL (from the OS) to its handler.
func (m *Manager) HandleURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "blaze" {
		return false
	}
	if u.Host == "connector" && strings.TrimSuffix(u.Path, "/") == "/oauth_callback" {
		m.HandleCallback(raw)
		return true
	}
	return false
}

// HandleCallback finishes a browser sign-in: it matches the state to a pending
// attempt, exchanges the code with the PKCE verifier and stores the token.
func (m *Manager) HandleCallback(raw string) (title, msg string, ok bool) {
	at, code, err := m.att.Resolve(raw)
	if at != nil {
		m.setPending(at.Connector, nil)
	}
	defer m.stopLoopIfIdle()
	if err != nil {
		text := RejectMessage(err)
		m.toast(text, "error")
		return "Sign-in didn't finish", text, false
	}
	c := m.cat[at.Connector]
	m.toast("Finishing "+c.Name+" setup", "")
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", at.RedirectURI)
	form.Set("client_id", m.store.Conn(c.ID).ClientID)
	form.Set("code_verifier", at.PKCE.Verifier)
	if sec := m.store.ClientSecret(c.ID); sec != "" {
		form.Set("client_secret", sec)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	tok, err := postToken(ctx, m.hc, c.TokenURL, form)
	if err != nil {
		text := "Couldn't finish " + c.Name + " setup: " + err.Error()
		m.toast(text, "error")
		return "Sign-in didn't finish", text, false
	}
	if err := m.finishConnect(ctx, c, tok, at.Scopes); err != nil {
		m.toast(err.Error(), "error")
		return "Sign-in didn't finish", err.Error(), false
	}
	go m.host.Focus()
	return "Connected to " + c.Name, "You can close this tab and go back to Blaze.", true
}

func (m *Manager) finishConnect(ctx context.Context, c *Connector, tok *Token, requested []string) error {
	return m.finishConnectVia(ctx, c, tok, requested, "", "")
}

// finishConnectVia stores a new sign-in. via is "broker" for one-click connections;
// hint is the account name to show when WhoAmI can't tell.
func (m *Manager) finishConnectVia(ctx context.Context, c *Connector, tok *Token, requested []string, via, hint string) error {
	granted := splitScopes(tok.Scope)
	if len(granted) == 0 {
		granted = requested
	}
	if err := m.store.SetToken(c.ID, tok); err != nil {
		return err
	}
	account := ""
	if c.WhoAmI != nil {
		if a, err := c.WhoAmI(&call{ctx: ctx, m: m, conn: c.ID, token: tok.AccessToken, a: args{}}); err == nil {
			account = a
		}
	}
	if account == "" {
		account = hint
	}
	_ = m.store.Update(c.ID, func(s *ConnState) {
		s.Connected, s.Expired, s.Scopes, s.ConnectedAt, s.Via = true, false, granted, time.Now(), via
		if account != "" {
			s.Account = account
		}
	})
	m.toast(c.Name+" is now connected", "ok")
	m.changed()
	return nil
}

// ---------------- device flow (GitHub) ----------------

func (m *Manager) startDevice(c *Connector, st ConnState) ConnectResult {
	m.mu.Lock()
	if prev := m.device[c.ID]; prev != nil {
		prev() // a newer attempt supersedes the older one
	}
	ctx, cancel := context.WithTimeout(context.Background(), 16*time.Minute)
	m.device[c.ID] = cancel
	m.mu.Unlock()
	form := url.Values{"client_id": {st.ClientID}, "scope": {strings.Join(c.BaseScopes, " ")}}
	req, _ := http.NewRequestWithContext(ctx, "POST", c.DeviceURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.hc.Do(req)
	if err != nil {
		cancel()
		return fail(err)
	}
	var d struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
		ErrorDesc       string `json:"error_description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&d)
	resp.Body.Close()
	if d.DeviceCode == "" {
		cancel()
		msg := d.ErrorDesc
		if msg == "" {
			msg = d.Error
		}
		if msg == "" {
			msg = resp.Status
		}
		if d.Error == "device_flow_disabled" {
			msg = "Device flow is off for this OAuth app. Tick \"Enable Device Flow\" on its GitHub settings page."
		}
		return fail(errors.New(c.Name + ": " + msg))
	}
	if d.VerificationURI == "" {
		d.VerificationURI = "https://github.com/login/device"
	}
	if d.Interval <= 0 {
		d.Interval = 5
	}
	m.setPending(c.ID, &PendingView{Kind: "device", URL: d.VerificationURI, UserCode: d.UserCode})
	m.host.OpenURL(d.VerificationURI)
	go m.pollDevice(ctx, cancel, c, st.ClientID, d.DeviceCode, d.Interval)
	return ConnectResult{Status: "waiting", URL: d.VerificationURI, UserCode: d.UserCode}
}

func (m *Manager) pollDevice(ctx context.Context, cancel context.CancelFunc, c *Connector, clientID, deviceCode string, interval int) {
	defer cancel()
	done := func() {
		m.mu.Lock()
		delete(m.device, c.ID)
		m.mu.Unlock()
		m.setPending(c.ID, nil)
	}
	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				m.toast("The "+c.Name+" code expired. Press Connect to get a new one.", "error")
				done()
			}
			return
		case <-time.After(time.Duration(interval) * time.Second):
		}
		tok, err := postToken(ctx, m.hc, c.TokenURL, url.Values{"client_id": {clientID}, "device_code": {deviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}})
		var te *TokenError
		if errors.As(err, &te) {
			switch te.Code {
			case "authorization_pending":
				continue
			case "slow_down":
				interval += 5
				continue
			case "":
				if te.Unreachable {
					continue
				}
			}
			if ctx.Err() != nil {
				return
			}
			msg := te.Error()
			switch te.Code {
			case "access_denied":
				msg = "You declined access on GitHub. Nothing was connected."
			case "expired_token":
				msg = "The code expired. Press Connect to get a new one."
			}
			m.toast(c.Name+": "+msg, "error")
			done()
			return
		}
		if err != nil {
			continue
		}
		m.toast("Finishing "+c.Name+" setup", "")
		_ = m.finishConnect(ctx, c, tok, c.BaseScopes)
		done()
		go m.host.Focus()
		return
	}
}

// ---------------- tokens ----------------

// SaveToken checks a pasted token (Notion, Slack) and keeps it if it works.
func (m *Manager) SaveToken(id, token string) error {
	c := m.cat[id]
	if c == nil {
		return errors.New("no connector " + id)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		m.Disconnect(id)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	account := ""
	if c.WhoAmI != nil {
		a, err := c.WhoAmI(&call{ctx: ctx, m: m, conn: id, token: token, a: args{}})
		if err != nil {
			var ae *APIError
			if errors.As(err, &ae) || errors.Is(err, ErrReauth) {
				return errors.New("That token didn't work: " + strings.TrimPrefix(err.Error(), "reauth_required"))
			}
			return errors.New("Couldn't check the token: " + err.Error())
		}
		account = a
	}
	if err := m.store.SetToken(id, &Token{AccessToken: token}); err != nil {
		return err
	}
	_ = m.store.Update(id, func(s *ConnState) {
		s.Connected, s.Expired, s.Account, s.ConnectedAt, s.Via = true, false, account, time.Now(), ""
	})
	m.toast(c.Name+" is now connected", "ok")
	m.changed()
	return nil
}

// Disconnect forgets a connector's tokens (the client ID and tool choices stay).
func (m *Manager) Disconnect(id string) {
	c := m.cat[id]
	if c == nil {
		return
	}
	m.CancelConnect(id)
	if tok := m.store.Token(id); tok != nil && id == "google" {
		t := tok.RefreshToken
		if t == "" {
			t = tok.AccessToken
		}
		go func() { // best effort: revoke at Google too
			r, err := m.hc.PostForm("https://oauth2.googleapis.com/revoke", url.Values{"token": {t}})
			if err == nil {
				r.Body.Close()
			}
		}()
	}
	m.store.DeleteToken(id)
	_ = m.store.Update(id, func(s *ConnState) {
		s.Connected, s.Expired, s.Account, s.Scopes, s.Via = false, false, "", nil, ""
	})
	m.mu.Lock()
	for _, g := range c.Groups {
		delete(m.loaded, g)
	}
	m.mu.Unlock()
	m.changed()
}

var ErrNotConnected = errors.New("not connected")

func (m *Manager) markExpired(id string) {
	_ = m.store.Update(id, func(s *ConnState) { s.Expired = true })
	m.changed()
}

// access returns a usable access token, refreshing it when it's (about to be)
// expired or when force is set (after a 401). ErrReauth means: Reconnect.
func (m *Manager) access(ctx context.Context, id string, force bool) (string, error) {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	tok := m.store.Token(id)
	if tok == nil {
		return "", ErrNotConnected
	}
	st := m.store.Conn(id)
	if st.Expired {
		return "", ErrReauth
	}
	due := tok.ExpiresAt > 0 && time.Now().Add(60*time.Second).UnixMilli() >= tok.ExpiresAt
	if !force && !due {
		return tok.AccessToken, nil
	}
	c := m.cat[id]
	if st.Via == "broker" {
		return m.brokerRefresh(ctx, c, tok)
	}
	if tok.RefreshToken == "" || c == nil || c.TokenURL == "" {
		m.markExpired(id)
		return "", ErrReauth
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok.RefreshToken}, "client_id": {st.ClientID}}
	if sec := m.store.ClientSecret(id); sec != "" {
		form.Set("client_secret", sec)
	}
	nt, err := postToken(ctx, m.hc, c.TokenURL, form)
	if err != nil {
		var te *TokenError
		if errors.As(err, &te) && te.Unreachable {
			return "", fmt.Errorf("couldn't reach %s to refresh the sign-in: %s", c.Name, te.Desc)
		}
		m.markExpired(id)
		return "", ErrReauth
	}
	if nt.RefreshToken == "" {
		nt.RefreshToken = tok.RefreshToken
	}
	if nt.Scope == "" {
		nt.Scope = tok.Scope
	}
	if err := m.store.SetToken(id, nt); err != nil {
		return "", err
	}
	if sc := splitScopes(nt.Scope); len(sc) > 0 {
		_ = m.store.Update(id, func(s *ConnState) { s.Scopes = sc })
	}
	return nt.AccessToken, nil
}

// brokerRefresh renews a one-click connection through the sign-in service, which
// holds the client secret. Called with refreshMu held.
func (m *Manager) brokerRefresh(ctx context.Context, c *Connector, tok *Token) (string, error) {
	b := m.getBroker()
	if c == nil || tok.RefreshToken == "" || b == nil {
		if c != nil {
			m.markExpired(c.ID)
		}
		return "", ErrReauth
	}
	nt, err := b.Refresh(ctx, c.ID, tok.RefreshToken)
	if err != nil {
		if errors.Is(err, ErrBrokerUnreachable) {
			return "", fmt.Errorf("couldn't reach Blaze's sign-in service to refresh %s. Check your internet connection", c.Name)
		}
		m.markExpired(c.ID)
		return "", ErrReauth
	}
	if nt.RefreshToken == "" {
		nt.RefreshToken = tok.RefreshToken
	}
	if nt.Scope == "" {
		nt.Scope = tok.Scope
	}
	if err := m.store.SetToken(c.ID, nt); err != nil {
		return "", err
	}
	return nt.AccessToken, nil
}

func missingScopes(need, granted []string) []string {
	if len(granted) == 0 {
		return nil // unknown: let the provider decide
	}
	have := map[string]bool{}
	for _, g := range granted {
		have[g] = true
		for _, i := range implies[g] {
			have[i] = true
		}
	}
	var out []string
	for _, n := range need {
		if !have[n] {
			out = append(out, n)
		}
	}
	return out
}

// ---------------- the agent side ----------------

// group readiness: a connected, enabled, unexpired connector (or an enabled MCP server with tools).
func (m *Manager) readyGroups() []string {
	var out []string
	for _, c := range m.conns {
		st := m.store.Conn(c.ID)
		if st.Disabled || !m.store.HasToken(c.ID) {
			continue
		}
		out = append(out, c.Groups...)
	}
	for _, s := range m.store.MCPList() {
		if !s.Disabled && len(s.Tools) > 0 {
			out = append(out, "mcp:"+s.ID)
		}
	}
	return out
}

// ResetLoaded forgets which tool groups this conversation loaded.
func (m *Manager) ResetLoaded() {
	m.mu.Lock()
	m.loaded = map[string]bool{}
	m.mu.Unlock()
}

// NoteUsed marks the group of a tool call from a saved chat as loaded.
func (m *Manager) NoteUsed(name, raw string) {
	switch {
	case name == metaLoad:
		var a args
		_ = json.Unmarshal([]byte(raw), &a)
		for _, g := range strings.Split(a.s("group"), ",") {
			if g = strings.TrimSpace(g); g != "" {
				m.markLoaded(g)
			}
		}
	case strings.HasPrefix(name, "mcp_"):
		for _, s := range m.store.MCPList() {
			if strings.HasPrefix(name, "mcp_"+s.ID+"_") {
				m.markLoaded("mcp:" + s.ID)
			}
		}
	default:
		if t := m.tools[name]; t != nil {
			m.markLoaded(t.Group)
		}
	}
}

func (m *Manager) markLoaded(g string) {
	m.mu.Lock()
	m.loaded[g] = true
	m.mu.Unlock()
}

func (m *Manager) isLoaded(g string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loaded[g]
}

func toolDef(name, desc string, props map[string]any, req []string) llm.Tool {
	t := llm.Tool{Type: "function"}
	t.Function.Name = name
	t.Function.Description = desc
	if props == nil {
		props = map[string]any{}
	}
	if req == nil {
		req = []string{}
	}
	t.Function.Parameters = map[string]any{"type": "object", "properties": props, "required": req}
	return t
}

const metaLoad, metaSuggest = "connector_tools", "suggest_connector"

// Tools is what the model sees this step: the two meta tools, plus the tools of
// the groups this conversation loaded (only connected, enabled ones, never "off").
func (m *Manager) Tools() []llm.Tool {
	ready := m.readyGroups()
	var out []llm.Tool
	if len(ready) > 0 {
		out = append(out, toolDef(metaLoad, "Load the tools of one of the user's connected accounts or MCP servers before using them. Groups: "+strings.Join(ready, ", ")+".",
			map[string]any{"group": enum("Tool group to load", ready...)}, []string{"group"}))
	}
	ids := []string{}
	for _, c := range m.conns {
		ids = append(ids, c.ID)
	}
	out = append(out, toolDef(metaSuggest, "When a request needs an account that isn't connected or is turned off (e.g. email for Gmail), show the user a card to connect or enable it, instead of guessing.",
		map[string]any{"connector": enum("Connector", ids...), "reason": str("One short sentence: what it would let Blaze do for this request")}, []string{"connector", "reason"}))
	for _, g := range ready {
		if !m.isLoaded(g) {
			continue
		}
		out = append(out, m.groupTools(g)...)
	}
	return out
}

func (m *Manager) groupTools(g string) []llm.Tool {
	var out []llm.Tool
	if sid, ok := strings.CutPrefix(g, "mcp:"); ok {
		s, ok := m.store.MCP(sid)
		if !ok {
			return nil
		}
		for _, t := range s.Tools {
			if effMode(s.Modes[t.Name], mcpRisk(t)) == "off" {
				continue
			}
			lt := llm.Tool{Type: "function"}
			lt.Function.Name = mcpToolName(s.ID, t.Name)
			lt.Function.Description = clip("["+s.Name+"] "+t.Description, 900)
			lt.Function.Parameters = t.Schema
			if lt.Function.Parameters == nil {
				lt.Function.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			out = append(out, lt)
		}
		return out
	}
	for _, n := range m.order {
		t := m.tools[n]
		if t.Group != g {
			continue
		}
		st := m.store.Conn(t.Connector)
		if effMode(st.Modes[t.Name], t.Risk) == "off" {
			continue
		}
		out = append(out, toolDef(t.Name, t.Desc, t.Props, t.Required))
	}
	return out
}

// Known lists every name the gatekeeper should accept: the meta tools and every
// tool of a ready group (loaded or not), so a direct call still resolves.
func (m *Manager) Known() []string {
	out := []string{metaLoad, metaSuggest}
	for _, g := range m.readyGroups() {
		for _, t := range m.groupTools(g) {
			out = append(out, t.Function.Name)
		}
	}
	return out
}

// Handles says whether a tool name belongs to the connectors.
func (m *Manager) Handles(name string) bool {
	if name == metaLoad || name == metaSuggest || strings.HasPrefix(name, "mcp_") {
		return true
	}
	_, ok := m.tools[name]
	return ok
}

// PromptBlock is the CONNECTORS section of the system prompt.
func (m *Manager) PromptBlock() string {
	var ready, expired, off, missing []string
	for _, c := range m.conns {
		st := m.store.Conn(c.ID)
		tok := m.store.HasToken(c.ID)
		switch {
		case !tok:
			missing = append(missing, c.Name)
		case st.Disabled:
			off = append(off, c.Name)
		case st.Expired:
			expired = append(expired, c.Name)
		default:
			acct := ""
			if st.Account != "" {
				acct = " as " + st.Account
			}
			var gs []string
			for _, g := range c.Groups {
				l := g
				if m.isLoaded(g) {
					l += " (loaded)"
				}
				gs = append(gs, l)
			}
			ready = append(ready, c.Name+acct+": groups "+strings.Join(gs, ", "))
		}
	}
	for _, s := range m.store.MCPList() {
		if s.Disabled || len(s.Tools) == 0 {
			continue
		}
		l := "mcp:" + s.ID
		if m.isLoaded(l) {
			l += " (loaded)"
		}
		ready = append(ready, "MCP server "+s.Name+": group "+l+" ("+fmt.Sprint(len(s.Tools))+" tools)")
	}
	var b strings.Builder
	b.WriteString("\nCONNECTORS (the user's own accounts)\n")
	if len(ready) > 0 {
		b.WriteString("- Connected: " + strings.Join(ready, "; ") + ".\n")
		b.WriteString("- Call connector_tools with a group to load its tools before using them; loaded groups stay for this chat.\n")
	}
	if len(expired) > 0 {
		b.WriteString("- Expired (the user must Reconnect in Connectors): " + strings.Join(expired, ", ") + ".\n")
	}
	if len(off) > 0 {
		b.WriteString("- Turned off by the user: " + strings.Join(off, ", ") + ".\n")
	}
	if len(missing) > 0 {
		b.WriteString("- Not connected: " + strings.Join(missing, ", ") + ". If a request needs one, call suggest_connector instead of guessing.\n")
	}
	b.WriteString("- Sending, posting and creating things show the user an approval card. Never say something was sent unless the tool says so.\n")
	return b.String()
}

// Run executes a connector, MCP or meta tool call through the approval gate.
func (m *Manager) Run(ctx context.Context, name, raw string) string {
	a := args{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			return "Error in " + name + ": the arguments weren't valid JSON. Call it again with a JSON object."
		}
	}
	switch name {
	case metaLoad:
		return m.loadGroups(a)
	case metaSuggest:
		return m.suggest(a)
	}
	if strings.HasPrefix(name, "mcp_") {
		return m.runMCP(ctx, name, a)
	}
	t := m.tools[name]
	if t == nil {
		return "Error: there is no tool called " + name + "."
	}
	c := m.cat[t.Connector]
	st := m.store.Conn(c.ID)
	mode := effMode(st.Modes[t.Name], t.Risk)
	if mode == "off" {
		return "Error: the user turned " + name + " off in Connectors. Don't use it; say so if it's needed."
	}
	if m.store.Token(c.ID) == nil {
		m.emit("connector_suggest", map[string]string{"id": c.ID, "name": c.Name, "action": "connect", "reason": "Blaze needs " + c.Name + " for this (" + strings.ReplaceAll(name, "_", " ") + ")."})
		return "Error: " + c.Name + " isn't connected. Blaze showed the user a Connect button; continue after they connect it, or do the task another way."
	}
	if st.Disabled {
		m.emit("connector_suggest", map[string]string{"id": c.ID, "name": c.Name, "action": "enable", "reason": c.Name + " is turned off for Blaze."})
		return "Error: the user turned " + c.Name + " off for Blaze. Blaze showed them an Enable button."
	}
	if st.Expired {
		return m.reauth(c)
	}
	if miss := missingScopes(t.Scopes, st.Scopes); len(miss) > 0 {
		return m.consent(c, t, miss)
	}
	m.markLoaded(t.Group)
	if !m.approve(ctx, c.ID, c.Name, t.Name, t.Risk, mode, st.Modes[t.Name] == "", t.Ask, a) {
		return "The user declined " + name + ". Nothing was done."
	}
	tok, err := m.access(ctx, c.ID, false)
	if errors.Is(err, ErrReauth) {
		return m.reauth(c)
	}
	if err != nil {
		return "Error in " + name + ": " + err.Error()
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := t.Run(&call{ctx: cctx, m: m, conn: c.ID, token: tok, a: a})
	if errors.Is(err, ErrReauth) {
		return m.reauth(c)
	}
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 403 && regexp.MustCompile(`(?i)insufficient|SCOPE`).MatchString(ae.Body) && len(t.Scopes) > 0 {
		return m.consent(c, t, t.Scopes)
	}
	if err != nil {
		return "Error in " + name + ": " + err.Error()
	}
	return out
}

func (m *Manager) reauth(c *Connector) string {
	m.emit("connector_reauth", map[string]string{"id": c.ID, "name": c.Name})
	return "Error: the user's " + c.Name + " connection has expired. Blaze showed them a Reconnect button. Don't retry until they reconnect; tell them in one line."
}

func (m *Manager) consent(c *Connector, t *Tool, scopes []string) string {
	var labels []string
	for _, s := range scopes {
		labels = append(labels, ScopeLabel(s))
	}
	m.emit("connector_consent", map[string]any{"id": c.ID, "name": c.Name, "tool": t.Name, "scopes": labels})
	return "Error: " + c.Name + " hasn't given Blaze permission to " + strings.Join(labels, ", ") + " yet. Blaze asked the user to grant it (a Grant access button in the chat). Wait for them, then try " + t.Name + " again."
}

// approve is the gate: Always allow runs; Ask shows the card (with Always allow);
// Action tools also run when auto-approve is on and the user kept the default.
func (m *Manager) approve(ctx context.Context, owner, ownerName, tool string, r Risk, mode string, isDefault bool, ask func(args) (string, string), a args) bool {
	if mode == "always" {
		return true
	}
	if r == Action && isDefault && m.host.Yolo() {
		return true
	}
	title, detail := "", ""
	if ask != nil {
		title, detail = ask(a)
	}
	if title == "" {
		title = "Allow " + ownerName + " · " + tool + "?"
	}
	if detail == "" {
		b, _ := json.MarshalIndent(a, "", "  ")
		detail = clip(string(b), 1200)
	}
	var yes, always bool
	if m.host.ConfirmCtx != nil {
		yes, always = m.host.ConfirmCtx(ctx, title, detail, true)
	} else {
		yes, always = m.host.Confirm(title, detail, true)
	}
	if yes && always {
		_ = m.SetToolMode(owner, tool, "always")
	}
	return yes
}

func (m *Manager) loadGroups(a args) string {
	ready := map[string]bool{}
	for _, g := range m.readyGroups() {
		ready[g] = true
	}
	var b strings.Builder
	for _, g := range strings.Split(a.s("group"), ",") {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if !ready[g] {
			fmt.Fprintf(&b, "%s isn't connected or is turned off. Ready groups: %s.\n", g, strings.Join(m.readyGroups(), ", "))
			continue
		}
		m.markLoaded(g)
		fmt.Fprintf(&b, "Loaded %s:\n", g)
		for _, t := range m.groupTools(g) {
			fmt.Fprintf(&b, "- %s: %s\n", t.Function.Name, clip(t.Function.Description, 140))
		}
	}
	if b.Len() == 0 {
		return "Error: name a group to load: " + strings.Join(m.readyGroups(), ", ")
	}
	return b.String() + "Call them directly now."
}

func (m *Manager) suggest(a args) string {
	c := m.cat[a.s("connector")]
	if c == nil {
		return "Error: unknown connector " + a.s("connector")
	}
	st := m.store.Conn(c.ID)
	action := "connect"
	switch {
	case m.store.Token(c.ID) != nil && st.Expired:
		return m.reauth(c)
	case m.store.Token(c.ID) != nil && !st.Disabled:
		return c.Name + " is already connected: load its tools with connector_tools."
	case m.store.Token(c.ID) != nil:
		action = "enable"
	}
	m.emit("connector_suggest", map[string]string{"id": c.ID, "name": c.Name, "action": action, "reason": a.s("reason")})
	return "Blaze showed the user a card to " + action + " " + c.Name + ". Tell them in one line why it helps, then wait for them."
}

// ---------------- MCP ----------------

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// MCPSpec is the add-server form.
type MCPSpec struct {
	Name      string `json:"name"`
	Transport string `json:"transport"` // http | sse | stdio
	URL       string `json:"url"`
	Header    string `json:"header"`
	Token     string `json:"token"`
	Command   string `json:"command"`
	Args      string `json:"args"` // space separated; "quoted parts" stay together
}

func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quote := rune(0)
	has := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, has = r, true
		case quote == 0 && (r == ' ' || r == '\t' || r == '\n'):
			if has || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if has || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// AddMCP saves a server and checks it right away.
func (m *Manager) AddMCP(sp MCPSpec) (MCPView, error) {
	sp.Name = strings.TrimSpace(sp.Name)
	sp.URL = strings.TrimSpace(sp.URL)
	sp.Command = strings.TrimSpace(sp.Command)
	switch sp.Transport {
	case "http", "sse":
		u, err := url.Parse(sp.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return MCPView{}, errors.New("Enter the server's full URL, e.g. https://example.com/mcp")
		}
		if u.Scheme == "http" && !isLocalHost(u.Hostname()) {
			return MCPView{}, errors.New("Use https for a remote server (plain http is only allowed for localhost)")
		}
		if sp.Name == "" {
			sp.Name = u.Hostname()
		}
	case "stdio":
		if sp.Command == "" {
			return MCPView{}, errors.New("Enter the command that starts the server, e.g. npx")
		}
		if sp.Name == "" {
			sp.Name = sp.Command
		}
	default:
		return MCPView{}, errors.New("transport must be http, sse or stdio")
	}
	id := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(sp.Name), ""), "")
	if len(id) > 12 {
		id = id[:12]
	}
	if id == "" {
		id = "srv"
	}
	base := id
	for i := 2; ; i++ {
		if _, ok := m.store.MCP(id); !ok {
			break
		}
		id = fmt.Sprintf("%s%d", base, i)
	}
	s := MCPServer{ID: id, Name: sp.Name, Transport: sp.Transport, URL: sp.URL, Header: strings.TrimSpace(sp.Header), Command: sp.Command, Args: splitArgs(sp.Args)}
	if err := m.store.SetMCPToken(id, sp.Token); err != nil {
		return MCPView{}, err
	}
	if err := m.store.PutMCP(s); err != nil {
		return MCPView{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v, _ := m.RefreshMCP(ctx, id)
	return v, nil
}

func isLocalHost(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// RemoveMCP forgets a server and its key.
func (m *Manager) RemoveMCP(id string) error {
	m.dropClient(id)
	m.mu.Lock()
	delete(m.loaded, "mcp:"+id)
	m.mu.Unlock()
	defer m.changed()
	return m.store.RemoveMCP(id)
}

// SetMCPEnabled turns a server's tools on or off for Blaze.
func (m *Manager) SetMCPEnabled(id string, on bool) error {
	if !on {
		m.dropClient(id)
	}
	defer m.changed()
	return m.store.UpdateMCP(id, func(s *MCPServer) { s.Disabled = !on })
}

func (m *Manager) dropClient(id string) {
	m.mu.Lock()
	c := m.mcp[id]
	delete(m.mcp, id)
	m.mu.Unlock()
	if c != nil {
		go c.close()
	}
}

func (m *Manager) mcpHeaders(s MCPServer) map[string]string {
	h := map[string]string{}
	tok := m.store.MCPToken(s.ID)
	if tok == "" {
		return h
	}
	name := s.Header
	if name == "" {
		name = "Authorization"
	}
	if strings.EqualFold(name, "Authorization") && !strings.HasPrefix(strings.ToLower(tok), "bearer ") && !strings.HasPrefix(strings.ToLower(tok), "basic ") {
		tok = "Bearer " + tok
	}
	h[name] = tok
	return h
}

// client returns a live, initialized connection to a server.
func (m *Manager) client(ctx context.Context, s MCPServer) (*mcpClient, error) {
	m.mu.Lock()
	c := m.mcp[s.ID]
	m.mu.Unlock()
	if c != nil {
		return c, nil
	}
	var tr transport
	switch s.Transport {
	case "http":
		tr = &httpTransport{url: s.URL, hdr: m.mcpHeaders(s), hc: m.hc}
	case "sse":
		p, err := dialSSE(ctx, m.hc, s.URL, m.mcpHeaders(s))
		if err != nil {
			return nil, err
		}
		tr = p
	case "stdio":
		p, logs, err := dialStdio(s.Command, s.Args, m.host.Dir())
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.mcpLogs[s.ID] = logs
		m.mu.Unlock()
		tr = p
	default:
		return nil, errors.New("unknown transport " + s.Transport)
	}
	c = newMCPClient(tr)
	if err := c.initialize(ctx, m.host.Version); err != nil {
		c.close()
		if s.Transport == "stdio" {
			m.mu.Lock()
			lg := m.mcpLogs[s.ID]
			m.mu.Unlock()
			if lg != nil && strings.TrimSpace(lg.String()) != "" {
				err = fmt.Errorf("%v (%s)", err, clip(lg.String(), 300))
			}
		}
		return nil, err
	}
	m.mu.Lock()
	m.mcp[s.ID] = c
	m.mu.Unlock()
	return c, nil
}

// RefreshMCP reconnects to a server and re-reads its tools.
func (m *Manager) RefreshMCP(ctx context.Context, id string) (MCPView, error) {
	m.dropClient(id)
	s, ok := m.store.MCP(id)
	if !ok {
		return MCPView{}, errors.New("no MCP server " + id)
	}
	c, err := m.client(ctx, s)
	var tools []MCPTool
	if err == nil {
		tools, err = c.listTools(ctx)
	}
	_ = m.store.UpdateMCP(id, func(x *MCPServer) {
		x.Checked = time.Now()
		if err != nil {
			x.Error = err.Error()
		} else {
			x.Error, x.Tools = "", tools
		}
	})
	m.changed()
	s, _ = m.store.MCP(id)
	return m.mcpView(s), err
}

func (m *Manager) runMCP(ctx context.Context, name string, a args) string {
	var srv MCPServer
	var tool *MCPTool
	for _, s := range m.store.MCPList() {
		for i := range s.Tools {
			if mcpToolName(s.ID, s.Tools[i].Name) == name {
				srv, tool = s, &s.Tools[i]
				break
			}
		}
		if tool != nil {
			break
		}
	}
	if tool == nil {
		return "Error: the MCP tool " + name + " isn't available any more. Load the server's group again with connector_tools."
	}
	if srv.Disabled {
		return "Error: the user turned the MCP server " + srv.Name + " off."
	}
	r := mcpRisk(*tool)
	mode := effMode(srv.Modes[tool.Name], r)
	if mode == "off" {
		return "Error: the user turned " + tool.Name + " off in Connectors."
	}
	m.markLoaded("mcp:" + srv.ID)
	if !m.approve(ctx, "mcp:"+srv.ID, srv.Name, tool.Name, r, mode, srv.Modes[tool.Name] == "", nil, a) {
		return "The user declined " + tool.Name + ". Nothing was done."
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		c, err := m.client(cctx, srv)
		if err != nil {
			return "Error: couldn't reach the MCP server " + srv.Name + ": " + err.Error()
		}
		out, err := c.callTool(cctx, tool.Name, map[string]any(a))
		if err != nil {
			if attempt == 0 && (errors.Is(err, errSessionGone) || strings.Contains(err.Error(), "went away") || strings.Contains(err.Error(), "exited") || strings.Contains(err.Error(), "closed")) {
				m.dropClient(srv.ID)
				continue
			}
			return "Error from " + srv.Name + ": " + err.Error()
		}
		if len(out) > 30000 {
			out = out[:30000] + "\n…(cut)"
		}
		return out
	}
	return "Error: the MCP server " + srv.Name + " kept dropping the connection."
}

// ConnectedCount is how many connectors and MCP servers are ready (the sidebar badge).
func (m *Manager) ConnectedCount() int {
	n := 0
	for _, c := range m.conns {
		if m.store.HasToken(c.ID) && !m.store.Conn(c.ID).Disabled {
			n++
		}
	}
	for _, s := range m.store.MCPList() {
		if !s.Disabled && len(s.Tools) > 0 {
			n++
		}
	}
	return n
}
