package store

// Tests for sharing blaze.db with Blaze Desktop: first-run memory.md import,
// the on-disk schema Desktop expects, several processes writing at once, and a
// database from a newer Blaze.

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// desktopV1 is the sha256 of Blaze Desktop's migration 1 (internal/store/store.go,
// v0.7.0). If this fails, the schema drifted from Desktop's: copy Desktop's
// migrations verbatim instead of editing them here.
const desktopV1Hash = "5af1301355adeefd58bd49413f552180539a6b881c687ac248007f2e943e7ea1"

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestSchemaMatchesDesktop(t *testing.T) {
	if len(migrations) != 1 {
		t.Fatalf("%d migrations; Desktop v0.7.0 has 1. Update this test with Desktop's new ones.", len(migrations))
	}
	if got := sha(migrations[0]); got != desktopV1Hash {
		t.Fatalf("migration 1 sha256 %s, Desktop's is %s: the schema drifted", got, desktopV1Hash)
	}
}

func TestFirstRunImportOnceAndBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BLAZE_HOME", home)
	md := filepath.Join(home, "memory.md")
	_ = os.WriteFile(md, []byte("- Uses pnpm (2026-10-01)\n- Lives in Ndola\n- uses PNPM.\n"), 0o600)
	d, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if n := d.CountMemories(); n != 2 {
		t.Fatalf("imported %d, want 2", n)
	}
	if _, err := os.Stat(md); !os.IsNotExist(err) {
		t.Fatal("memory.md should be moved aside after the import")
	}
	old, err := os.ReadFile(md + ".migrated")
	if err != nil || !strings.Contains(string(old), "Lives in Ndola") {
		t.Fatal("memory.md.migrated backup missing")
	}
	// Writes go to the database only; memory.md never comes back.
	if _, st, err := d.Add("Studies at Northrise", "", "", time.Time{}); err != nil || st != Added {
		t.Fatal(st, err)
	}
	if _, err := os.Stat(md); !os.IsNotExist(err) {
		t.Fatal("the CLI must not write memory.md any more")
	}
	// Another memory.md turning up (an old Desktop or a hand edit) is imported
	// without duplicates, and the first backup is kept.
	_ = os.WriteFile(md, []byte("- Uses pnpm\n- Studies at Northrise\n- Has a Samsung phone\n"), 0o600)
	n, err := d.ImportMarkdown(md)
	if err != nil || n != 1 || d.CountMemories() != 4 {
		t.Fatalf("re-import added %d (%v), total %d; want 1 and 4", n, err, d.CountMemories())
	}
	if b, _ := os.ReadFile(md + ".migrated"); !bytes.Equal(b, old) {
		t.Fatal("an older memory.md.migrated was overwritten")
	}
	m, _ := filepath.Glob(md + ".migrated-*")
	if len(m) != 1 {
		t.Fatalf("second backup: %v", m)
	}
	// Re-opening (next CLI start) imports nothing.
	d.Close()
	defMu.Lock()
	defDB = nil
	defMu.Unlock()
	d, err = Default()
	if err != nil || d.CountMemories() != 4 {
		t.Fatal(err, d.CountMemories())
	}
}

