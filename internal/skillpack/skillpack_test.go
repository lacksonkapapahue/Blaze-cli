package skillpack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func packFS(version int, body string) fstest.MapFS {
	md := Render(Meta{Name: "demo", Description: "A demo skill.", Tags: []string{"test"}, Version: version}, body)
	return fstest.MapFS{
		"demo/SKILL.md":        {Data: []byte(md)},
		"demo/assets/tpl.html": {Data: []byte("<p>" + body + "</p>")},
		"other/SKILL.md":       {Data: []byte(Render(Meta{Name: "other", Description: "Other.", Version: 1}, "Other body."))},
	}
}

func mustLoad(t *testing.T, f fstest.MapFS) []Skill {
	t.Helper()
	p, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseFront(t *testing.T) {
	m, body, ok := ParseFront("---\r\nname: x-y\r\ndescription: \"Use it: always\"\r\ntags: [a, 'b c']\r\nversion: 4\r\n---\r\n\r\n# Hi\r\n")
	if !ok || m.Name != "x-y" || m.Description != "Use it: always" || m.Version != 4 || len(m.Tags) != 2 || m.Tags[1] != "b c" || body != "# Hi\n" {
		t.Fatalf("got %+v %q %v", m, body, ok)
	}
	m, _, _ = ParseFront("---\nname: z\ntags:\n  - one\n  - two\n---\nbody")
	if len(m.Tags) != 2 || m.Tags[0] != "one" {
		t.Fatalf("list tags: %+v", m.Tags)
	}
	if _, _, ok := ParseFront("no front matter"); ok {
		t.Fatal("expected no front matter")
	}
}

func TestLoadRejectsMismatchedName(t *testing.T) {
	_, err := Load(fstest.MapFS{"a/SKILL.md": {Data: []byte("---\nname: b\ndescription: x\n---\n")}})
	if err == nil {
		t.Fatal("expected an error for a name that doesn't match its folder")
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := mustLoad(t, packFS(1, "v1"))
	r, err := Seed(dir, p)
	if err != nil || len(r.Added) != 2 {
		t.Fatalf("first seed: %+v %v", r, err)
	}
	if !strings.Contains(read(t, filepath.Join(dir, "demo", "assets", "tpl.html")), "v1") {
		t.Fatal("asset not written")
	}
	before := read(t, filepath.Join(dir, ManifestName))
	r, err = Seed(dir, p)
	if err != nil || r.Changed() {
		t.Fatalf("second seed changed something: %+v %v", r, err)
	}
	if read(t, filepath.Join(dir, ManifestName)) != before {
		t.Fatal("manifest rewritten on a no-op seed")
	}
}

func TestSeedUpgradesUntouched(t *testing.T) {
	dir := t.TempDir()
	_, _ = Seed(dir, mustLoad(t, packFS(1, "v1")))
	r, _ := Seed(dir, mustLoad(t, packFS(2, "v2")))
	if len(r.Upgraded) != 1 || r.Upgraded[0] != "demo" {
		t.Fatalf("expected demo upgraded: %+v", r)
	}
	if !strings.Contains(read(t, filepath.Join(dir, "demo", "SKILL.md")), "v2") || !strings.Contains(read(t, filepath.Join(dir, "demo", "assets", "tpl.html")), "v2") {
		t.Fatal("upgrade didn't write the new files")
	}
	// An older pack (an older CLI sharing the folder) never downgrades.
	r, _ = Seed(dir, mustLoad(t, packFS(1, "v1")))
	if r.Changed() || !strings.Contains(read(t, filepath.Join(dir, "demo", "SKILL.md")), "v2") {
		t.Fatalf("downgraded: %+v", r)
	}
}

func TestSeedNeverOverwritesEdits(t *testing.T) {
	dir := t.TempDir()
	_, _ = Seed(dir, mustLoad(t, packFS(1, "v1")))
	f := filepath.Join(dir, "demo", "SKILL.md")
	mine := Render(Meta{Name: "demo", Description: "Mine now.", Version: 1}, "my own steps")
	if err := os.WriteFile(f, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	pack2 := mustLoad(t, packFS(2, "v2"))
	r, _ := Seed(dir, pack2)
	if len(r.Edited) != 1 || read(t, f) != mine {
		t.Fatalf("edited skill was overwritten: %+v", r)
	}
	st := Status(dir, pack2)
	if st[0].Name != "demo" || st[0].State != StateEdited || !st[0].Update {
		t.Fatalf("status: %+v", st[0])
	}
	// Reset to bundled brings the shipped v2 back.
	if err := Restore(dir, "demo", pack2); err != nil || !strings.Contains(read(t, f), "v2") {
		t.Fatalf("restore: %v", err)
	}
	if Status(dir, pack2)[0].State != StateBundled {
		t.Fatal("restored skill should read as bundled")
	}
}

func TestDeletedStaysDeleted(t *testing.T) {
	dir := t.TempDir()
	p := mustLoad(t, packFS(1, "v1"))
	_, _ = Seed(dir, p)
	// Removed in Settings.
	if err := Delete(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	r, _ := Seed(dir, mustLoad(t, packFS(2, "v2")))
	if r.Changed() {
		t.Fatalf("deleted skill came back: %+v", r)
	}
	// Removed by hand (rm -r): also respected.
	_ = os.RemoveAll(filepath.Join(dir, "other"))
	r, _ = Seed(dir, p)
	if r.Changed() || len(r.Deleted) != 2 {
		t.Fatalf("hand-deleted skill came back: %+v", r)
	}
	var removed int
	for _, v := range Status(dir, p) {
		if v.State == StateRemoved {
			removed++
		}
	}
	if removed != 2 {
		t.Fatalf("status should list 2 removed bundled skills, got %d", removed)
	}
	if err := Restore(dir, "other", p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other", "SKILL.md")); err != nil {
		t.Fatal("restore didn't write the skill")
	}
}

func TestUserSkillWithBundledNameWins(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "demo", "SKILL.md")
	_ = os.MkdirAll(filepath.Dir(f), 0o755)
	mine := "---\nname: demo\ndescription: mine\n---\nmine"
	_ = os.WriteFile(f, []byte(mine), 0o644)
	r, _ := Seed(dir, mustLoad(t, packFS(1, "v1")))
	if len(r.Shadowed) != 1 || read(t, f) != mine {
		t.Fatalf("user's skill was replaced: %+v", r)
	}
}

func TestCRLFCheckoutCountsAsUnedited(t *testing.T) {
	dir := t.TempDir()
	_, _ = Seed(dir, mustLoad(t, packFS(1, "v1")))
	f := filepath.Join(dir, "demo", "SKILL.md")
	_ = os.WriteFile(f, []byte(strings.ReplaceAll(read(t, f), "\n", "\r\n")), 0o644)
	r, _ := Seed(dir, mustLoad(t, packFS(2, "v2")))
	if len(r.Upgraded) != 1 {
		t.Fatalf("line endings alone shouldn't count as an edit: %+v", r)
	}
}

func TestDisable(t *testing.T) {
	dir := t.TempDir()
	p := mustLoad(t, packFS(1, "v1"))
	_, _ = Seed(dir, p)
	_ = SetEnabled(dir, "demo", false)
	if !DisabledSet(dir)["demo"] {
		t.Fatal("not disabled")
	}
	r, _ := Seed(dir, p)
	if r.Changed() || !DisabledSet(dir)["demo"] {
		t.Fatal("seeding re-enabled a disabled skill")
	}
	_ = SetEnabled(dir, "demo", true)
	if DisabledSet(dir)["demo"] {
		t.Fatal("still disabled")
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"web-design-blaze-classic", "git-pr", "ab"} {
		if !ValidName(ok) {
			t.Errorf("%s should be valid", ok)
		}
	}
	for _, bad := range []string{"", "A", "../x", "a b", "-x", "a--b", "x/y"} {
		if ValidName(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
