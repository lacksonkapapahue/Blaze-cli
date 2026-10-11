// Package config keeps Blaze's settings in ~/.blaze/config.json and API keys in
// the OS keychain (Windows Credential Manager, macOS Keychain, Linux Secret
// Service), falling back to the config file (mode 0600) when no keychain exists.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

	// Shared with Blaze Desktop (its Settings › Models): the model you picked, and
	// the model chosen per provider. Default nil or provider "auto" = Auto.
	Default  *Entry            `json:"default,omitempty"`
	ModelFor map[string]string `json:"model_for,omitempty"`

	// Override is a one-off "--model" for this run only; never saved.
	Override *Entry `json:"-"`
	path     string
}

// Auto is the provider id that means "no fixed model, route over the chain".
const Auto = "auto"

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

// Save writes config.json. The file is shared with Blaze Desktop, so keys this
// version doesn't know (Desktop's routing settings, custom provider, …) are read
// back from the file and kept rather than dropped.
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

// mergeJSON returns v as indented JSON on top of the object already in path:
// keys v has replace the file's, keys only the file has are kept, and keys v
// leaves out because they are empty (omitempty) are removed.
func mergeJSON(path string, v any) ([]byte, error) {
	mine, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	if old, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(old, &out)
	}
	known := map[string]bool{}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			known[name] = true
		}
	}
	for k := range known {
		delete(out, k)
	}
	var cur map[string]json.RawMessage
	if err := json.Unmarshal(mine, &cur); err != nil {
		return nil, err
	}
	for k, val := range cur {
		out[k] = val
	}
	return json.MarshalIndent(out, "", "  ")
}

// IsAuto is true when no fixed model is picked (Desktop's "Auto").
func (c *Config) IsAuto() bool {
	return c.Default == nil || c.Default.Provider == "" || c.Default.Provider == Auto
}

// Model is the model Blaze uses for a provider: the one picked for it, then its
// chain entry's, then the provider's built-in default (Desktop's rule).
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

// SelectModel makes provider/model the model Blaze uses from now on, in the CLI
// and in Blaze Desktop: it becomes the default, the provider's chosen model and
// the first chain entry (the others stay behind it as fallbacks), and is saved.
// provider "auto" goes back to plain chain order.
func (c *Config) SelectModel(provider, model string) error {
	if provider == "" || provider == Auto {
		c.Default = nil
		return c.Save()
	}
	if model == "" {
		model = c.Model(provider)
	}
	c.Default = &Entry{Provider: provider, Model: model}
	if model != "" {
		if c.ModelFor == nil {
			c.ModelFor = map[string]string{}
		}
		c.ModelFor[provider] = model
	}
	chain := []Entry{{Provider: provider, Model: model}}
	for _, e := range c.Chain {
		if e.Provider == provider && (e.Model == model || e.Model == "" && model == c.Model(provider)) {
			continue
		}
		chain = append(chain, e)
	}
	c.Chain = chain
	return c.Save()
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

// RouteChain is the chain plus the keyless last resort.
// A --model override goes first, then the picked default model, then the chain.
func (c *Config) RouteChain() []Entry {
	var ch []Entry
	seen := map[Entry]bool{}
	add := func(e Entry) {
		k := e
		if p, ok := ProviderByID(k.Provider); ok && k.Model == "" {
			k.Model = p.DefaultModel
		}
		if !seen[k] {
			seen[k] = true
			ch = append(ch, e)
		}
	}
	if c.Override != nil {
		add(*c.Override)
	}
	if !c.IsAuto() {
		if _, ok := ProviderByID(c.Default.Provider); ok {
			d := *c.Default
			if d.Model == "" {
				d.Model = c.Model(d.Provider)
			}
			add(d)
		}
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
