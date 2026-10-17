package llm

// Blaze Cloud: every model call can go through the Blaze Gateway, one
// OpenAI-compatible endpoint signed in with the Blaze account (the session token
// "bz_…" from `blaze login` is the API key). The gateway holds the provider keys
// and picks the model per task, so nobody needs keys of their own. Mirrors Blaze
// Android v0.25.0+ (agent/Providers.kt GATEWAY, agent/Cloud.kt, LlmClient.kt) and
// the gateway's contract (Blaze-agent gateway/README.md, src/chat.ts).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

const (
	// BlazeID is the provider id of Blaze Cloud in route chains.
	BlazeID = "blaze"
	// GatewayBase is the gateway's current name (Android Providers.kt v0.25.0).
	GatewayBase = "https://ai.blaze-app.workers.dev/v1/"
	// LegacyGatewayBase still answers (the gateway's first name).
	LegacyGatewayBase = "https://blaze-gateway.blaze-app.workers.dev/v1/"
	// BlazeModel is the gateway's "pick for me" model; the task hint does the choosing.
	BlazeModel = "blaze-auto"
	// longChars is about 12k tokens, the gateway's own "long" cut-off (router.ts LONG_CHARS).
	longChars = 48_000
)

// BlazeTasks are the gateway's task tags; each also exists as model "blaze-<task>".
var BlazeTasks = []string{"chat", "quick", "code", "vision", "long", "agent"}

// BlazeModelInfo (read-only) describes the gateway's virtual models (gateway/src/index.ts MODEL_INFO).
var BlazeModelInfo = map[string]string{
	"blaze-auto":   "Blaze picks the task from the request",
	"blaze-chat":   "Everyday conversation",
	"blaze-quick":  "Short, fast answers",
	"blaze-code":   "Programming",
	"blaze-vision": "Images in the conversation",
	"blaze-long":   "Very long inputs",
	"blaze-agent":  "Tool / function calling",
}

// StaticBlazeModels is the list used when GET /v1/models can't be reached.
func StaticBlazeModels() []string {
	out := []string{BlazeModel}
	for _, t := range BlazeTasks {
		out = append(out, "blaze-"+t)
	}
	return out
}

// IsBlazeModel is true for "blaze-auto", "blaze-<task>" and any id the gateway listed.
func IsBlazeModel(m string) bool {
	m = strings.TrimSpace(m)
	if _, ok := BlazeModelInfo[m]; ok {
		return true
	}
	modelsMu.Lock()
	defer modelsMu.Unlock()
	for _, id := range modelsCache {
		if id == m {
			return true
		}
	}
	return false
}

// BlazeModelDesc is a Blaze model's one-line description ("" when unknown).
func BlazeModelDesc(id string) string {
	if d, ok := BlazeModelInfo[id]; ok {
		return d
	}
	modelsMu.Lock()
	defer modelsMu.Unlock()
	return modelDesc[id]
}

// SessionToken returns the signed-in Blaze account's session token ("" when signed
// out). The host sets it; it is read on every request so login/logout apply at once.
var SessionToken func() string

// Token is the current session token, "" when signed out.
func Token() string {
	if SessionToken == nil {
		return ""
	}
	return strings.TrimSpace(SessionToken())
}

// SignedIn is true when a Blaze session token is present.
func SignedIn() bool { return Token() != "" }

// GatewayURL is BLAZE_GATEWAY_URL (…/v1), else GatewayBase.
func GatewayURL() string {
	if v := strings.TrimSpace(os.Getenv("BLAZE_GATEWAY_URL")); v != "" {
		return strings.TrimRight(v, "/") + "/"
	}
	return GatewayBase
}

// BlazeProvider is Blaze Cloud as an OpenAI-compatible provider.
func BlazeProvider() config.Provider {
	return config.Provider{ID: BlazeID, Name: "Blaze", BaseURL: GatewayURL(), DefaultModel: BlazeModel, KeyHint: "your Blaze account", KeyPage: "", Free: true}
}

// ProviderFor is config.ProviderByID plus Blaze Cloud.
func ProviderFor(id string) (config.Provider, bool) {
	if id == BlazeID {
		return BlazeProvider(), true
	}
	return config.ProviderByID(id)
}

