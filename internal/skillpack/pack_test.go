package skillpack

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real pack in the repo's skills/ folder. Set UPDATE_SKILL_INDEX=1 to
// regenerate skills/index.json after editing a skill.
const packDir = "../../skills"

func TestRealPack(t *testing.T) {
	pack, err := Load(os.DirFS(packDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(pack) < 15 {
		t.Fatalf("expected at least 15 bundled skills, got %d", len(pack))
	}
	seen := map[string]bool{}
	for _, s := range pack {
		md := string(s.Files["SKILL.md"])
		_, body, _ := ParseFront(md)
		switch {
		case !ValidName(s.Name):
			t.Errorf("%s: bad name", s.Name)
		case seen[s.Name]:
			t.Errorf("%s: duplicate", s.Name)
		case len(strings.Fields(s.Description)) < 12 || len(s.Description) > 420:
			t.Errorf("%s: description should say what and when in 12+ words (and stay under 420 chars)", s.Name)
		case len(s.Tags) < 3:
			t.Errorf("%s: needs at least 3 tags", s.Name)
		case len(body) < 1500:
			t.Errorf("%s: body too thin (%d chars)", s.Name, len(body))
		case !strings.Contains(body, "- [ ]") && !strings.Contains(strings.ToLower(body), "checklist"):
			t.Errorf("%s: needs a checklist", s.Name)
		}
		seen[s.Name] = true
		for f := range s.Files {
			if f != "SKILL.md" && f != "LICENSE" && f != "SOURCE.md" && !strings.HasPrefix(f, "assets/") && !strings.HasPrefix(f, "references/") {
				t.Errorf("%s: unexpected extra file: %s", s.Name, f)
			}
			if strings.HasPrefix(f, "references/") && !strings.Contains(md, f) {
				t.Errorf("%s: %s is never mentioned in SKILL.md", s.Name, f)
			}
		}
	}
	for _, must := range []string{"web-design-teamofkeys", "web-design-blaze-classic", "skill-creator", "landing-page-builder", "reverse-engineer-anything"} {
		if !seen[must] {
			t.Errorf("missing %s", must)
		}
	}
	idx := Index(pack)
	p := filepath.Join(packDir, "index.json")
	if os.Getenv("UPDATE_SKILL_INDEX") != "" {
		if err := os.WriteFile(p, idx, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	have, _ := os.ReadFile(p)
	if !bytes.Equal(LF(have), idx) {
		t.Fatal("skills/index.json is stale: run UPDATE_SKILL_INDEX=1 go test ./internal/skillpack/")
	}
}
