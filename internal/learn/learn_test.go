package learn

import (
	"strings"
	"testing"
)

func TestTrivial(t *testing.T) {
	for _, s := range []string{"thanks!", "ok", "hi", "continue", "yes do it", ""} {
		if !Trivial(s) {
			t.Errorf("%q should be trivial", s)
		}
	}
	for _, s := range []string{"remember I use pnpm", "fix the failing price test in homeglow"} {
		if Trivial(s) {
			t.Errorf("%q should not be trivial", s)
		}
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name   string
		turn   Turn
		repeat bool
		want   Decision
	}{
		{"small talk", Turn{User: "thanks!"}, false, Decision{}},
		{"preference", Turn{User: "From now on use pnpm, never npm, in my projects."}, false, Decision{Reflect: true, Memory: true}},
		{"preference already saved", Turn{User: "I prefer short answers", SavedMemory: true, Tools: []string{"remember"}}, false, Decision{}},
		{"plain task", Turn{User: "what's the capital of Zambia", Tools: []string{"web_search"}}, false, Decision{}},
		{"taught method", Turn{User: "Here's how I deploy: build with pnpm, then rsync dist/ to the server and purge the CDN."}, false, Decision{Reflect: true, Skill: true}},
		{"long workflow, first time", Turn{User: "set up the postgres container for the app", Tools: []string{"run_shell", "file_write", "run_shell", "run_shell", "file_edit"}}, false, Decision{}},
		{"long workflow, repeated", Turn{User: "set up the postgres container for the app", Tools: []string{"run_shell", "file_write", "run_shell", "run_shell", "file_edit"}}, true, Decision{Reflect: true, Skill: true}},
		{"skill already written", Turn{User: "Here's how I deploy, save it as a skill", SavedSkill: true}, false, Decision{}},
		{"workflow with a go-ahead", Turn{User: "go ahead", Tools: []string{"run_shell", "run_shell", "file_edit", "run_shell", "git"}}, true, Decision{Reflect: true, Skill: true}},
	}
	for _, c := range cases {
		got := Decide(c.turn, c.repeat)
		if got.Reflect != c.want.Reflect || got.Memory != c.want.Memory || got.Skill != c.want.Skill {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
		if got.Why == "" {
			t.Errorf("%s: no reason", c.name)
		}
	}
}

func TestStepsIgnoreMemoryTools(t *testing.T) {
	s, d := Steps([]string{"recall", "run_shell", "run_shell", "remember", "file_edit", "load_skill"})
	if s != 3 || d != 2 {
		t.Fatalf("got %d steps %d distinct", s, d)
	}
}

func TestSignatureRepeats(t *testing.T) {
	a := Signature("Deploy the HomeGlow theme to Shopify", []string{"run_shell", "git", "run_shell"})
	b := Signature("deploy homeglow theme to shopify again please", []string{"git", "run_shell", "recall"})
	c := Signature("Write a cover letter for the Upwork job", []string{"web_search", "file_write"})
	if !Repeats(b, [][]string{c, a}) {
		t.Fatalf("same job should repeat: %v vs %v (%.2f)", a, b, Similarity(a, b))
	}
	if Repeats(c, [][]string{a}) {
		t.Fatalf("different jobs matched (%.2f)", Similarity(a, c))
	}
}

func TestAccept(t *testing.T) {
	good := []string{
		"Uses pnpm, not npm, in the HomeGlow repo.",
		"Prefers short answers with the result first.",
		"Luke's PC runs Arch Linux (Omarchy) with Hyprland.",
	}
	for _, f := range good {
		if _, ok, why := Accept(f); !ok {
			t.Errorf("%q rejected: %s", f, why)
		}
	}
	bad := []string{
		"pnpm",
		"The user's OpenAI key is sk-abc123def456ghi789",
		"Card number 4111 1111 1111 1111 expires soon",
		"My password is hunter2 for the store",
		"The build is failing right now on main",
		"The user asked to fix the price test",
		"Is working on the hero section today",
		strings.Repeat("word ", 60),
	}
	for _, f := range bad {
		if _, ok, _ := Accept(f); ok {
			t.Errorf("%q should be rejected", f)
		}
	}
}

func TestParse(t *testing.T) {
	raw := "Sure!\n```json\n{\"memories\":[{\"text\":\"Deploys HomeGlow with pnpm build then rsync to the VPS.\",\"replaces\":\"#12\"},{\"text\":\"today he is tired\"},{\"text\":\"Prefers Inter for UI text.\",\"replaces\":\"oops\"},{\"text\":\"Third valid fact about his setup here.\"}],\"skill\":{\"name\":\"deploy-homeglow\",\"description\":\"Deploy the HomeGlow Shopify theme to production\",\"body\":\"1. pnpm build\\n2. rsync -a dist/ vps:/srv/homeglow\\n3. purge the CDN cache and open the site to check\\n\\nPitfall: run the tests first.\"}}\n```"
	r, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Memories) != 2 || r.Memories[0].Replaces != "#12" || r.Memories[1].Replaces != "" {
		t.Fatalf("memories: %+v", r.Memories)
	}
	if r.Skill == nil || r.Skill.Name != "deploy-homeglow" {
		t.Fatalf("skill dropped: %+v", r.Skill)
	}
	r, _ = Parse(`{"memories":[],"skill":{"name":"Bad Name","description":"x","body":"do it"}}`)
	if r.Skill != nil {
		t.Fatal("weak skill accepted")
	}
	if _, err := Parse("nothing to save"); err == nil {
		t.Fatal("expected an error without JSON")
	}
}

func TestRulesFollowSetting(t *testing.T) {
	if !strings.Contains(Rules(true), "replaces") || !strings.Contains(Rules(false), "OFF") {
		t.Fatal("rules don't follow the setting")
	}
}

func TestPromptMentionsSkillOnlyWhenAllowed(t *testing.T) {
	p := Prompt(Turn{User: "I prefer tabs"}, Decision{Reflect: true, Memory: true}, []string{"#3 Uses spaces"}, []string{"git-pr"})
	if !strings.Contains(p, "skill: null") || !strings.Contains(p, "#3 Uses spaces") || strings.Contains(p, "Existing skills") {
		t.Fatalf("prompt: %s", p)
	}
}
