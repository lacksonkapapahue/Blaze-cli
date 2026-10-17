package main

// Blaze accounts and connectors in the terminal: blaze login / logout / whoami /
// connect, through blaze-auth (the Cloudflare Worker in the desktop repo's cloud/).
// The session and connector tokens use the same store as Blaze Desktop: the OS
// keychain under "blaze-cli", with a 0600 file fallback in the Blaze home; the
// signed-in user is cached in ~/.blaze/desktop/account.json and connector state in
// ~/.blaze/desktop/, so signing in on one signs in on the other.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/lacksonkapapahue/blaze-cli/internal/cloud"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/connect"
	"github.com/lacksonkapapahue/blaze-cli/internal/keystore"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

const sessionKey = "blaze-account:session"

func sharedDir() string   { return filepath.Join(config.Dir(), "desktop") }
func accountFile() string { return filepath.Join(sharedDir(), "account.json") }

// openURL opens a link in the default browser (best effort; the link is printed too).
func openURL(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}

func openOAuthURL(u string) {
	if _, ok := os.LookupEnv("CODESPACES"); ok {
		if browser := strings.Fields(os.Getenv("BROWSER")); len(browser) > 0 {
			args := append(browser[1:], u)
			if err := exec.Command(browser[0], args...).Start(); err == nil {
				return
			}
		}
	}
	openURL(u)
}

func isRemoteSession() bool {
	for _, key := range []string{"CODESPACES", "SSH_CONNECTION", "SSH_TTY", "REMOTE_CONTAINERS"} {
		if _, ok := os.LookupEnv(key); ok {
			return true
		}
	}
	return false
}

func pasteOAuthCallback(sp *ui.Spinner, waiting string) func(context.Context) <-chan string {
	return func(ctx context.Context) <-chan string {
		lines := make(chan string)
		sp.Stop()
		go func() {
			defer close(lines)
			line, err := ui.ReadLineContext(ctx, "  Paste the browser address and press Enter: ")
			if err == nil {
				if ctx.Err() == nil {
					sp.Start(waiting)
				}
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			} else if ctx.Err() == nil {
				ui.Println("  " + ui.Weak("Could not read the sign-in address: "+err.Error()))
				sp.Start(waiting)
			}
		}()
		return lines
	}
}

func printOAuthInstructions() {
	if isRemoteSession() {
		ui.Println("  This is a remote machine. After you approve, your browser shows a page that can't load (127.0.0.1).")
		ui.Println("  Copy that page's full address and paste it here, then press Enter.")
		return
	}
	ui.Println("  " + ui.Weak("If the browser ends on a page that can't load, paste its address here."))
}

// account is the Worker client plus the shared secret store.
type account struct {
	client *cloud.Client
	sec    connect.Secrets

	mu      sync.Mutex
	prov    *cloud.Providers
	checked time.Time
	online  bool
}

var acctOnce sync.Once
var acct *account

func getAccount() *account {
	acctOnce.Do(func() {
		c := cloud.New(cloud.BaseURL(sharedDir()))
		c.Name = "Blaze CLI"
		acct = &account{client: c, sec: connect.Keychain()}
	})
	return acct
}

func (ac *account) providers(force bool) (*cloud.Providers, bool) {
	ac.mu.Lock()
	if !force && !ac.checked.IsZero() && time.Since(ac.checked) < 10*time.Minute {
		p, on := ac.prov, ac.online
		ac.mu.Unlock()
		return p, on
	}
	ac.checked = time.Now()
	ac.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := ac.client.Providers(ctx)
	ac.mu.Lock()
	defer ac.mu.Unlock()
	ac.prov, ac.online = p, err == nil
	return ac.prov, ac.online
}

func (ac *account) cachedUser() *cloud.User {
	b, err := os.ReadFile(accountFile())
	if err != nil {
		return nil
	}
	var u cloud.User
	if json.Unmarshal(b, &u) != nil || u.ID == "" {
		return nil
	}
	return &u
}