// KeyFor is the key a provider is called with: the session token for Blaze Cloud.
func KeyFor(cfg *config.Config, id string) string {
	if id == BlazeID {
		return Token()
	}
	return cfg.Key(id)
}

// Usable is true when the provider can be called right now.
func Usable(cfg *config.Config, id string) bool {
	if id == BlazeID {
		return SignedIn()
	}
	return config.Keyless(id) || cfg.Key(id) != ""
}

// HasAnyModel is true when Blaze Cloud (signed in) or one of the user's own models can answer.
func HasAnyModel(cfg *config.Config) bool { return SignedIn() || cfg.HasAnyModel() }

// ---------------- the Blaze model choice (its own file, not the shared config.json) ----------------

// CloudPrefs is ~/.blaze/blaze-cloud.json: which Blaze model, and whether a model
// of the user's own was picked to go first (Blaze Cloud then right behind it).
type CloudPrefs struct {
	Model    string `json:"model,omitempty"`     // blaze-auto (default) or blaze-<task>
	OwnFirst bool   `json:"own_first,omitempty"` // the picked own-key model goes before Blaze Cloud
}

var prefsMu sync.Mutex

func prefsPath() string { return config.Dir() + string(os.PathSeparator) + "blaze-cloud.json" }

// LoadCloudPrefs reads blaze-cloud.json (defaults when missing).
func LoadCloudPrefs() CloudPrefs {
	prefsMu.Lock()
	defer prefsMu.Unlock()
	var p CloudPrefs
	if b, err := os.ReadFile(prefsPath()); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	if !IsBlazeModel(p.Model) {
		p.Model = BlazeModel
	}
	return p
}

// SaveCloudPrefs writes blaze-cloud.json.
func SaveCloudPrefs(p CloudPrefs) error {
	prefsMu.Lock()
	defer prefsMu.Unlock()
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	if !IsBlazeModel(p.Model) {
		p.Model = BlazeModel
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(prefsPath(), b, 0o600)
}

// RouteChain is the order models are tried in. Signed in, Blaze Cloud comes first
// (after a one-run --model, or after the user's own picked model when they chose
// one to go first); the user's own keys follow as optional fallbacks. Signed out,
// it is the user's own chain (any Blaze entry there is skipped: no token).
func RouteChain(cfg *config.Config) []config.Entry {
	base := cfg.RouteChain()
	if !SignedIn() {
		return base
	}
	// Signed in, Blaze Cloud replaces the weak keyless last resort (it stays only
	// when the user put it in their own chain), so a used-up allowance is said
	// plainly instead of a smaller model answering quietly.
	if n := len(base); n > 0 && base[n-1].Provider == "pollinations" && !inChain(cfg, "pollinations") {
		base = base[:n-1]
	}
	prefs := LoadCloudPrefs()
	blaze := config.Entry{Provider: BlazeID, Model: prefs.Model}
	for _, e := range base {
		if e.Provider == BlazeID { // a --model blaze-… override already put it in
			return base
		}
	}
	at := 0
	if cfg.Override != nil && len(base) > 0 {
		at = 1
	}
	if prefs.OwnFirst && !cfg.IsAuto() && len(base) > at {
		if d := base[at]; d.Provider == cfg.Default.Provider {
			at++
		}
	}
	out := make([]config.Entry, 0, len(base)+1)
	out = append(out, base[:at]...)
	out = append(out, blaze)
	return append(out, base[at:]...)
}

func inChain(cfg *config.Config, provider string) bool {
	for _, e := range cfg.Chain {
		if e.Provider == provider {
			return true
		}
	}
	return (cfg.Override != nil && cfg.Override.Provider == provider) || (!cfg.IsAuto() && cfg.Default.Provider == provider)
}

// ---------------- the task hint ----------------

var codeCue = regexp.MustCompile("(?i)(```|\\b(code|coding|function|bug|stack ?trace|compile|kotlin|python|javascript|typescript|java|rust|golang|go test|sql|regex|script|refactor|debug)\\b)")

// BlazeTask is the gateway task for one request, as Android sends it:
// long > agent (tools) > code > chat.
func BlazeTask(msgs []Message, tools []Tool) string {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
	}
	switch {
	case n > longChars:
		return "long"
	case len(tools) > 0:
		return "agent"
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			t := msgs[i].Content
			if len(t) > 4000 {
				t = t[:4000]
			}
			if codeCue.MatchString(t) {
				return "code"
			}
			break
		}
	}
	return "chat"
}

