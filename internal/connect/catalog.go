package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Auth is how a connector signs in.
type Auth string

const (
	AuthOAuth  Auth = "oauth"  // browser sign-in, PKCE, loopback (or blaze://) callback
	AuthDevice Auth = "device" // a code to type on the provider's site (GitHub)
	AuthToken  Auth = "token"  // paste a token (Notion, Slack)
	AuthNone   Auth = "none"   // nothing to set up
)

// Field is one thing the user fills in to set a connector up.
type Field struct {
	Key      string `json:"key"` // client_id | client_secret | token
	Label    string `json:"label"`
	Hint     string `json:"hint"`
	Secret   bool   `json:"secret"`
	Optional bool   `json:"optional"`
}

// Connector is one catalog entry.
type Connector struct {
	ID, Name, Blurb   string
	Auth              Auth
	AuthURL, TokenURL string
	DeviceURL         string
	BaseScopes        []string // always asked for
	AuthParams        url.Values
	Fields            []Field
	Steps             []string
	Console           string // the provider's developer console
	ConsoleName       string
	CustomScheme      bool // the provider accepts a blaze:// redirect
	AnyPort           bool // loopback may use any port (Google desktop clients)
	SecretRequired    bool // the token exchange needs the client secret
	Groups            []string
	WhoAmI            func(c *call) (string, error)
}

// Risk decides when a tool asks first.
type Risk string

const (
	Read   Risk = "read"   // runs without asking
	Action Risk = "action" // asks, unless auto-approve is on or the user chose Always allow
	Write  Risk = "write"  // sends, posts or changes things: always asks unless Always allow
)

// Tool is one connector tool.
type Tool struct {
	Name      string
	Connector string
	Group     string
	Desc      string
	Props     map[string]any
	Required  []string
	Risk      Risk
	Scopes    []string                            // needed scopes (any listed in implies also counts)
	Ask       func(a args) (title, detail string) // the approval card
	Run       func(c *call) (string, error)
}

// ---------------- the catalog ----------------

const (
	gScope = "https://www.googleapis.com/auth/"
)

var googleScopeNames = map[string]string{
	gScope + "gmail.readonly":           "read your email",
	gScope + "gmail.compose":            "create email drafts",
	gScope + "gmail.send":               "send email as you",
	gScope + "calendar.events.readonly": "see your calendar events",
	gScope + "calendar.events":          "add and change calendar events",
	gScope + "drive.readonly":           "see and download your Drive files",
	gScope + "contacts.readonly":        "see your contacts",
}

// implies: holding the key scope also satisfies the listed ones.
var implies = map[string][]string{
	gScope + "calendar.events": {gScope + "calendar.events.readonly"},
	gScope + "gmail.modify":    {gScope + "gmail.readonly", gScope + "gmail.compose"},
	"https://mail.google.com/": {gScope + "gmail.readonly", gScope + "gmail.compose", gScope + "gmail.send"},
}

// ScopeLabel is a scope in words.
func ScopeLabel(s string) string {
	if n, ok := googleScopeNames[s]; ok {
		return n
	}
	return strings.TrimPrefix(s, gScope)
}

