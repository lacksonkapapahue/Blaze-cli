package agent

// v0.5 proactive memory & skills (ported from Blaze Desktop v0.7):
//   - the prompt carries learn.Rules and only the skills matching the conversation;
//   - memory/skill changes print a small "◆ Remembered · …" line under the step;
//   - after an interactive turn, learn.Decide (a local check, no model call)
//     decides whether a short reflection call is worth it (at most once a minute).
//
// "Learn automatically" is shared with Blaze Desktop: Settings › Behaviour ›
// Learning (or /learn off, blaze learn off) writes desktop.json
// {"learn":{"off":true}}. BLAZE_LEARN=off|on overrides it for one shell.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/learn"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

// LearnOn reports whether Blaze may save memories and skills on its own.
func LearnOn() bool {
	switch strings.ToLower(os.Getenv("BLAZE_LEARN")) {
	case "off", "0", "false", "no":
		return false
	case "on", "1", "true", "yes":
		return true
	}
	var d struct {
		Learn struct {
			Off bool `json:"off"`
		} `json:"learn"`
	}
	b, err := os.ReadFile(filepath.Join(config.Dir(), "desktop.json"))
	if err == nil {
		_ = json.Unmarshal(b, &d)
	}
	return !d.Learn.Off
}

// SetLearn turns "Learn automatically" on or off. It writes the same setting
// Blaze Desktop uses (desktop.json "learn.off"), keeping every other key in that
// file (including learn.no_marks) as it was.
func SetLearn(on bool) error {
	path := filepath.Join(config.Dir(), "desktop.json")
	top := map[string]json.RawMessage{}
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &top); err != nil {
			return fmt.Errorf("desktop.json is not valid JSON, not touching it: %w", err)
		}
	}
	lp := map[string]json.RawMessage{}
	if raw, ok := top["learn"]; ok {
		_ = json.Unmarshal(raw, &lp)
	}
	if on {
		delete(lp, "off")
	} else {
		lp["off"] = json.RawMessage("true")
	}
	lb, err := json.Marshal(lp)
	if err != nil {
		return err
	}
	top["learn"] = lb
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var learned struct {
	sync.Mutex
	q []tools.LearnEvent
}

// QueueLearn collects memory/skill changes to print after the step (tools.SetOnLearn).
func QueueLearn(ev tools.LearnEvent) {
	learned.Lock()
	learned.q = append(learned.q, ev)
	learned.Unlock()
}

// LearnLine is how one change reads in the terminal.
func LearnLine(ev tools.LearnEvent) string {
	words := map[string]string{"saved": "Remembered", "updated": "Memory updated", "replaced": "Memory updated", "forgot": "Forgot", "created": "New skill", "edited": "Skill updated"}
	w := words[ev.Action]
	if w == "" {
		w = ev.Action
	}
	if ev.Auto {
		w += " · auto"
	}
	t := ev.Text
	if len(t) > 90 {
		t = t[:90] + "…"
	}
	return "  " + ui.Accent("◆") + " " + ui.Sub(w) + ui.Weak(" · "+t)
}

func (a *Agent) printLearned() {
	learned.Lock()
	q := learned.q
	learned.q = nil
	learned.Unlock()
	if a.Quiet {
		return
	}
	for _, ev := range q {
		ui.Println(LearnLine(ev))
	}
}

// recentQuery is what the skill picker matches: the last few user messages and the last reply.
func recentQuery(h []llm.Message) string {
	var parts []string
	users, replies := 0, 0
	for i := len(h) - 1; i >= 0 && users < 3; i-- {
		m := h[i]
		c := m.Content
		switch {
		case m.Role == "user":
			users++
		case m.Role == "assistant" && replies == 0 && c != "":
			replies++
			if len(c) > 400 {
				c = c[:400]
			}
		default:
			continue
		}
		parts = append([]string{c}, parts...)
	}
	return strings.Join(parts, "\n")
}

