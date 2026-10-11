package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
