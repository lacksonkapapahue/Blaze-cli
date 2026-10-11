// Package config keeps Blaze's settings in ~/.blaze/config.json and API keys in
// the OS keychain (Windows Credential Manager, macOS Keychain, Linux Secret
// Service), falling back to the config file (mode 0600) when no keychain exists.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

const service = "blaze-cli"

type Provider struct {
	ID, Name, BaseURL, DefaultModel, KeyHint, KeyPage, Env string
	Free                                                   bool
}

// Providers mirrors the Android app's list (all OpenAI-compatible).
var Providers = []Provider{
	{"gemini", "Google Gemini", "https://generativelanguage.googleapis.com/v1beta/openai/", "gemini-3.8-flash", "AIza…", "https://aistudio.google.com/apikey", "GEMINI_API_KEY", true},
	{"groq", "Groq", "https://api.groq.com/openai/v1/", "", "gsk_…", "https://console.groq.com/keys", "GROQ_API_KEY", true},
	{"openrouter", "OpenRouter", "https://openrouter.ai/api/v1/", "", "sk-or-…", "https://openrouter.ai/settings/keys", "OPENROUTER_API_KEY", true},
	{"mistral", "Mistral", "https://api.mistral.ai/v1/", "", "…", "https://console.mistral.ai/api-keys", "MISTRAL_API_KEY", true},
	{"nvidia", "NVIDIA NIM", "https://integrate.api.nvidia.com/v1/", "", "nvapi-…", "https://build.nvidia.com/settings/api-keys", "NVIDIA_API_KEY", true},
	{"deepseek", "DeepSeek", "https://api.deepseek.com/v1/", "deepseek-chat", "sk-…", "https://platform.deepseek.com/api_keys", "DEEPSEEK_API_KEY", false},
	{"moonshot", "Kimi (Moonshot)", "https://api.moonshot.ai/v1/", "", "sk-…", "https://platform.moonshot.ai/console/api-keys", "MOONSHOT_API_KEY", false},
	{"openai", "ChatGPT (OpenAI)", "https://api.openai.com/v1/", "", "sk-…", "https://platform.openai.com/api-keys", "OPENAI_API_KEY", false},
	{"anthropic", "Claude (Anthropic)", "https://api.anthropic.com/v1/", "", "sk-ant-…", "https://console.anthropic.com/settings/keys", "ANTHROPIC_API_KEY", false},
	{"xai", "Grok (xAI)", "https://api.x.ai/v1/", "", "xai-…", "https://console.x.ai/", "XAI_API_KEY", false},
	{"pollinations", "Pollinations (keyless)", "https://text.pollinations.ai/openai/", "openai-fast", "(no key)", "https://pollinations.ai", "", true},
	{"ollama", "Ollama (local)", "http://127.0.0.1:11434/v1/", "", "(no key)", "https://ollama.com/download", "", true},
}

func ProviderByID(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

type Entry struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type Config struct {
	Chain       []Entry           `json:"chain"`          // tried in order; first healthy one answers
	Keys        map[string]string `json:"keys,omitempty"` // only when no keychain is available
	AutoApprove bool              `json:"auto_approve"`   // --yolo default
	SetupDone   bool              `json:"setup_done"`
	MaxSteps    int               `json:"max_steps"`
	Persona     string            `json:"persona,omitempty"`
	NoKeyless   bool              `json:"no_keyless,omitempty"` // don't fall back to the keyless model

	// Shared with Blaze Desktop (v0.3+): the model the user picked last, in the
	// desktop's keys. Default nil or provider "auto" = Auto (route over Chain).
	Default  *Entry            `json:"default,omitempty"`
	ModelFor map[string]string `json:"model_for,omitempty"` // chosen model per provider

	// Session is a one-run override (--model); never saved.
	Session *Entry `json:"-"`

	path string
}

func Dir() string {
	if d := os.Getenv("BLAZE_HOME"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".blaze")
}

func Load() *Config {
	c := &Config{MaxSteps: 30, path: filepath.Join(Dir(), "config.json")}
	if b, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(b, c)
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = 30
	}
	return c
}

// Save writes config.json. Keys this build doesn't know (Blaze Desktop's routing
// switches, custom provider, extra keys…) are kept as they were, so the CLI never
// wipes the desktop's settings.
func (c *Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := mergeJSON(c.path, c)
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, b, 0o600)
}

// Key returns the provider's key: env var first, then keychain, then the file.
func (c *Config) Key(provider string) string {
	if p, ok := ProviderByID(provider); ok && p.Env != "" {
		if v := strings.TrimSpace(os.Getenv(p.Env)); v != "" {
			return v
		}
	}
	if v, err := keyring.Get(service, provider); err == nil && v != "" {
		return v
	}
	return c.Keys[provider]
}

// SetKey stores a key in the keychain when possible. Returns where it went.
func (c *Config) SetKey(provider, key string) (string, error) {
	key = strings.TrimSpace(key)
	if err := keyring.Set(service, provider, key); err == nil {
		if c.Keys != nil {
			delete(c.Keys, provider)
		}
		return "your system keychain", c.Save()
	}
	if c.Keys == nil {
		c.Keys = map[string]string{}
	}
	c.Keys[provider] = key
	return "~/.blaze/config.json (owner-only)", c.Save()
}