// turn is the last user turn of a history, for the learning check.
func turn(h []llm.Message) learn.Turn {
	start := -1
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Role == "user" && !strings.HasPrefix(h[i].Content, "[Autopilot]") {
			start = i
			break
		}
	}
	var t learn.Turn
	if start < 0 {
		return t
	}
	t.User = h[start].Content
	for _, m := range h[start+1:] {
		for _, tc := range m.ToolCalls {
			t.Tools = append(t.Tools, tc.Function.Name)
			switch tc.Function.Name {
			case "remember", "forget":
				t.SavedMemory = true
			case "write_skill":
				t.SavedSkill = true
			}
		}
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
			t.Reply = m.Content
		}
	}
	return t
}

var reflectState struct {
	sync.Mutex
	last time.Time
}

// workflowsFile keeps recent workflow signatures so a repeat can be spotted.
func workflowsFile() string { return filepath.Join(config.Dir(), "learn-workflows.json") }

// Reflect runs the end-of-turn learning check after an interactive turn.
func (a *Agent) Reflect(ctx context.Context) {
	if a.Quiet || !LearnOn() {
		return
	}
	t := turn(a.History)
	if t.User == "" || t.Reply == "" {
		return
	}
	var earlier [][]string
	b, _ := os.ReadFile(workflowsFile())
	_ = json.Unmarshal(b, &earlier)
	sig := learn.Signature(t.User, t.Tools)
	dec := learn.Decide(t, learn.Repeats(sig, earlier))
	if steps, _ := learn.Steps(t.Tools); steps >= 3 {
		earlier = append(earlier, sig)
		if len(earlier) > 40 {
			earlier = earlier[len(earlier)-40:]
		}
		nb, _ := json.Marshal(earlier)
		_ = os.WriteFile(workflowsFile(), nb, 0o600)
	}
	if !dec.Reflect {
		return
	}
	reflectState.Lock()
	if time.Since(reflectState.last) < time.Minute {
		reflectState.Unlock()
		return
	}
	reflectState.last = time.Now()
	reflectState.Unlock()

	var known []string
	for _, f := range tools.SearchFacts(t.User, 6) {
		known = append(known, fmt.Sprintf("#%d %s", f.ID, f.Text))
	}
	var skills []string
	if dec.Skill {
		for _, s := range tools.Skills() {
			skills = append(skills, s.Name)
		}
	}
	a.spin.Start("learning")
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	r, err := llm.Complete(cctx, a.Cfg, []llm.Message{{Role: "user", Content: learn.Prompt(t, dec, known, skills)}}, nil, nil)
	cancel()
	a.spin.Stop()
	if err != nil || r == nil {
		return
	}
	ref, err := learn.Parse(r.Text)
	if err != nil {
		return
	}
	for _, m := range ref.Memories {
		if id, ok := parseRef(m.Replaces); ok && tools.ReplaceFact(id, m.Text) == nil {
			QueueLearn(tools.LearnEvent{Kind: "memory", Action: "replaced", Text: m.Text, Ref: fmt.Sprintf("#%d", id), Auto: true})
			continue
		}
		id, st, err := tools.AddFact(m.Text)
		if err != nil || st == "exists" {
			continue
		}
		act := map[bool]string{true: "updated", false: "saved"}[st == "updated"]
		QueueLearn(tools.LearnEvent{Kind: "memory", Action: act, Text: m.Text, Ref: fmt.Sprintf("#%d", id), Auto: true})
	}
	if s := ref.Skill; s != nil && dec.Skill {
		exists := false
		for _, have := range tools.Skills() {
			exists = exists || have.Name == s.Name
		}
		if !exists {
			if created, err := tools.WriteSkill(s.Name, s.Description, s.Tags, s.Body); err == nil && created {
				QueueLearn(tools.LearnEvent{Kind: "skill", Action: "created", Text: s.Name, Ref: s.Name, Auto: true})
			}
		}
	}
	a.printLearned()
}

func parseRef(s string) (int, bool) {
	var n int
	_, err := fmt.Sscanf(strings.TrimPrefix(strings.TrimSpace(s), "#"), "%d", &n)
	return n, err == nil && n > 0
}