func (ac *account) setSession(s *cloud.Session) error {
	if s == nil {
		if err := ac.sec.Delete(sessionKey); err != nil {
			return err
		}
		_ = os.Remove(accountFile())
		return nil
	}
	if err := ac.sec.Set(sessionKey, s.Token); err != nil {
		return err
	}
	_ = os.MkdirAll(sharedDir(), 0o700)
	b, _ := json.MarshalIndent(s.User, "", "  ")
	return os.WriteFile(accountFile(), b, 0o600)
}

func userLine(u *cloud.User) string {
	if u == nil {
		return ""
	}
	if u.Name != "" {
		return u.Name + " <" + u.Email + ">"
	}
	return u.Email
}

func fail(msg string) {
	ui.Println("  " + ui.Ink("■ ") + ui.Sub(msg))
}

// cmdLogin is `blaze login [email|google|github|--signup]`.
func cmdLogin(args []string) error {
	ac := getAccount()
	method := ""
	if len(args) > 0 {
		method = strings.TrimLeft(strings.ToLower(args[0]), "-")
	}
	// Blaze Cloud: `blaze login --force` (or "again") signs in afresh, and a saved
	// session the service refuses (401) no longer blocks signing in again.
	if method == "force" || method == "again" {
		if err := ac.setSession(nil); err != nil {
			return err
		}
		method = ""
		if len(args) > 1 {
			method = strings.TrimLeft(strings.ToLower(args[1]), "-")
		}
	}
	if tok, _ := ac.sec.Get(sessionKey); tok != "" {
		if u := ac.cachedUser(); u != nil {
			if _, _, err := whoCheck(ac, tok); !sessionRefused(err) { // offline/5xx keep it
				ui.StepDone(true, "Already signed in as "+userLine(u), "blaze logout to switch", 0)
				return nil
			}
			_ = ac.setSession(nil) // refused (401): sign in afresh
		}
	}
	prov, online := ac.providers(true)
	if !online {
		return errors.New("couldn't reach Blaze's sign-in service (" + ac.client.Base + "). Check your internet connection")
	}
	if method == "" {
		opts := []string{"Email"}
		keys := []string{"email"}
		for _, p := range []string{"google", "github"} {
			if prov != nil && prov.Login[p] {
				opts = append(opts, titleCase(p))
				keys = append(keys, p)
			}
		}
		opts, keys = append(opts, "Create account"), append(keys, "signup")
		i := ui.Choose("  "+ui.Ink("Sign in with")+"  ", opts, 0, -1)
		if i < 0 {
			return errors.New("sign-in cancelled. Run `blaze login email` to sign in with email")
		}
		if i >= len(keys) {
			return errors.New("invalid sign-in method selected")
		}
		method = keys[i]
	}
	var s *cloud.Session
	var err error
	switch method {
	case "email", "signin", "signup":
		s, err = emailLogin(ac, method == "signup")
	case "google", "github":
		if prov == nil || !prov.Login[method] {
			return errors.New("Sign in with " + titleCase(method) + " isn't switched on yet. Use `blaze login email`")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 11*time.Minute)
		defer cancel()
		var sp ui.Spinner
		waiting := "waiting for " + titleCase(method) + " in the browser"
		previousPaste := ac.client.Paste
		ac.client.Paste = pasteOAuthCallback(&sp, waiting)
		defer func() { ac.client.Paste = previousPaste }()
		s, err = ac.client.SignInWith(ctx, method, func(u string) {
			ui.Println("  " + ui.Sub("Opening your browser. If it doesn't open, visit:"))
			ui.Println("  " + ui.Accent(u))
			printOAuthInstructions()
			openOAuthURL(u)
			sp.Start(waiting)
		})
		sp.Stop()
	default:
		return errors.New("use: blaze login [email|google|github|--signup] [--force]")
	}
	if err != nil {
		return errors.New(cloud.Message(err))
	}
	if s == nil {
		return nil
	}
	if err := ac.setSession(s); err != nil {
		return err
	}
	if connect.UsedFileFallback() && term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprintln(os.Stderr, "  "+ui.Weak("No system keychain here, so Blaze keeps your sign-in in ~/.blaze/secrets.json (only you can read it)."))
	}
	ui.StepDone(true, "Signed in as "+userLine(&s.User), "Blaze Cloud is ready · no API keys needed · shared with Blaze Desktop", 0)
	if r, err := syncKeys(20 * time.Second); err == nil {
		reportSync(r, nil)
	} else if !errors.Is(err, keystore.ErrUnsupported) {
		reportSync(r, err)
	}
	return nil
}

