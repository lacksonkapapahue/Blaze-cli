package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/keystore"
)

func TestSaveKeepsDesktopKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BLAZE_HOME", dir)
	p := filepath.Join(dir, "config.json")
	desk := `{"chain":[{"provider":"gemini","model":""}],"setup_done":true,"max_steps":30,
"no_fallback":true,"daily_cap":2.5,"custom_base":"http://x/v1","persona":"old"}`
	if err := os.WriteFile(p, []byte(desk), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Load()
	c.Persona = "" // omitempty: clearing a known key removes it
	if err := c.SelectModel("openrouter", "nvidia/nemotron-3.5-lightning:free"); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	b, _ := os.ReadFile(p)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["no_fallback"] != true || m["daily_cap"] != 2.5 || m["custom_base"] != "http://x/v1" {
		t.Fatalf("desktop keys lost: %s", b)
	}
	if _, ok := m["persona"]; ok {
		t.Fatalf("cleared known key kept: %s", b)
	}

	// Reload: the pick is the default, first in the chain, and first in the route.
	c = Load()
	want := Entry{Provider: "openrouter", Model: "nvidia/nemotron-3.5-lightning:free"}
	if c.Default == nil || *c.Default != want || c.Chain[0] != want || c.RouteChain()[0] != want {
		t.Fatalf("pick not sticky: default=%v chain=%v", c.Default, c.Chain)
	}
	if c.ModelFor["openrouter"] != want.Model || len(c.Chain) != 2 || c.Chain[1].Provider != "gemini" {
		t.Fatalf("model_for/chain: %v %v", c.ModelFor, c.Chain)
	}

	// Picking it again doesn't duplicate it; auto clears the default but keeps the order.
	_ = c.SelectModel("openrouter", want.Model)
	if len(c.Chain) != 2 {
		t.Fatalf("duplicated: %v", c.Chain)
	}
	_ = c.SelectModel(Auto, "")
	if c = Load(); c.Default != nil || c.Chain[0] != want {
		t.Fatalf("auto: %v %v", c.Default, c.Chain)
	}
}

func TestRouteChainOrder(t *testing.T) {
	c := &Config{Chain: []Entry{{"gemini", ""}, {"groq", "llama"}}}
	c.Default = &Entry{Provider: "groq", Model: "llama"} // Desktop's pick
	r := c.RouteChain()
	if r[0] != (Entry{"groq", "llama"}) || r[1].Provider != "gemini" || len(r) != 3 {
		t.Fatalf("default first: %v", r)
	}
	c.Override = &Entry{"mistral", "m"} // --model, this run only
	if r = c.RouteChain(); r[0] != (Entry{"mistral", "m"}) || r[1] != (Entry{"groq", "llama"}) {
		t.Fatalf("override first: %v", r)
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "mistral") {
		t.Fatalf("override saved: %s", b)
	}
	// Default with an empty model and the same provider's chain entry dedupe.
	c = &Config{Chain: []Entry{{"gemini", ""}}, Default: &Entry{Provider: "gemini"}, NoKeyless: true}
	if r = c.RouteChain(); len(r) != 1 {
		t.Fatalf("dedupe: %v", r)
	}
}

type noKeychain struct{}

func (noKeychain) Get(string) (string, error) { return "", errors.New("none") }
func (noKeychain) Set(string, string) error   { return errors.New("none") }
func (noKeychain) Delete(string) error        { return nil }

func TestSaveKeepsDesktopFieldsAndLegacyKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BLAZE_HOME", dir)
	path := filepath.Join(dir, "config.json")
	in := `{"chain":[{"provider":"groq","model":"m"}],"persona":"old","model_for":{"groq":"x"},"default":{"provider":"auto","model":""},"keys":{"groq":"legacy"}}`
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Load()
	c.Persona = "" // cleared omitempty field must disappear
	c.MaxSteps = 12
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out["model_for"] == nil || out["default"] == nil || out["keys"] == nil {
		t.Fatalf("desktop/legacy fields dropped: %s", b)
	}
	if _, ok := out["persona"]; ok || out["max_steps"].(float64) != 12 {
		t.Fatalf("own fields wrong: %s", b)
	}
}

func TestKeysGoToSharedStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BLAZE_HOME", dir)
	t.Setenv("GROQ_API_KEY", "")
	old := keystore.Keychain
	keystore.Keychain = noKeychain{}
	defer func() { keystore.Keychain = old }()
	c := Load()
	where, err := c.SetKey("groq", "gsk_1")
	if err != nil || where != "~/.blaze/keys.json (owner-only)" {
		t.Fatalf("%q %v", where, err)
	}
	if Load().Key("groq") != "gsk_1" {
		t.Fatal("key not read back")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
		b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		if m["keys"] != nil {
			t.Fatalf("key written to config.json: %s", b)
		}
	}
	c.DeleteKey("groq")
	if Load().Key("groq") != "" {
		t.Fatal("key not deleted")
	}
}
