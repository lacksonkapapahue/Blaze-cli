package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/skillpack"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
)

func TestEmbeddedREASkillIsIndexedAndSeeds(t *testing.T) {
	packFS, err := fs.Sub(skillsFS, "skills")
	if err != nil {
		t.Fatal(err)
	}
	pack, err := skillpack.Load(packFS)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, skill := range pack {
		if skill.Name != "reverse-engineer-anything" {
			continue
		}
		found = true
		for _, file := range []string{"SKILL.md", "LICENSE", "SOURCE.md", "references/connection-and-recovery.md"} {
			if _, ok := skill.Files[file]; !ok {
				t.Errorf("embedded REA pack is missing %s", file)
			}
		}
		if !strings.Contains(string(skill.Files["SKILL.md"]), "Only analyse software the user owns") {
			t.Error("REA skill is missing the Blaze legal-use note")
		}
		if len(skill.Tags) < 9 {
			t.Errorf("REA triggers weren't parsed: %v", skill.Tags)
		}
	}
	if !found {
		t.Fatal("REA skill isn't in the embedded pack")
	}

	data, err := os.ReadFile(filepath.Join("skills", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Skills []skillpack.IndexEntry `json:"skills"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("skills/index.json doesn't parse: %v", err)
	}
	var indexed bool
	for _, entry := range index.Skills {
		if entry.Name == "reverse-engineer-anything" {
			indexed = true
			tags := make(map[string]bool, len(entry.Tags))
			for _, tag := range entry.Tags {
				tags[tag] = true
			}
			for _, trigger := range []string{"reverse engineer", "decompile", "how does app do X", "binary", "APK", "Electron app", ".NET assembly", "ASAR", "firmware", "compare versions"} {
				if !tags[trigger] {
					t.Errorf("index doesn't include REA trigger %q: %v", trigger, entry.Tags)
				}
			}
			if !strings.Contains(entry.Description, "Skip REA for ordinary source-code repositories") {
				t.Errorf("REA index description doesn't exclude ordinary source repos: %q", entry.Description)
			}
		}
	}
	if !indexed {
		t.Fatal("skills/index.json has no REA entry")
	}

	home := t.TempDir()
	t.Setenv("BLAZE_HOME", home)
	if _, err := tools.SeedSkills(packFS); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "reverse-engineer-anything", "SKILL.md")); err != nil {
		t.Fatalf("REA skill wasn't seeded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "reverse-engineer-anything", "LICENSE")); err != nil {
		t.Fatalf("REA license wasn't seeded: %v", err)
	}
}

func TestREASkillPromptTriggers(t *testing.T) {
	for prompt, want := range map[string]bool{
		"reverse engineer this app":                                true,
		"decompile this binary":                                    true,
		"how does Firefox do search?":                              true,
		"inspect this binary file":                                 true,
		"analyze this APK":                                         true,
		"understand this Electron app":                             true,
		"inspect this .NET assembly":                               true,
		"extract code from this ASAR":                              true,
		"analyze this firmware image":                              true,
		"compare versions of this app":                             true,
		"explain binary search in this ordinary source repository": false,
		"review this ordinary source-code repository":              false,
	} {
		if got := reaPromptShouldTrigger(prompt); got != want {
			t.Errorf("reaPromptShouldTrigger(%q) = %t, want %t", prompt, got, want)
		}
	}
}
