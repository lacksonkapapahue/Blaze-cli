package main

import (
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

func TestParseModel(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	for _, p := range config.Providers {
		if p.Env != "" {
			t.Setenv(p.Env, "")
		}
	}
	t.Setenv("GEMINI_API_KEY", "x")
	cfg := &config.Config{Chain: []config.Entry{{Provider: "gemini"}, {Provider: "openrouter", Model: "vendor/known"}}}
	cases := []struct{ in, prov, model string }{
		{"gemini/gemini-3.8-flash", "gemini", "gemini-3.8-flash"},
		{"openrouter/vendor/x", "openrouter", "vendor/x"},
		{"vendor/known", "openrouter", "vendor/known"},                                  // already in the chain
		{"nvidia/nemotron-3.5-lightning:free", "nvidia", "nemotron-3.5-lightning:free"}, // no OpenRouter key: provider prefix
	}
	for _, c := range cases {
		e, err := parseModel(cfg, c.in)
		if err != nil || e.Provider != c.prov || e.Model != c.model {
			t.Errorf("%s: got %v %v", c.in, e, err)
		}
	}
	t.Setenv("OPENROUTER_API_KEY", "y")
	e, _ := parseModel(cfg, "nvidia/nemotron-3.5-lightning:free")
	if e.Provider != "openrouter" || e.Model != "nvidia/nemotron-3.5-lightning:free" {
		t.Errorf(":free id with OpenRouter set up: %v", e)
	}
	e, _ = parseModel(cfg, "meta/llama")
	if e.Provider != "openrouter" || e.Model != "meta/llama" {
		t.Errorf("unknown prefix with OpenRouter: %v", e)
	}
	t.Setenv("OPENROUTER_API_KEY", "")
	if _, err := parseModel(&config.Config{}, "nope/x"); err == nil {
		t.Errorf("unknown provider without OpenRouter should fail")
	}
}
