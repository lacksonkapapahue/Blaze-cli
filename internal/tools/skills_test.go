package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lacksonkapapahue/blaze-cli/internal/skillpack"
)

func testPack() fstest.MapFS {
	md := func(n, d string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(skillpack.Render(skillpack.Meta{Name: n, Description: d, Tags: []string{"t"}, Version: 1}, "1. step one\n2. step two\n"))}
	}
	return fstest.MapFS{
		"web-design-demo/SKILL.md": md("web-design-demo", "Design websites in the demo style"),
		"git-demo/SKILL.md":        md("git-demo", "Branch, commit and open a pull request"),
	}
}

func TestSeedSkillsIntoFolderAndIndex(t *testing.T) {
	d := t.TempDir()
	t.Setenv("BLAZE_HOME", d)
	r, err := SeedSkills(testPack())
	if err != nil || len(r.Added) != 2 {
		t.Fatalf("seed: %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(d, "skills", "git-demo", "SKILL.md")); err != nil {
		t.Fatal("not written to ~/.blaze/skills")
	}
	if !strings.Contains(SkillsFor("open a pull request"), "git-demo") {
		t.Fatal("seeded skill not in the prompt")
	}
	if !strings.Contains(loadSkill("git-demo"), "step two") {
		t.Fatal("load_skill can't read a seeded skill")
	}
	// Disabled skills leave the prompt but stay listed.
	if err := SetSkillEnabled("git-demo", false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(SkillsFor("open a pull request"), "git-demo") {
		t.Fatal("disabled skill still in the prompt")
	}
	var seen bool
	for _, v := range SkillStatus() {
		seen = seen || v.Name == "git-demo" && !v.Enabled && v.State == skillpack.StateBundled
	}
	if !seen {
		t.Fatalf("status: %+v", SkillStatus())
	}
	// Delete, re-seed (stays gone), restore.
	if err := DeleteSkill("web-design-demo"); err != nil {
		t.Fatal(err)
	}
	if r, _ := SeedSkills(testPack()); r.Changed() {
		t.Fatal("deleted bundled skill came back")
	}
	if err := RestoreSkill("web-design-demo"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loadSkill("web-design-demo"), "step one") {
		t.Fatal("restore failed")
	}
}

func TestWriteSkillEventsAndProtection(t *testing.T) {
	d := t.TempDir()
	t.Setenv("BLAZE_HOME", d)
	var evs []LearnEvent
	SetOnLearn(func(e LearnEvent) { evs = append(evs, e) })
	defer SetOnLearn(nil)
	_, _ = SeedSkills(testPack())
	if r := Run(&Env{}, "write_skill", `{"name":"Bad Name","description":"x","body":"y"}`); !strings.HasPrefix(r, "Error") {
		t.Fatal("bad name accepted: " + r)
	}
	if r := Run(&Env{}, "write_skill", `{"name":"deploy-homeglow","description":"Deploy HomeGlow","body":"1. pnpm build","tags":"deploy, shopify"}`); !strings.HasPrefix(r, "Saved skill") {
		t.Fatal(r)
	}
	// Blaze improves a bundled skill: an edit, so the next seed must keep it.
	if r := Run(&Env{}, "write_skill", `{"name":"git-demo","description":"Branch, commit, PR","body":"1. better way"}`); !strings.HasPrefix(r, "Updated skill") {
		t.Fatal(r)
	}
	if len(evs) != 2 || evs[0].Action != "created" || evs[1].Action != "edited" || evs[1].Kind != "skill" {
		t.Fatalf("events: %+v", evs)
	}
	b, _ := os.ReadFile(filepath.Join(d, "skills", "git-demo", "SKILL.md"))
	if !strings.Contains(string(b), "version: 2") || !strings.Contains(string(b), "better way") {
		t.Fatalf("rewrite: %s", b)
	}
	if r, _ := SeedSkills(testPack()); len(r.Edited) != 1 || r.Changed() {
		t.Fatalf("seed overwrote Blaze's improvement: %+v", r)
	}
	b2, _ := os.ReadFile(filepath.Join(d, "skills", "deploy-homeglow", "SKILL.md"))
	if !strings.Contains(string(b2), "tags: [deploy, shopify]") {
		t.Fatalf("tags: %s", b2)
	}
}

func TestRememberReplacesAndNudges(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	var evs []LearnEvent
	SetOnLearn(func(e LearnEvent) { evs = append(evs, e) })
	defer SetOnLearn(nil)
	r := Run(&Env{}, "remember", `{"fact":"Deploys the HomeGlow theme with npm run build"}`)
	if !strings.HasPrefix(r, "Remembered (#1)") {
		t.Fatal(r)
	}
	if r := Run(&Env{}, "remember", `{"fact":"deploys the homeglow theme with npm run build."}`); !strings.HasPrefix(r, "Already remembered") {
		t.Fatal(r)
	}
	r = Run(&Env{}, "remember", `{"fact":"Deploys the HomeGlow theme with pnpm build and rsync"}`)
	if !strings.Contains(r, "Similar saved memories") || !strings.Contains(r, "#1") {
		t.Fatalf("no nudge: %s", r)
	}
	r = Run(&Env{}, "remember", `{"fact":"Ships HomeGlow from CI now, not by hand","replaces":"#1"}`)
	if !strings.HasPrefix(r, "Replaced memory #1") {
		t.Fatal(r)
	}
	if m := Memory(); strings.Contains(m, "npm run build") || !strings.Contains(m, "from CI now, not by hand [#1]") {
		t.Fatalf("memory after replace: %s", m)
	}
	if r := Run(&Env{}, "recall", `{"query":"rsync deploy"}`); !strings.Contains(r, "#2") {
		t.Fatal(r)
	}
	if r := Run(&Env{}, "forget", `{"text":"#2"}`); !strings.HasPrefix(r, "Forgot 1") {
		t.Fatal(r)
	}
	acts := []string{}
	for _, e := range evs {
		acts = append(acts, e.Action)
	}
	if strings.Join(acts, ",") != "saved,saved,replaced,forgot" {
		t.Fatalf("events: %v", acts)
	}
}

func TestPromptListsMatchingSkills(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	for i := 0; i < 14; i++ {
		_, _ = WriteSkill(fmt.Sprintf("skill-%02d", i), "Generic helper number for chores", nil, "1. a")
	}
	_, _ = WriteSkill("deploy-homeglow", "Deploy the HomeGlow Shopify theme", []string{"deploy", "shopify"}, "1. pnpm build")
	p := SkillsFor("please deploy the shopify theme")
	if !strings.Contains(p, "deploy-homeglow") || !strings.Contains(p, "10 of 15") {
		t.Fatalf("prompt: %s", p)
	}
}