func (c *Config) DeleteKey(provider string) {
	_ = keyring.Delete(service, provider)
	delete(c.Keys, provider)
	_ = c.Save()
}

// HasAnyModel is true when at least one chain entry has what it needs.
func (c *Config) HasAnyModel() bool {
	for _, e := range c.Chain {
		if Keyless(e.Provider) || c.Key(e.Provider) != "" {
			return true
		}
	}
	return false
}

// Keyless providers need no API key.
func Keyless(id string) bool { return id == "ollama" || id == "pollinations" }

// RouteChain is the order models are tried in: the --model override, then the
// last-picked model (when it can run: it has a key or needs none), then the
// chain, then the keyless last resort.
func (c *Config) RouteChain() []Entry {
	var ch []Entry
	seen := map[Entry]bool{}
	add := func(e Entry) {
		if e.Provider == "" || seen[e] {
			return
		}
		seen[e] = true
		ch = append(ch, e)
	}
	if c.Session != nil {
		add(*c.Session)
	}
	if d, ok := c.Preferred(); ok && c.Usable(d.Provider) {
		add(d)
	}
	for _, e := range c.Chain {
		add(e)
	}
	if c.NoKeyless {
		return ch
	}
	for _, e := range ch {
		if e.Provider == "pollinations" {
			return ch
		}
	}
	return append(ch, Entry{Provider: "pollinations", Model: "openai-fast"})
}

// Auto is Default.Provider for automatic routing (the desktop's "auto").
const Auto = "auto"

// IsAuto reports whether no model was picked (route over the chain).
func (c *Config) IsAuto() bool {
	return c.Default == nil || c.Default.Provider == "" || c.Default.Provider == Auto
}

// Preferred is the last-picked model with its model filled in (ok=false on Auto).
func (c *Config) Preferred() (Entry, bool) {
	if c.IsAuto() {
		return Entry{}, false
	}
	e := *c.Default
	if e.Model == "" {
		e.Model = c.Model(e.Provider)
	}
	return e, true
}

// Usable reports whether a provider can be called now: it has a key, or needs none.
func (c *Config) Usable(provider string) bool {
	if _, ok := ProviderByID(provider); !ok {
		return false
	}
	return Keyless(provider) || c.Key(provider) != ""
}

// Model is the model Blaze uses for a provider: the user's pick, then its chain
// entry, then the built-in default (same order as the desktop).
func (c *Config) Model(provider string) string {
	if m := c.ModelFor[provider]; m != "" {
		return m
	}
	for _, e := range c.Chain {
		if e.Provider == provider && e.Model != "" {
			return e.Model
		}
	}
	if p, ok := ProviderByID(provider); ok {
		return p.DefaultModel
	}
	return ""
}

// SetModelFor remembers the model picked for a provider.
func (c *Config) SetModelFor(provider, model string) {
	if c.ModelFor == nil {
		c.ModelFor = map[string]string{}
	}
	c.ModelFor[provider] = model
}

// SelectModel makes provider/model the model Blaze starts on, here and in Blaze
// Desktop (config.json "default" + "model_for"), and saves it. provider "auto" (or
// "") goes back to automatic routing. An empty model keeps the provider's current one.
func (c *Config) SelectModel(provider, model string) error {
	provider = strings.TrimSpace(provider)
	if provider == "" || provider == Auto {
		c.Default = nil
		return c.Save()
	}
	if _, ok := ProviderByID(provider); !ok {
		return fmt.Errorf("no provider called %q (try one of: %s)", provider, strings.Join(ProviderIDs(), ", "))
	}
	if provider == "pollinations" {
		return errors.New("Pollinations can't use tools, so it can't be the main model; it only answers plain chat when nothing else is set up")
	}
	if model == "" {
		model = c.Model(provider)
	}
	c.Default = &Entry{Provider: provider, Model: model}
	if model != "" {
		c.SetModelFor(provider, model)
	}
	for i, e := range c.Chain {
		if e.Provider == provider {
			c.Chain[i].Model = model
		}
	}
	return c.Save()
}

// ProviderIDs lists every provider id.
func ProviderIDs() []string {
	var out []string
	for _, p := range Providers {
		out = append(out, p.ID)
	}
	return out
}

// Fallback explains why the last-picked model can't start this session ("" = it
// can, or none was picked): "no API key" today; quota is noticed per turn.
func (c *Config) Fallback() string {
	d, ok := c.Preferred()
	if !ok {
		return ""
	}
	if _, known := ProviderByID(d.Provider); !known {
		return d.Provider + " isn't a provider this Blaze knows"
	}
	if !c.Usable(d.Provider) {
		return "it has no API key here"
	}
	return ""
}

// mergeJSON marshals v over the JSON object already in path: v's fields replace
// theirs (an omitted empty field removes the old value), unknown keys stay.
func mergeJSON(path string, v any) ([]byte, error) {
	old := map[string]json.RawMessage{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &old)
	}
	for _, k := range jsonKeys(v) {
		delete(old, k)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	cur := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &cur); err != nil {
		return nil, err
	}
	for k, x := range cur {
		old[k] = x
	}
	return json.MarshalIndent(old, "", "  ")
}

// jsonKeys are the JSON names of a struct's exported, serialised fields.
func jsonKeys(v any) []string {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}
