package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "blaze.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestSchemaVersion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "blaze.db")
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Version() != len(migrations) {
		t.Fatalf("user_version %d, want %d", d.Version(), len(migrations))
	}
	d.Close()
	d, err = Open(p) // reopening runs no migration twice
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
}

func TestImportMarkdown(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "memory.md")
	_ = os.WriteFile(md, []byte("# Notes\n- Uses pnpm, not npm (2026-10-09)\n- Likes short answers\n\n- Likes short answers\n* Deploys HomeGlow with shopify theme push\n```\n"), 0o600)
	// The v0.5 sidecar: keyed by sha1 of the normalised text.
	meta := `{"version":1,"items":{"` + legacyID("Uses pnpm, not npm", 0) + `":{"created":"2026-10-09T10:00:00Z","project":"p1","chat":"c1","category":"Setup"}},"sorted_at":"2026-10-10T08:00:00Z"}`
	_ = os.WriteFile(filepath.Join(dir, "memory.meta.json"), []byte(meta), 0o600)
	d := open(t)
	n, err := d.ImportMarkdown(md)
	if err != nil || n != 3 {
		t.Fatalf("imported %d (%v), want 3 distinct facts", n, err)
	}
	ms := d.Memories()
	if len(ms) != 3 || ms[0].Text != "Uses pnpm, not npm" {
		t.Fatalf("bad import: %+v", ms)
	}
	if ms[0].Project != "p1" || ms[0].Chat != "c1" || ms[0].Category != "Setup" || ms[0].Created.UTC().Format("2006-01-02") != "2026-10-09" {
		t.Errorf("sidecar metadata lost: %+v", ms[0])
	}
	if d.Meta("mem_sorted_at") == "" {
		t.Error("sorted_at should carry over")
	}
	if _, err := os.Stat(md + ".migrated"); err != nil {
		t.Error("memory.md should be kept as memory.md.migrated")
	}
	if _, err := os.Stat(md); !os.IsNotExist(err) {
		t.Error("memory.md should be renamed")
	}
	// Idempotent: the same file appearing again adds nothing new.
	_ = os.WriteFile(md, []byte("- Likes short answers (2026-10-01)\n- Has a cat called Mo\n"), 0o600)
	if n, _ := d.ImportMarkdown(md); n != 1 || d.CountMemories() != 4 {
		t.Errorf("re-import added %d, total %d; want 1 and 4", n, d.CountMemories())
	}
	if n, _ := d.ImportMarkdown(md); n != 0 {
		t.Error("a missing memory.md imports nothing")
	}
}

func TestSearchRanking(t *testing.T) {
	d := open(t)
	for _, f := range []string{
		"Uses pnpm, not npm",
		"HomeGlow deploys with shopify theme push",
		"HomeGlow tests run with pnpm test",
		"Prefers short answers without emoji",
		"Lives in Lusaka, Zambia",
		"Shopify store HomeGlow sells home lighting",
	} {
		if _, st, err := d.Add(f, "", "", time.Time{}); err != nil || st != Added {
			t.Fatal(f, st, err)
		}
	}
	r := d.Search("how do I deploy the homeglow theme?", 3)
	if len(r) == 0 || r[0].Text != "HomeGlow deploys with shopify theme push" {
		t.Fatalf("best match should be the deploy fact (porter: deploy~deploys), got %+v", r)
	}
	if got := d.Search("emoji", 5); len(got) != 1 {
		t.Errorf("emoji: %+v", got)
	}
	if got := d.Search("the and of", 5); got != nil {
		t.Errorf("stopwords alone should match nothing, got %+v", got)
	}
	_ = d.Search(`"quotes" AND (weird*) NEAR: -x ^y`, 5) // FTS syntax characters are quoted away, never an error
}

func TestDedupeAndForget(t *testing.T) {
	d := open(t)
	id, st, _ := d.Add("Uses pnpm, not npm", "", "", time.Time{})
	if st != Added {
		t.Fatal(st)
	}
	if id2, st, _ := d.Add("  uses PNPM not npm. ", "", "", time.Time{}); st != Exists || id2 != id {
		t.Errorf("identical text: %s %d", st, id2)
	}
	_, _, _ = d.Add("Favourite editor is Neovim with LazyVim config", "", "", time.Time{})
	id3, st, _ := d.Add("Favourite editor is Neovim with the LazyVim config", "", "", time.Time{})
	if st != Updated {
		t.Errorf("near-identical should update, got %s", st)
	}
	if m, _ := d.Memory(id3); !strings.Contains(m.Text, "the LazyVim") {
		t.Errorf("newer wording should win: %q", m.Text)
	}
	if _, st, _ := d.Add("Favourite drink is coffee", "", "", time.Time{}); st != Added {
		t.Errorf("a different fact must be added, got %s", st)
	}
	if d.CountMemories() != 3 {
		t.Fatalf("have %d memories, want 3", d.CountMemories())
	}
	if gone := d.ForgetMatching("favourite"); len(gone) != 2 {
		t.Errorf("forget by text removed %d", len(gone))
	}
	if err := d.Delete(id); err != nil || d.CountMemories() != 0 {
		t.Errorf("forget by id: %v, left %d", err, d.CountMemories())
	}
	if d.Delete(id) != ErrNotFound {
		t.Error("deleting twice should say not found")
	}
	if len(d.Search("pnpm", 5)) != 0 {
		t.Error("the FTS index must follow deletes")
	}
}

