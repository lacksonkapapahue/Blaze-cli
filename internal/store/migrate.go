package store

// One-time (and repeatable) import of the old memory.md plus its v0.5 sidecar
// memory.meta.json. After a successful import the files are renamed to
// *.migrated as a backup. If memory.md shows up again later (the CLI or a hand
// edit appended to it), the next start imports it again; identical facts are
// skipped, so the import is idempotent. (CLI copy: since v0.6.0 the CLI writes
// blaze.db directly and never writes memory.md again.)

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"
)

var dateSuffix = regexp.MustCompile(`\s*\((\d{4}-\d{2}-\d{2})\)\s*$`)

// ParseLine turns "- Uses pnpm (2026-10-09)" into its text and date. Headings,
// blank lines and code fences are not facts.
func ParseLine(l string) (string, time.Time, bool) {
	t := strings.TrimSpace(l)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "```") {
		return "", time.Time{}, false
	}
	for _, p := range []string{"- [ ] ", "- [x] ", "- ", "* ", "• "} {
		if strings.HasPrefix(t, p) {
			t = strings.TrimSpace(t[len(p):])
			break
		}
	}
	var d time.Time
	if m := dateSuffix.FindStringSubmatch(t); m != nil {
		d, _ = time.ParseInLocation("2006-01-02", m[1], time.Local)
		t = strings.TrimSpace(t[:len(t)-len(m[0])])
	}
	return t, d, t != ""
}

// legacyID is the v0.5 sidecar's key for a fact (sha1 of its normalised text).
func legacyID(text string, n int) string {
	h := sha1.Sum([]byte(strings.Join(strings.Fields(strings.ToLower(text)), " ")))
	id := hex.EncodeToString(h[:6])
	if n > 0 {
		id += "-" + string(rune('a'+n%26))
	}
	return id
}

type legacyMeta struct {
	Created  time.Time `json:"created"`
	Project  string    `json:"project"`
	Chat     string    `json:"chat"`
	Category string    `json:"category"`
}

// ImportMarkdown imports a memory.md (and memory.meta.json beside it) and renames
// both to *.migrated. It returns how many new memories it added.
func (d *DB) ImportMarkdown(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	metaPath := strings.TrimSuffix(path, "memory.md") + "memory.meta.json"
	var ix struct {
		Items    map[string]legacyMeta `json:"items"`
		SortedAt time.Time             `json:"sorted_at"`
	}
	if mb, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(mb, &ix)
	}
	if !ix.SortedAt.IsZero() && d.Meta("mem_sorted_at") == "" {
		_ = d.SetMeta("mem_sorted_at", ix.SortedAt.Format(time.RFC3339))
	}
	seen := map[string]int{}
	added := 0
	for i, l := range strings.Split(string(b), "\n") {
		txt, date, ok := ParseLine(l)
		if !ok {
			continue
		}
		k := strings.Join(strings.Fields(strings.ToLower(txt)), " ")
		m := ix.Items[legacyID(txt, seen[k])]
		seen[k]++
		at := m.Created
		if at.IsZero() {
			at = date
		}
		if at.IsZero() {
			at = time.Now().Add(-time.Hour)
		}
		if m.Created.IsZero() {
			at = at.Add(time.Duration(i) * time.Minute) // same-day facts keep their file order
		}
		id, st, err := d.addExact(txt, m.Project, m.Chat, at)
		if err != nil {
			return added, err
		}
		if st == Added {
			added++
			if m.Category != "" {
				_ = d.SetCategory(id, m.Category)
			}
		}
	}
	backup(path)
	if _, err := os.Stat(metaPath); err == nil {
		backup(metaPath)
	}
	return added, nil
}

// addExact is Add without the near-duplicate merge (an import keeps every distinct line).
func (d *DB) addExact(text, project, chat string, at time.Time) (int64, string, error) {
	text = clean(text)
	n := Norm(text)
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.insertNew(text, n, project, chat, at.Unix(), at.Unix())
}

// backup renames a migrated file to name.migrated, or name.migrated-<time> when
// an older backup (say Desktop's) is already there, so no backup is lost.
func backup(path string) {
	dst := path + ".migrated"
	if _, err := os.Stat(dst); err == nil {
		dst += "-" + time.Now().Format("20060102-150405")
	}
	_ = os.Rename(path, dst)
}