func emailLogin(ac *account, signup bool) (*cloud.Session, error) {
	name := ""
	if signup {
		name, _ = ui.ReadLine("  " + ui.Ink("Name: "))
	}
	email, err := ui.ReadLine("  " + ui.Ink("Email: "))
	if err != nil {
		return nil, nil
	}
	email = strings.TrimSpace(email)
	pw := ui.Secret("  " + ui.Ink("Password: "))
	if email == "" || pw == "" {
		return nil, errors.New("enter your email and password")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if signup {
		if len(pw) < 8 {
			return nil, errors.New("use a password of at least 8 characters")
		}
		return ac.client.SignUp(ctx, strings.TrimSpace(name), email, pw)
	}
	return ac.client.SignIn(ctx, email, pw)
}

func cmdLogout() error {
	ac := getAccount()
	if tok, _ := ac.sec.Get(sessionKey); tok != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = ac.client.Logout(ctx, tok) // best effort: offline still signs out here
		cancel()
	}
	if err := ac.setSession(nil); err != nil {
		return err
	}
	ui.StepDone(true, "Signed out", "here and in Blaze Desktop", 0)
	return nil
}

func cmdWhoami() error {
	ac := getAccount()
	tok, _ := ac.sec.Get(sessionKey)
	if tok == "" {
		ui.Println("  " + ui.Sub("Not signed in. ") + ui.Accent("blaze login") + ui.Sub(" to sign in (free, no API keys needed)."))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	u, err := ac.client.Me(ctx, tok)
	var ae *cloud.APIError
	switch {
	case err == nil:
		_ = os.MkdirAll(sharedDir(), 0o700)
		b, _ := json.MarshalIndent(u, "", "  ")
		_ = os.WriteFile(accountFile(), b, 0o600)
	case errors.As(err, &ae) && ae.Status == 401:
		_ = ac.setSession(nil)
		ui.Println("  " + ui.Sub("Your session ended (signed out elsewhere). ") + ui.Accent("blaze login") + ui.Sub(" to sign in again."))
		return nil
	default:
		u = ac.cachedUser()
		if u == nil {
			return errors.New(cloud.Message(err))
		}
		ui.Println("  " + ui.Weak("(offline: showing the saved account)"))
	}
	rows := []string{ui.Ink(userLine(u))}
	if len(u.Providers) > 0 {
		rows = append(rows, ui.Sub("sign-ins: "+strings.Join(u.Providers, ", ")))
	}
	rows = append(rows, ui.Sub(cloudRouteRow()))
	rows = append(rows, ui.Sub("service: "+ac.client.Base))
	ui.Box(ui.AccentLabel("blaze account"), rows)
	return nil
}

func titleCase(s string) string {
	switch s {
	case "github":
		return "GitHub"
	case "google":
		return "Google"
	}
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ---------------- connectors ----------------

var connOnce sync.Once
var conns *connect.Manager

// connEvents carries the connector manager's events to whoever waits on a sign-in.
var connEvents = make(chan connEvent, 32)

type connEvent struct {
	name string
	data any
}

// confirmHook routes a connector approval (the agent wiring replaces it).
var confirmHook = func(ctx context.Context, title, detail string, always bool) (bool, bool) {
	ui.Box(ui.AccentLabel(title), []string{ui.Ink(detail)})
	switch ui.Choose("  "+ui.Accent("?")+" ", []string{"Yes", "Always allow", "No"}, 2, 2) {
	case 0:
		return true, false
	case 1:
		return true, true
	}
	return false, false
}

// autopilotHook says whether the approval mode is Autopilot (set by the REPL).
var autopilotHook = func() bool { return false }

func getConns() *connect.Manager {
	connOnce.Do(func() {
		conns = connect.New(sharedDir(), connect.Keychain(), connect.Host{
			Emit: func(ev string, d any) {
				select {
				case connEvents <- connEvent{ev, d}:
				default:
				}
				connectorNotice(ev, d)
			},
			OpenURL: func(u string) {
				// Only user-started sign-ins open a URL, so the prompt isn't open: print directly.
				ui.Println("  " + ui.Sub("Opening your browser. If it doesn't open, visit:"))
				ui.Println("  " + ui.Accent(u))
				printOAuthInstructions()
				openOAuthURL(u)
			},
			ConfirmCtx: func(ctx context.Context, title, detail string, always bool) (bool, bool) {
				return confirmHook(ctx, title, detail, always)
			},
			Yolo:    func() bool { return autopilotHook() },
			Dir:     func() string { d, _ := os.Getwd(); return d },
			Version: version,
		})
		conns.SetBroker(&broker{ac: getAccount()})
	})
	return conns
}

// connectorNotice turns the manager's UI events into terminal lines.
func connectorNotice(ev string, d any) {
	m, _ := d.(map[string]string)
	switch ev {
	case "connector_suggest", "connector_reauth":
		if m != nil {
			ui.Above("  " + ui.Accent("■ ") + ui.Ink(m["name"]+" isn't ready. ") + ui.Sub("Run ") + ui.Accent("/connect "+m["id"]) + ui.Sub(" to connect it."))
		}
	case "connector_consent":
		if mm, ok := d.(map[string]any); ok {
			ui.Above("  " + ui.Accent("■ ") + ui.Ink(fmt.Sprint(mm["name"])+" needs more access. ") + ui.Sub("Run ") + ui.Accent(fmt.Sprintf("/connect %v", mm["id"])) + ui.Sub(" to grant it."))
		}
	}
}

// cmdConnect is `blaze connect [id] | blaze connect <id> --manual | blaze disconnect <id>`.
func cmdConnect(args []string) error {
	m := getConns()
	if len(args) == 0 {
		listConnectors(m)
		return nil
	}
	id := strings.ToLower(args[0])
	manual := len(args) > 1 && (args[1] == "--manual" || args[1] == "manual")
	var cv *connect.ConnectorView
	v := m.Views()
	for i := range v.Connectors {
		if v.Connectors[i].ID == id {
			cv = &v.Connectors[i]
		}
	}
	if cv == nil {
		return errors.New("there's no connector called " + id + "; `blaze connect` lists them")
	}
	if cv.Auth == connect.AuthToken {
		tok := ui.Secret("  " + ui.Ink("Paste your "+cv.Name+" token: "))
		if tok == "" {
			return nil
		}
		if err := m.SaveToken(id, tok); err != nil {
			return err
		}
		ui.StepDone(true, cv.Name+" connected", "", 0)
		return nil
	}
	// Drain stale events.
	for len(connEvents) > 0 {
		<-connEvents
	}
	var r connect.ConnectResult
	sp := ui.Spinner{}
	waiting := "waiting for " + cv.Name + " in the browser"
	ac := getAccount()
	previousPaste := ac.client.Paste
	ac.client.Paste = pasteOAuthCallback(&sp, waiting)
	defer func() { ac.client.Paste = previousPaste }()
	sp.Start(waiting)
	if manual {
		r = m.ConnectManual(id)
	} else {
		r = m.Connect(id)
	}
	switch r.Status {
	case "error":
		sp.Stop()
		return errors.New(r.Error)
	case "connected":
		sp.Stop()
		ui.StepDone(true, cv.Name+" connected", "", 0)
		return nil
	}
	if r.UserCode != "" {
		ui.Println("  " + ui.Ink("Enter the code ") + ui.Accent(r.UserCode) + ui.Ink(" at ") + ui.Accent(r.URL))
	}
	defer sp.Stop()
	deadline := time.After(11 * time.Minute)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case ev := <-connEvents:
			if ev.name == "toast" {
				if t, ok := ev.data.(map[string]string); ok && t["kind"] == "error" {
					sp.Stop()
					return errors.New(t["text"])
				}
			}
		case <-tick.C:
			for _, c := range m.Views().Connectors {
				if c.ID == id && c.Status == "connected" && c.Pending == nil {
					sp.Stop()
					acc := c.Account
					ui.StepDone(true, cv.Name+" connected", acc, 0)
					return nil
				}
			}
		case <-deadline:
			m.CancelConnect(id)
			return errors.New("the sign-in timed out")
		}
	}
}