func TestForPrompt(t *testing.T) {
	d := open(t)
	pid, _, _ := d.Add("Name is Luke", "", "", time.Time{})
	_ = d.SetPinned(pid, true)
	for i := 0; i < 50; i++ {
		_, _, _ = d.Add("Filler fact number "+strings.Repeat("x", i%7+1)+" about topic"+string(rune('a'+i%26))+string(rune('a'+i/26)), "", "", time.Time{})
	}
	_, _, _ = d.Add("HomeGlow deploys with shopify theme push", "", "", time.Time{})
	got := d.ForPrompt("deploy homeglow", 8, 600)
	if len(got) < 2 || got[0].ID != pid || got[1].Text != "HomeGlow deploys with shopify theme push" {
		t.Fatalf("want pinned then the match, got %+v", got)
	}
	tok := 0
	for _, m := range got {
		tok += Tokens(m.Text)
	}
	if tok > 600 {
		t.Errorf("over budget: %d tokens", tok)
	}
	if m, _ := d.Memory(pid); m.Uses != 1 || m.LastUsed.IsZero() {
		t.Errorf("injected memories are touched: %+v", m)
	}
	// Nothing matches: pinned + a few most used, never the whole table.
	if got := d.ForPrompt("hello", 8, 600); len(got) > 4 {
		t.Errorf("no-match turn got %d memories", len(got))
	}
	if got := d.ForPrompt("deploy", 8, 4); len(got) != 0 {
		t.Errorf("a tiny budget fits nothing, got %d", len(got))
	}
}

func TestReplaceAll(t *testing.T) {
	d := open(t)
	id, _, _ := d.Add("Uses pnpm", "p1", "", time.Time{})
	_, _, _ = d.Add("Old fact", "", "", time.Time{})
	d.ReplaceAll([]string{"Uses pnpm", "New fact"})
	ms := d.Memories()
	if len(ms) != 2 || ms[0].ID != id || ms[0].Project != "p1" || ms[1].Text != "New fact" {
		t.Errorf("replace should keep unchanged rows: %+v", ms)
	}
}

func TestToolsIndexAndActivation(t *testing.T) {
	d := open(t)
	rows := []ToolRow{
		{"download_file", "Download a file to disk (default ~/Downloads).", "web files download"},
		{"schedule", "Schedule a shell command or a Blaze prompt to run by itself.", "schedule cron timer"},
		{"install_tool", "Install a program with the package manager.", "install packages"},
		{"see_screen", "Look at the user's screen with a vision model.", "screen vision"},
	}
	if err := d.SyncTools(rows); err != nil {
		t.Fatal(err)
	}
	if err := d.SyncTools(rows); err != nil { // unchanged: a no-op
		t.Fatal(err)
	}
	if got := d.SearchTools("download a pdf", 3); len(got) == 0 || got[0] != "download_file" {
		t.Errorf("download: %v", got)
	}
	if got := d.SearchTools("run this every morning cron", 3); len(got) == 0 || got[0] != "schedule" {
		t.Errorf("schedule: %v", got)
	}
	if got := d.SearchTools("screenshot screen", 3); len(got) == 0 || got[0] != "see_screen" {
		t.Errorf("screen: %v", got)
	}
	_ = d.Activate("chat:1", "schedule", "download_file")
	_ = d.Activate("chat:1", "schedule")
	if a := d.Active("chat:1"); strings.Join(a, ",") != "download_file,schedule" {
		t.Errorf("active: %v", a)
	}
	if len(d.Active("chat:2")) != 0 {
		t.Error("activation is per conversation")
	}
	if err := d.SyncTools(rows[:1]); err != nil || len(d.SearchTools("schedule", 3)) != 0 {
		t.Error("a changed registry rebuilds the index")
	}
}

func TestSkillsIndex(t *testing.T) {
	d := open(t)
	_ = d.ReplaceSkills([]Skill{
		{Name: "deploy-site", Desc: "Build and push the HomeGlow theme", Path: "/s/deploy-site/SKILL.md", Body: "Run shopify theme push", Mtime: 1},
		{Name: "pg-docker", Desc: "Start Postgres in Docker", Path: "/s/pg-docker/SKILL.md", Body: "docker run postgres", Mtime: 2},
	})
	if got := d.SearchSkills("deploy my site", 1); len(got) != 1 || got[0].Name != "deploy-site" {
		t.Errorf("skills search: %+v", got)
	}
	if got := d.SearchSkills("postgres database", 1); len(got) != 1 || got[0].Name != "pg-docker" {
		t.Errorf("skills search: %+v", got)
	}
	if m := d.SkillMtimes(); m["/s/pg-docker/SKILL.md"] != 2 {
		t.Errorf("mtimes: %v", m)
	}
	if !strings.Contains(d.SkillBody("/s/pg-docker/SKILL.md"), "docker run") {
		t.Error("body")
	}
}

func TestRelated(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	old, _, _ := d.Add("Deploys the HomeGlow theme with npm run build", "", "", time.Time{})
	_, _, _ = d.Add("Likes coffee in the morning", "", "", time.Time{})
	nid, _, _ := d.Add("Deploys the HomeGlow theme with pnpm build and rsync", "", "", time.Time{})
	rel := d.Related("Deploys the HomeGlow theme with pnpm build and rsync", 0.3, 3, nid)
	if len(rel) != 1 || rel[0].ID != old {
		t.Fatalf("related: %+v", rel)
	}
}