// Catalog is every built-in connector, in display order.
var Catalog = []*Connector{
	{
		ID: "google", Name: "Google", Blurb: "Gmail, Calendar, Drive and Contacts", Auth: AuthOAuth,
		AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token",
		BaseScopes: []string{"openid", "email"},
		AuthParams: url.Values{"access_type": {"offline"}, "prompt": {"consent"}, "include_granted_scopes": {"true"}},
		Fields: []Field{
			{Key: "client_id", Label: "Client ID", Hint: "…apps.googleusercontent.com"},
			{Key: "client_secret", Label: "Client secret", Hint: "GOCSPX-…", Secret: true},
		},
		Steps: []string{
			"Open Google Cloud Console and create a project (any name).",
			"APIs & Services › Library: enable Gmail API, Google Calendar API, Google Drive API and People API.",
			"OAuth consent screen: External, then add your own Google address as a test user.",
			"Credentials › Create credentials › OAuth client ID › Application type: Desktop app.",
			"Paste the client ID and client secret here, then Connect. Google signs in through 127.0.0.1, so there's no redirect URI to add.",
		},
		Console: "https://console.cloud.google.com/apis/credentials", ConsoleName: "Google Cloud Console",
		AnyPort: true, SecretRequired: true,
		Groups: []string{"gmail", "calendar", "drive", "contacts"},
		WhoAmI: googleWho,
	},
	{
		ID: "github", Name: "GitHub", Blurb: "Repos, issues, pull requests and notifications", Auth: AuthDevice,
		DeviceURL: "https://github.com/login/device/code", TokenURL: "https://github.com/login/oauth/access_token",
		BaseScopes: []string{"repo", "read:user", "notifications"},
		Fields:     []Field{{Key: "client_id", Label: "Client ID", Hint: "Ov23li…"}},
		Steps: []string{
			"github.com › Settings › Developer settings › OAuth Apps › New OAuth App. Homepage and callback can be any URL (e.g. http://127.0.0.1).",
			"On the app page, tick Enable Device Flow and save.",
			"Paste the Client ID here (no secret needed), then Connect and type the code Blaze shows on github.com/login/device.",
		},
		Console: "https://github.com/settings/developers", ConsoleName: "GitHub Developer settings",
		Groups: []string{"github"}, WhoAmI: githubWho,
	},
	{
		ID: "spotify", Name: "Spotify", Blurb: "Now playing, play, pause, skip and search", Auth: AuthOAuth,
		AuthURL: "https://accounts.spotify.com/authorize", TokenURL: "https://accounts.spotify.com/api/token",
		BaseScopes: []string{"user-read-playback-state", "user-modify-playback-state", "user-read-currently-playing"},
		Fields:     []Field{{Key: "client_id", Label: "Client ID", Hint: "32 hex characters"}},
		Steps: []string{
			"developer.spotify.com › Dashboard › Create app. Tick Web API.",
			fmt.Sprintf("Redirect URIs: add http://127.0.0.1:%d/callback (and %s if you pick that redirect below).", LoopbackPort, SchemeCallback),
			"Save, then paste the Client ID here (Blaze uses PKCE, so no secret) and Connect.",
			"Playback control needs Spotify open on a device, and Spotify Premium.",
		},
		Console: "https://developer.spotify.com/dashboard", ConsoleName: "Spotify Developer Dashboard",
		CustomScheme: true, Groups: []string{"spotify"}, WhoAmI: spotifyWho,
	},
	{
		ID: "notion", Name: "Notion", Blurb: "Search, read and add to your pages", Auth: AuthToken,
		Fields: []Field{{Key: "token", Label: "Internal integration secret", Hint: "ntn_… or secret_…", Secret: true}},
		Steps: []string{
			"notion.so/my-integrations › New integration (Internal), pick your workspace.",
			"Copy the Internal Integration Secret and paste it here.",
			"In Notion, open each page Blaze may use › ••• › Connections › add your integration. Blaze only sees pages you share this way.",
		},
		Console: "https://www.notion.so/my-integrations", ConsoleName: "Notion integrations",
		Groups: []string{"notion"}, WhoAmI: notionWho,
	},
	{
		ID: "slack", Name: "Slack", Blurb: "Read channels and post messages", Auth: AuthToken,
		Fields: []Field{{Key: "token", Label: "User OAuth token", Hint: "xoxp-…", Secret: true}},
		Steps: []string{
			"api.slack.com/apps › Create New App › From scratch, pick your workspace.",
			"OAuth & Permissions › User Token Scopes: channels:read, channels:history, groups:read, groups:history, chat:write, users:read.",
			"Install to Workspace, then copy the User OAuth Token (xoxp-…) and paste it here.",
		},
		Console: "https://api.slack.com/apps", ConsoleName: "Slack apps",
		Groups: []string{"slack"}, WhoAmI: slackWho,
	},
}

// GroupNames are the tool groups in words, for the prompt and the UI.
var GroupNames = map[string]string{
	"gmail": "Gmail", "calendar": "Google Calendar", "drive": "Google Drive", "contacts": "Google Contacts",
	"github": "GitHub", "spotify": "Spotify", "notion": "Notion", "slack": "Slack",
}

func byID(id string) *Connector {
	for _, c := range Catalog {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// ---------------- tool arguments ----------------

type args map[string]any

func (a args) s(k string) string {
	switch v := a[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprint(int64(v))
		}
		return fmt.Sprint(v)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func (a args) i(k string, def, lo, hi int) int {
	n := def
	switch v := a[k].(type) {
	case float64:
		n = int(v)
	case string:
		if _, err := fmt.Sscan(v, &n); err != nil {
			n = def
		}
	}
	if n < lo {
		n = lo
	}
	if n > hi {
		n = hi
	}
	return n
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
func enum(desc string, v ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": v}
}

// ---------------- HTTP for tools ----------------

// call is one tool run: its arguments and an authorized HTTP client.
type call struct {
	ctx   context.Context
	m     *Manager
	conn  string
	token string
	a     args
}

// APIError is a provider's non-2xx answer.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	b := strings.TrimSpace(e.Body)
	if len(b) > 300 {
		b = b[:300] + "…"
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, b)
}

var errUnauthorized = errors.New("unauthorized")

// do sends a request with the connector's token. A 401 refreshes the token once
// and retries; if that fails the connection needs Reconnect (ErrReauth).
func (c *call) do(method, u string, body any, hdr map[string]string) (int, []byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		var rd io.Reader
		ct := ""
		switch b := body.(type) {
		case nil:
		case string:
			rd = strings.NewReader(b)
		case []byte:
			rd = bytes.NewReader(b)
		default:
			j, _ := json.Marshal(b)
			rd = bytes.NewReader(j)
			ct = "application/json"
		}
		req, err := http.NewRequestWithContext(c.ctx, method, u, rd)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "Blaze-Desktop")
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := c.m.hc.Do(req)
		if err != nil {
			return 0, nil, err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == 401 && attempt == 0 {
			t, err := c.m.access(c.ctx, c.conn, true)
			if err != nil {
				return 401, data, err
			}
			c.token = t
			continue
		}
		if resp.StatusCode == 401 {
			c.m.markExpired(c.conn)
			return 401, data, ErrReauth
		}
		return resp.StatusCode, data, nil
	}
	return 0, nil, errUnauthorized
}

// json sends a request and decodes a 2xx JSON answer into out.
func (c *call) json(method, u string, body any, out any, hdr ...string) error {
	h := map[string]string{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h[hdr[i]] = hdr[i+1]
	}
	st, data, err := c.do(method, u, body, h)
	if err != nil {
		return err
	}
	if st < 200 || st > 299 {
		return &APIError{st, string(data)}
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *call) get(u string, out any, hdr ...string) error { return c.json("GET", u, nil, out, hdr...) }

func q(s string) string { return url.QueryEscape(s) }

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
