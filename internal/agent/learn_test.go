package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
)

func TestLearnOnFollowsDesktopAndEnv(t *testing.T) {
	d := t.TempDir()
	t.Setenv("BLAZE_HOME", d)
	t.Setenv("BLAZE_LEARN", "")
	if !LearnOn() {
		t.Fatal("learning should default to on")
	}
	_ = os.WriteFile(filepath.Join(d, "desktop.json"), []byte(`{"learn":{"off":true}}`), 0o600)
	if LearnOn() {
		t.Fatal("desktop's Learn automatically = off should apply to the CLI")
	}
	t.Setenv("BLAZE_LEARN", "on")
	if !LearnOn() {
		t.Fatal("BLAZE_LEARN=on should win")
	}
}

func TestTurnAndLearnLine(t *testing.T) {
	call := func(n string) llm.Message {
		m := llm.Message{Role: "assistant"}
		m.ToolCalls = make([]llm.ToolCall, 1)
		m.ToolCalls[0].Function.Name = n
		return m
	}
	h := []llm.Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "x"},
		{Role: "user", Content: "Here's how I deploy: pnpm build then rsync"}, call("write_skill"), {Role: "tool", Content: "Saved"},
		{Role: "assistant", Content: "Saved it as a skill."}}
	tr := turn(h)
	if tr.User != "Here's how I deploy: pnpm build then rsync" || !tr.SavedSkill || tr.Reply != "Saved it as a skill." {
		t.Fatalf("%+v", tr)
	}
	if l := LearnLine(tools.LearnEvent{Kind: "skill", Action: "created", Text: "deploy-homeglow"}); !strings.Contains(l, "New skill") || !strings.Contains(l, "deploy-homeglow") {
		t.Fatal(l)
	}
}

func TestSetLearnKeepsOtherDesktopPrefs(t *testing.T) {
	d := t.TempDir()
	t.Setenv("BLAZE_HOME", d)
	t.Setenv("BLAZE_LEARN", "")
	p := filepath.Join(d, "desktop.json")
	_ = os.WriteFile(p, []byte(`{"notify":{"mode":"toast"},"learn":{"no_marks":true}}`), 0o600)
	if err := SetLearn(false); err != nil {
		t.Fatal(err)
	}
	if LearnOn() {
		t.Fatal("SetLearn(false) should turn learning off")
	}
	b := compactFile(t, p)
	if !strings.Contains(string(b), `"no_marks":true`) || !strings.Contains(string(b), `"mode":"toast"`) {
		t.Fatalf("other prefs lost: %s", b)
	}
	if err := SetLearn(true); err != nil {
		t.Fatal(err)
	}
	if !LearnOn() {
		t.Fatal("SetLearn(true) should turn learning on")
	}
	b = compactFile(t, p)
	if strings.Contains(string(b), `"off"`) || !strings.Contains(string(b), `"no_marks":true`) {
		t.Fatalf("unexpected desktop.json after turning on: %s", b)
	}
	// a broken desktop.json is never overwritten
	_ = os.WriteFile(p, []byte(`{broken`), 0o600)
	if err := SetLearn(false); err == nil {
		t.Fatal("expected an error on invalid desktop.json")
	}
	if b, _ = os.ReadFile(p); string(b) != `{broken` {
		t.Fatal("invalid desktop.json was overwritten")
	}
}

func TestSetLearnCreatesFile(t *testing.T) {
	d := t.TempDir()
	t.Setenv("BLAZE_HOME", filepath.Join(d, "home"))
	t.Setenv("BLAZE_LEARN", "")
	if err := SetLearn(false); err != nil {
		t.Fatal(err)
	}
	if LearnOn() {
		t.Fatal("learning should be off")
	}
}

func compactFile(t *testing.T, p string) []byte {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("desktop.json is not valid JSON: %v", err)
	}
	return buf.Bytes()
}