// blazeExtra is what a gateway request adds to the body: blaze_task when the model
// is blaze-auto (a picked blaze-<task> model names its task itself).
func blazeExtra(model string, msgs []Message, tools []Tool) map[string]any {
	if model != "" && model != BlazeModel {
		return nil
	}
	return map[string]any{"blaze_task": BlazeTask(msgs, tools)}
}

// ---------------- errors ----------------

// The lines people see (Android agent/Cloud.kt, gateway/src/access.ts).
const (
	MsgSignedOut  = "Sign in to Blaze to start (free, no API keys): run `blaze login`."
	MsgExpired    = "Your Blaze sign-in has expired. Run `blaze login` to sign in again."
	MsgWaitlisted = "You're on the Blaze waiting list. You'll get access as soon as you're approved."
	MsgRejected   = "Your Blaze account doesn't have access to Blaze AI. If you think this is a mistake, contact the Blaze team."
	MsgOffline    = "Couldn't reach Blaze Cloud. Check your internet connection and try again."
)

// GatewayError is a failed Blaze Gateway call. Status 0 means no answer (network).
type GatewayError struct {
	Status     int
	Code       string // invalid_api_key, quota_exceeded, blaze_models_busy, waitlisted, access_rejected, …
	Message    string // the gateway's own message
	RetryAfter int    // seconds, from Retry-After
	Err        error  // the network error when Status is 0
}

func (e *GatewayError) Error() string { return e.Friendly() }
func (e *GatewayError) Unwrap() error { return e.Err }

// Expired: the session token was refused (401). Only this means "sign in again";
// 5xx, 429, 403 waiting list and network failures never do.
func (e *GatewayError) Expired() bool { return e != nil && e.Status == http.StatusUnauthorized }

// Quota: today's plan allowance is used up.
func (e *GatewayError) Quota() bool {
	return e != nil && e.Status == http.StatusTooManyRequests && (e.Code == "quota_exceeded" || strings.Contains(strings.ToLower(e.Message), "allowance"))
}

// Waitlisted / Rejected: signed in, but no access (yet).
func (e *GatewayError) Waitlisted() bool {
	return e != nil && e.Status == http.StatusForbidden && (e.Code == "waitlisted" || strings.Contains(strings.ToLower(e.Message), "waiting list"))
}
func (e *GatewayError) Rejected() bool {
	return e != nil && e.Status == http.StatusForbidden && e.Code == "access_rejected"
}

// Busy: every model is cooling (503 blaze_models_busy) or a short rate limit.
func (e *GatewayError) Busy() bool {
	return e != nil && (e.Status == http.StatusServiceUnavailable || e.Status == http.StatusTooManyRequests && !e.Quota())
}

// Friendly is the one line a person sees.
func (e *GatewayError) Friendly() string {
	switch {
	case e == nil:
		return ""
	case e.Status == 0:
		return MsgOffline
	case e.Expired():
		return MsgExpired
	case e.Rejected():
		return MsgRejected
	case e.Waitlisted():
		return MsgWaitlisted
	case e.Quota():
		if e.RetryAfter > 0 {
			return "You've used today's free Blaze allowance. It resets in " + FormatWait(e.RetryAfter) + "."
		}
		return "You've used today's free Blaze allowance. It resets at 00:00 UTC."
	case e.Busy():
		if e.RetryAfter > 0 {
			return "Blaze's models are all busy right now. Try again in about " + FormatWait(e.RetryAfter) + "."
		}
		return "Blaze's models are all busy right now. Try again in a moment."
	case e.Status == http.StatusForbidden:
		return "Blaze Cloud refused this request (403): " + nz(e.Message, e.Code)
	case e.Status >= 500:
		return "Blaze Cloud is having trouble on its side right now. Try again in a moment."
	}
	return "Blaze Cloud couldn't answer that (" + strconv.Itoa(e.Status) + "): " + nz(e.Message, e.Code)
}