func cmdDisconnect(args []string) error {
	if len(args) == 0 {
		return errors.New("use: blaze disconnect <connector>")
	}
	getConns().Disconnect(strings.ToLower(args[0]))
	ui.StepDone(true, "Disconnected "+args[0], "", 0)
	return nil
}

func listConnectors(m *connect.Manager) {
	v := m.Views()
	sort.SliceStable(v.Connectors, func(i, j int) bool { return v.Connectors[i].ID < v.Connectors[j].ID })
	var rows []string
	for _, c := range v.Connectors {
		st := c.Status
		mark := ui.Weak("□ ")
		if st == "connected" {
			mark = ui.Accent("■ ")
			if c.Account != "" {
				st += " · " + c.Account
			}
		} else if c.Cloud == "ready" {
			st = "one-click"
		} else if st == "not_connected" || st == "needs_setup" {
			st = "manual setup (--manual)"
		}
		rows = append(rows, mark+ui.Ink(fmt.Sprintf("%-10s", c.ID))+ui.Sub(" "+st))
	}
	for _, s := range v.MCP {
		rows = append(rows, ui.Accent("◆ ")+ui.Ink(fmt.Sprintf("%-10s", s.Name))+ui.Sub(" MCP · "+s.Status))
	}
	ui.Box(ui.AccentLabel("connectors"), rows)
	ui.Println("  " + ui.Weak("blaze connect <id> to connect · blaze disconnect <id> · --manual uses your own developer app"))
}

