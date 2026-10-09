package agent

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

// ProjectInstructions reads BLAZE.md (or AGENTS.md / CLAUDE.md) from the
// project folder, plus a global ~/.blaze/BLAZE.md, like Claude Code's CLAUDE.md.
func ProjectInstructions(root string) string {
	var parts []string
	if b, err := os.ReadFile(filepath.Join(config.Dir(), "BLAZE.md")); err == nil {
		parts = append(parts, strings.TrimSpace(string(b)))
	}
	for _, n := range []string{"BLAZE.md", "AGENTS.md", "CLAUDE.md"} {
		if b, err := os.ReadFile(filepath.Join(root, n)); err == nil {
			parts = append(parts, strings.TrimSpace(string(b)))
			break
		}
	}
	s := strings.Join(parts, "\n\n")
	if len(s) > 12000 {
		s = s[:12000] + "\n…(cut)"
	}
	return s
}

func sessionFile(root string) string {
	h := sha1.Sum([]byte(root))
	return filepath.Join(config.Dir(), "sessions", hex.EncodeToString(h[:6])+".json")
}

// Save keeps this folder's conversation so `blaze -c` or /resume can pick it up.
func (a *Agent) Save() {
	if len(a.History) == 0 {
		return
	}
	f := sessionFile(a.Env.Root)
	_ = os.MkdirAll(filepath.Dir(f), 0o700)
	if b, err := json.Marshal(a.History); err == nil {
		_ = os.WriteFile(f, b, 0o600)
	}
}

// Resume loads the last conversation in this folder; it returns the number of messages.
func (a *Agent) Resume() (int, error) {
	b, err := os.ReadFile(sessionFile(a.Env.Root))
	if err != nil {
		return 0, errors.New("no earlier conversation in this folder")
	}
	var h []llm.Message
	if err := json.Unmarshal(b, &h); err != nil {
		return 0, err
	}
	a.History = h
	return len(h), nil
}

// LastUser returns the last thing the user typed in the loaded history.
func (a *Agent) LastUser() string {
	for i := len(a.History) - 1; i >= 0; i-- {
		if a.History[i].Role == "user" {
			return a.History[i].Content
		}
	}
	return ""
}

// Compact swaps the conversation for a tight summary, freeing room in the context.
func (a *Agent) Compact(ctx context.Context) (int, error) {
	if len(a.History) < 2 {
		return 0, errors.New("nothing to compact yet")
	}
	var b strings.Builder
	for _, m := range a.History {
		c := m.Content
		if m.Role == "tool" && len(c) > 1500 {
			c = c[:1500] + "…"
		}
		for _, tc := range m.ToolCalls {
			c += "\n[called " + tc.Function.Name + " " + tc.Function.Arguments + "]"
		}
		b.WriteString(strings.ToUpper(m.Role) + ": " + c + "\n\n")
	}
	tr := b.String()
	if len(tr) > 120000 {
		tr = tr[len(tr)-120000:]
	}
	msgs := []llm.Message{
		{Role: "system", Content: "You compress a coding-assistant conversation into a handoff note for the same assistant. Keep: the user's goals and preferences, decisions made, files touched and why, commands that worked, open problems and the next step. Drop chit-chat and raw tool output. Plain Markdown, under 300 words."},
		{Role: "user", Content: tr},
	}
	r, err := llm.Complete(ctx, a.Cfg, msgs, nil, nil)
	if err != nil {
		return 0, err
	}
	before := len(a.History)
	a.History = []llm.Message{
		{Role: "user", Content: "Summary of our conversation so far:\n\n" + r.Text},
		{Role: "assistant", Content: "Got it, I have the context. What next?"},
	}
	a.Save()
	return before, nil
}
