package main

import (
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

func TestSessionTokenPerCall(t *testing.T) {
	t.Setenv("BLAZE_SESSION_TOKEN", "")
	if llm.SignedIn() {
		t.Fatal("tests must not see a real sign-in")
	}
	t.Setenv("BLAZE_SESSION_TOKEN", "bz_env")
	if llm.Token() != "bz_env" || !llm.SignedIn() {
		t.Fatalf("token = %q", llm.Token())
	}
	t.Setenv("BLAZE_SESSION_TOKEN", "")
	if llm.SignedIn() {
		t.Fatal("logout not seen on the next read")
	}
}

func TestParseBlazeModel(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	cfg := &config.Config{}
	for in, want := range map[string]string{"blaze": "blaze-auto", "blaze-code": "blaze-code", "blaze/blaze-agent": "blaze-agent", "BLAZE-AUTO": "blaze-auto"} {
		e, err := parseModel(cfg, in)
		if err != nil || e.Provider != llm.BlazeID || e.Model != want {
			t.Errorf("%s: %v %v", in, e, err)
		}
	}
	if e, _ := parseModel(cfg, "gemini/gemini-3.8-flash"); e.Provider != "gemini" {
		t.Errorf("own model parsed as %v", e)
	}
}

func TestModelMenuListsBlaze(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	t.Setenv("BLAZE_SESSION_TOKEN", "bz_x")
	cfg := &config.Config{NoKeyless: true}
	m := modelMenu("", cfg)
	var names []string
	for _, it := range m.Items {
		names = append(names, it.Name)
	}
	got := strings.Join(names, " ")
	for _, want := range []string{"blaze-auto", "blaze-code", "blaze-agent", "auto"} {
		if !strings.Contains(got, want) {
			t.Errorf("menu %q lacks %s", got, want)
		}
	}
	for _, it := range m.Items {
		if it.Name == "blaze-auto" && !strings.Contains(it.Desc, "in use") {
			t.Errorf("blaze-auto not in use: %q", it.Desc)
		}
	}
}

func TestCloudRouteRow(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	t.Setenv("BLAZE_GATEWAY_URL", "")
	if r := cloudRouteRow(); !strings.Contains(r, "blaze-auto") || !strings.Contains(r, "ai.blaze-app.workers.dev") {
		t.Fatalf("row = %q", r)
	}
}
