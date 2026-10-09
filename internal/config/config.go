// Package config keeps Blaze's settings in ~/.blaze/config.json and API keys in
// the OS keychain (Windows Credential Manager, macOS Keychain, Linux Secret
// Service), falling back to the config file (mode 0600) when no keychain exists.
package config

import (
	"encoding/json"
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
	path        string
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

func (c *Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
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

// RouteChain is the chain plus the keyless last resort.
func (c *Config) RouteChain() []Entry {
	ch := append([]Entry(nil), c.Chain...)
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