// rawOpen opens the file the way another program (Desktop) would: its own
// connection, no help from this package.
func rawOpen(t *testing.T, p string) *sql.DB {
	t.Helper()
	s, err := sql.Open("sqlite", "file:"+filepath.ToSlash(p)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCLIWriteVisibleToDesktop(t *testing.T) {
	p := filepath.Join(t.TempDir(), "blaze.db")
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	id, _, err := d.Add("Prefers dark mode in every editor", "", "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.ReplaceSkills([]Skill{{Name: "deploy-homeglow", Desc: "Deploy the theme", Path: "/x/skills/deploy-homeglow/SKILL.md", Body: "1. pnpm build", Mtime: 7}})

	desk := rawOpen(t, p) // stays open, like Desktop running
	var v int
	_ = desk.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 1 {
		t.Fatalf("user_version %d", v)
	}
	var mode string
	_ = desk.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Fatalf("journal_mode %s, want wal", mode)
	}
	// Desktop's own queries (memories.go / index.go).
	var gotID int64
	var text, norm string
	if err := desk.QueryRow(`SELECT m.id, m.text, m.norm FROM memories_fts f JOIN memories m ON m.id=f.rowid WHERE memories_fts MATCH '"dark"' ORDER BY bm25(memories_fts)`).Scan(&gotID, &text, &norm); err != nil {
		t.Fatal(err)
	}
	if gotID != id || norm != Norm(text) {
		t.Fatalf("row %d %q %q", gotID, text, norm)
	}
	var sk string
	if err := desk.QueryRow(`SELECT s.name FROM skills_fts f JOIN skills s ON s.path=f.path WHERE skills_fts MATCH '"homeglow"'`).Scan(&sk); err != nil || sk != "deploy-homeglow" {
		t.Fatal(sk, err)
	}
	// And the other way: a Desktop write is visible to the CLI's open handle at once.
	now := time.Now().Unix()
	if _, err := desk.Exec(`INSERT INTO memories(text, norm, created, updated) VALUES(?,?,?,?)`, "Runs Arch Linux with Hyprland", Norm("Runs Arch Linux with Hyprland"), now, now); err != nil {
		t.Fatal(err)
	}
	if ms := d.Search("hyprland", 5); len(ms) != 1 {
		t.Fatalf("CLI doesn't see Desktop's write: %+v", ms)
	}
}

// TestHelperWriter is a child process for TestConcurrentProcesses.
func TestHelperWriter(t *testing.T) {
	p := os.Getenv("BLAZE_TEST_DB")
	if p == "" {
		t.Skip("helper")
	}
	who := os.Getenv("BLAZE_TEST_WHO")
	n, _ := strconv.Atoi(os.Getenv("BLAZE_TEST_N"))
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for i := 0; i < n; i++ {
		if _, _, err := d.Add(fmt.Sprintf("%s fact number %d about topic%d", who, i, i), "", "", time.Time{}); err != nil {
			t.Fatal(err)
		}
		// Everyone also writes the same shared facts: they must end up once.
		if _, _, err := d.Add(fmt.Sprintf("shared zeta%d", i%10), "", "", time.Time{}); err != nil {
			t.Fatal(err)
		}
		if i%10 == 0 {
			if err := d.ReplaceSkills([]Skill{{Name: who, Path: "/s/" + who, Body: "b"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestConcurrentProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	p := filepath.Join(t.TempDir(), "blaze.db") // fresh: the processes also race the migration
	const n, procs = 150, 4
	var wg sync.WaitGroup
	errs := make(chan error, procs+1)
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := exec.Command(os.Args[0], "-test.run=^TestHelperWriter$", "-test.count=1")
			c.Env = append(os.Environ(), "BLAZE_TEST_DB="+p, "BLAZE_TEST_WHO=proc"+strconv.Itoa(i), "BLAZE_TEST_N="+strconv.Itoa(n))
			if out, err := c.CombinedOutput(); err != nil {
				errs <- fmt.Errorf("proc%d: %v\n%s", i, err, out)
			}
		}(i)
	}
	// This process writes too, through its own handle, while Desktop-style raw
	// readers keep reading.
	wg.Add(1)
	go func() {
		defer wg.Done()
		var d *DB
		var err error
		for k := 0; k < 50; k++ { // the children may be mid-migration
			if d, err = Open(p); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			errs <- err
			return
		}
		defer d.Close()
		for i := 0; i < n; i++ {
			if _, _, err := d.Add(fmt.Sprintf("parent fact number %d about topic%d", i, i), "", "", time.Time{}); err != nil {
				errs <- err
				return
			}
			_ = d.Search("topic", 5)
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// Each "procK fact number i" is distinct except that near-duplicates merge
	// (they differ in one number), so count distinct prefixes instead.
	ms := d.Memories()
	shared := 0
	seen := map[string]bool{}
	for _, m := range ms {
		if seen[m.Text] {
			t.Fatalf("duplicate row %q", m.Text)
		}
		seen[m.Text] = true
		if strings.HasPrefix(m.Text, "shared zeta") {
			shared++
		}
	}
	if shared != 10 {
		t.Fatalf("shared facts: %d rows, want 10", shared)
	}
	var ok string
	_ = d.sql.QueryRow(`PRAGMA integrity_check`).Scan(&ok)
	if ok != "ok" {
		t.Fatal(ok)
	}
	if _, err := d.sql.Exec(`INSERT INTO memories_fts(memories_fts) VALUES('integrity-check')`); err != nil {
		t.Fatalf("fts out of step: %v", err)
	}
	if d.Version() != 1 {
		t.Fatal(d.Version())
	}
}

func fileSum(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha(string(b))
}

func TestNewerSchemaRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "blaze.db")
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = d.Add("Keep me safe", "", "", time.Time{})
	// A future Desktop moves the schema on.
	if _, err := d.sql.Exec(`CREATE TABLE future(x); PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	d.Close() // checkpoints the WAL into the main file
	before := fileSum(t, p)

	if _, err := Open(p); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("got %v, want ErrNewerSchema", err)
	}
	if fileSum(t, p) != before {
		t.Fatal("refusing must not change the database")
	}
	s := rawOpen(t, p)
	var v int
	var txt string
	_ = s.QueryRow(`PRAGMA user_version`).Scan(&v)
	_ = s.QueryRow(`SELECT text FROM memories`).Scan(&txt)
	if v != 2 || txt != "Keep me safe" {
		t.Fatal(v, txt)
	}
}

func TestBrokenSchemaRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "blaze.db")
	s, _ := sql.Open("sqlite", "file:"+filepath.ToSlash(p))
	// Claims v1 but isn't Blaze's schema.
	if _, err := s.Exec(`CREATE TABLE memories(id INTEGER PRIMARY KEY, other TEXT); PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(p); err == nil || !strings.Contains(err.Error(), "missing table") {
		t.Fatalf("got %v", err)
	}
	// Not a database at all.
	q := filepath.Join(t.TempDir(), "blaze.db")
	_ = os.WriteFile(q, []byte("this is not sqlite, just some notes the user kept"), 0o600)
	before := fileSum(t, q)
	if _, err := Open(q); err == nil {
		t.Fatal("opened a non-database")
	}
	if fileSum(t, q) != before {
		t.Fatal("a non-database file was changed")
	}
}

func TestOlderSchemaMigrates(t *testing.T) {
	// An empty file (user_version 0, as a brand-new Desktop or CLI leaves it
	// before migrating) is brought up to date.
	p := filepath.Join(t.TempDir(), "blaze.db")
	s, _ := sql.Open("sqlite", "file:"+filepath.ToSlash(p))
	_, _ = s.Exec(`PRAGMA user_version = 0`)
	s.Close()
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Version() != SchemaVersion() {
		t.Fatal(d.Version())
	}
}