// FormatWait is seconds as "3h 12m", "12m", or "45s".
func FormatWait(s int) string {
	switch {
	case s <= 0:
		return "a moment"
	case s >= 3600:
		h, m := s/3600, (s%3600+59)/60
		if m == 60 {
			h, m = h+1, 0
		}
		return fmt.Sprintf("%dh %dm", h, m)
	case s >= 60:
		return fmt.Sprintf("%dm", (s+59)/60)
	}
	return fmt.Sprintf("%ds", s)
}

func nz(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// gatewayError reads a non-2xx gateway answer: {"error":{"message","type","code"}} + Retry-After.
func gatewayError(status int, h http.Header, raw []byte) *GatewayError {
	ge := &GatewayError{Status: status, RetryAfter: retryAfter(h.Get("Retry-After"))}
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && len(e.Error) > 0 {
		var obj struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		}
		var str string
		if json.Unmarshal(e.Error, &obj) == nil {
			ge.Message = obj.Message
			if s, ok := obj.Code.(string); ok {
				ge.Code = s
			}
			if ge.Code == "" {
				ge.Code = obj.Type
			}
		} else if json.Unmarshal(e.Error, &str) == nil {
			ge.Code = str
		}
	}
	if ge.Message == "" && ge.Code == "" {
		ge.Message = cleanErr(string(raw))
	}
	return ge
}

func retryAfter(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return int(d.Seconds() + 0.5)
		}
	}
	return 0
}

// Served is what the gateway says answered a call, from its response headers.
type Served struct {
	Provider, Model, Task, Remaining string
}

func servedFrom(h http.Header) Served {
	return Served{Provider: h.Get("X-Blaze-Provider"), Model: h.Get("X-Blaze-Model"), Task: h.Get("X-Blaze-Task"), Remaining: h.Get("X-RateLimit-Remaining-Requests")}
}

// RouteLabel is the footer's "Blaze · provider/model" (or "Blaze · blaze-auto"
// when the gateway didn't say).
func (s Served) RouteLabel(asked string) string {
	switch {
	case s.Provider != "" && s.Model != "":
		return "Blaze · " + s.Provider + "/" + s.Model
	case s.Model != "":
		return "Blaze · " + s.Model
	}
	return "Blaze · " + nz(asked, BlazeModel)
}

// tokenTag keys the router's health per session, so a new sign-in starts fresh.
func tokenTag(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:4])
}

// AsGateway returns err's GatewayError, if it has one.
func AsGateway(err error) (*GatewayError, bool) {
	var ge *GatewayError
	if errors.As(err, &ge) {
		return ge, true
	}
	return nil, false
}

// ---------------- the model list ----------------

var (
	modelsMu    sync.Mutex
	modelsCache []string
	modelsAt    time.Time
	modelDesc   = map[string]string{}
)

// BlazeModels is GET {gateway}/models (blaze-auto plus one model per task), cached
// for 10 minutes; the static list when it can't be reached.
func BlazeModels(ctx context.Context) ([]string, bool) {
	modelsMu.Lock()
	if modelsCache != nil && time.Since(modelsAt) < 10*time.Minute {
		c := modelsCache
		modelsMu.Unlock()
		return c, true
	}
	modelsMu.Unlock()
	tok := Token()
	if tok == "" {
		return StaticBlazeModels(), false
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(GatewayURL(), "/")+"/models", nil)
	if err != nil {
		return StaticBlazeModels(), false
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return StaticBlazeModels(), false
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		Data []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"data"`
	}
	if resp.StatusCode >= 300 || json.Unmarshal(raw, &r) != nil || len(r.Data) == 0 {
		return StaticBlazeModels(), false
	}
	var ids []string
	for _, d := range r.Data {
		if d.ID == "" {
			continue
		}
		ids = append(ids, d.ID)
		if d.Description != "" {
			modelsMu.Lock()
			modelDesc[d.ID] = d.Description
			modelsMu.Unlock()
		}
	}
	modelsMu.Lock()
	modelsCache, modelsAt = ids, time.Now()
	modelsMu.Unlock()
	return ids, true
}

// CachedBlazeModels is the last fetched list (no network), else the static one.
func CachedBlazeModels() []string {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	if modelsCache != nil {
		return modelsCache
	}
	return StaticBlazeModels()
}