// ---------------- the connectors' broker ----------------

// broker adapts cloud.Client to connect.Broker (same as the desktop's).
type broker struct{ ac *account }

func (b *broker) Known() bool {
	b.ac.mu.Lock()
	defer b.ac.mu.Unlock()
	return b.ac.online && b.ac.prov != nil
}

func (b *broker) Available(id string) bool {
	p, on := b.ac.providers(false)
	return on && p != nil && p.Connectors[id]
}

func toToken(t *cloud.Tokens) *connect.Token {
	tok := &connect.Token{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, TokenType: t.TokenType, Scope: t.Scope}
	if t.ExpiresIn > 0 {
		tok.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second).UnixMilli()
	}
	return tok
}

func (b *broker) Connect(ctx context.Context, id string, scopes []string, open func(string)) (*connect.Token, []string, string, error) {
	r, err := b.ac.client.Connect(ctx, id, scopes, open)
	if err != nil {
		if errors.Is(err, cloud.ErrNotConfigured) {
			go b.ac.providers(true)
		}
		return nil, nil, "", err
	}
	if r.Tokens == nil || r.Tokens.AccessToken == "" {
		return nil, nil, "", errors.New("the sign-in service sent no token back")
	}
	return toToken(r.Tokens), r.Scopes, r.Tokens.Account, nil
}

func (b *broker) Refresh(ctx context.Context, id, rt string) (*connect.Token, error) {
	t, err := b.ac.client.Refresh(ctx, id, rt)
	if err != nil {
		var un *cloud.Unreachable
		if errors.As(err, &un) {
			return nil, connect.ErrBrokerUnreachable
		}
		return nil, err
	}
	return toToken(t), nil
}

func (b *broker) Message(err error) string { return cloud.Message(err) }
