// Package skillpack is Blaze's bundled skill library: the skills/ folder of the
// desktop repo, embedded in the binary and seeded into the user's skills folder
// (~/.blaze/skills, shared by the desktop app, the CLI and the Android app).
//
// Pack format (easy to port, no YAML library needed):
//
//	skills/<name>/SKILL.md      front matter + Markdown body
//	skills/<name>/assets/...    optional files the skill refers to
//	skills/<name>/references/... supporting Markdown referenced by the skill
//
//	---
//	name: web-design-blaze-classic
//	description: When to use it, one or two sentences.
//	tags: [design, web, css]
//	version: 3
//	---
//	# Body
//
// Seeding is idempotent and version-aware. The state lives in a manifest beside
// the skills (~/.blaze/skills/.bundled.json) so every app that shares the folder
// agrees on it:
//
//   - a bundled skill the user never had is written;
//   - a newer bundled version replaces the old one only when the file on disk is
//     still exactly what Blaze wrote (same sha256), so a skill the user (or Blaze,
//     on their behalf) edited is never overwritten;
//   - a bundled skill the user deleted stays deleted (Restore brings it back);
//   - a user's own skill with the same name as a bundled one wins;
//   - any skill can be disabled (kept on disk, left out of prompts).
package skillpack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ManifestName is the pack state file inside the skills folder.
const ManifestName = ".bundled.json"

// Skill is one skill of the bundled pack.
type Skill struct {
	Name        string
	Description string
	Tags        []string
	Version     int
	SHA256      string            // of SKILL.md (line endings normalised to \n)
	Files       map[string][]byte // path inside the skill folder -> content; always has SKILL.md
}

// Meta is a SKILL.md front matter.
type Meta struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Version     int      `json:"version,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// ValidName reports whether s is a usable skill name (kebab-case, 2-63 chars).
func ValidName(s string) bool { return nameRe.MatchString(s) && !strings.Contains(s, "--") }

// LF normalises line endings (a Windows checkout may turn the pack into CRLF).
func LF(b []byte) []byte {
	return []byte(strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r", "\n"))
}

// Hash is the sha256 of a SKILL.md with normalised line endings.
func Hash(b []byte) string {
	s := sha256.Sum256(LF(b))
	return hex.EncodeToString(s[:])
}

// ParseFront reads the front matter of a SKILL.md. It understands the small YAML
// subset skills use: `key: value`, quoted values, inline lists `[a, b]` and
// `- item` lists. ok is false when there is no front matter.
func ParseFront(txt string) (m Meta, body string, ok bool) {
	txt = strings.TrimPrefix(string(LF([]byte(txt))), "\ufeff")
	if !strings.HasPrefix(txt, "---\n") {
		return m, txt, false
	}
	end := strings.Index(txt[4:], "\n---")
	if end < 0 {
		return m, txt, false
	}
	head := txt[4 : 4+end]
	body = strings.TrimLeft(txt[4+end+4:], "\n")
	var listKey string
	for _, l := range strings.Split(head, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "- ") && listKey == "tags" {
			m.Tags = append(m.Tags, unq(strings.TrimSpace(t[2:])))
			continue
		}
		k, v, found := strings.Cut(t, ":")
		if !found {
			continue
		}
		k, v = strings.TrimSpace(strings.ToLower(k)), strings.TrimSpace(v)
		listKey = k
		switch k {
		case "name":
			m.Name = unq(v)
		case "description":
			m.Description = unq(v)
		case "version":
			m.Version, _ = strconv.Atoi(unq(v))
		case "tags":
			if strings.HasPrefix(v, "[") {
				for _, x := range strings.Split(strings.Trim(v, "[]"), ",") {
					if x = unq(strings.TrimSpace(x)); x != "" {
						m.Tags = append(m.Tags, x)
					}
				}
			} else if v != "" {
				for _, x := range strings.Split(v, ",") {
					if x = unq(strings.TrimSpace(x)); x != "" {
						m.Tags = append(m.Tags, x)
					}
				}
			}
		}
	}
	return m, body, true
}

func unq(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// Render writes a SKILL.md from its front matter and body.
func Render(m Meta, body string) string {
	var b strings.Builder
	b.WriteString("---\nname: " + m.Name + "\n")
	b.WriteString("description: " + oneLine(m.Description) + "\n")
	if len(m.Tags) > 0 {
		b.WriteString("tags: [" + strings.Join(m.Tags, ", ") + "]\n")
	}
	if m.Version > 0 {
		b.WriteString("version: " + strconv.Itoa(m.Version) + "\n")
	}
	b.WriteString("---\n\n" + strings.TrimSpace(body) + "\n")
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Load reads a pack from fsys (the embedded skills/ folder): one skill per
// directory holding a SKILL.md. Every file under a skill's folder is included.
func Load(fsys fs.FS) ([]Skill, error) {
	ents, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Join(e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		m, _, ok := ParseFront(string(raw))
		if !ok || m.Name == "" {
			return nil, fmt.Errorf("skills/%s/SKILL.md: missing front matter or name", e.Name())
		}
		if m.Name != e.Name() {
			return nil, fmt.Errorf("skills/%s: name %q must match its folder", e.Name(), m.Name)
		}
		if m.Version <= 0 {
			m.Version = 1
		}
		s := Skill{Name: m.Name, Description: m.Description, Tags: m.Tags, Version: m.Version, SHA256: Hash(raw), Files: map[string][]byte{}}
		err = fs.WalkDir(fsys, e.Name(), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(p, e.Name()+"/")
			if rel == "SKILL.md" || rel == "LICENSE" || strings.HasSuffix(rel, ".md") || strings.HasSuffix(rel, ".css") || strings.HasSuffix(rel, ".html") || strings.HasSuffix(rel, ".txt") {
				b = LF(b)
			}
			s.Files[rel] = b
			return nil
		})
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ---------------- manifest ----------------

// Entry is what Blaze last wrote for one bundled skill.
type Entry struct {
	Version int    `json:"version"`
	SHA256  string `json:"sha256"`
}

// Manifest is the pack state shared by every Blaze app using the folder.
type Manifest struct {
	Format   int              `json:"format"`
	Bundled  map[string]Entry `json:"bundled"`
	Deleted  []string         `json:"deleted,omitempty"`  // bundled skills the user removed: never re-seeded
	Disabled []string         `json:"disabled,omitempty"` // any skill (bundled or the user's) left out of prompts
}

var mu sync.Mutex // one seeder / editor at a time in this process

// ReadManifest loads dir/.bundled.json (an empty manifest when missing or broken).
func ReadManifest(dir string) Manifest {
	m := Manifest{Format: 1, Bundled: map[string]Entry{}}
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err == nil {
		_ = json.Unmarshal(b, &m)
	}
	if m.Bundled == nil {
		m.Bundled = map[string]Entry{}
	}
	m.Format = 1
	return m
}

func writeManifest(dir string, m Manifest) error {
	sort.Strings(m.Deleted)
	sort.Strings(m.Disabled)
	b, _ := json.MarshalIndent(m, "", "  ")
	return writeAtomic(filepath.Join(dir, ManifestName), append(b, '\n'))
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func without(list []string, s string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func writeAtomic(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(p) // Windows: rename over an existing file can fail
		return os.Rename(tmp, p)
	}
	return nil
}

func writeSkill(dir string, s Skill) error {
	for rel, b := range s.Files {
		if strings.Contains(rel, "..") {
			continue
		}
		if err := writeAtomic(filepath.Join(dir, s.Name, filepath.FromSlash(rel)), b); err != nil {
			return err
		}
	}
	return nil
}

// ---------------- seeding ----------------

// Report says what a Seed did.
type Report struct {
	Added    []string `json:"added"`
	Upgraded []string `json:"upgraded"`
	Edited   []string `json:"edited"`  // kept: the user changed them
	Deleted  []string `json:"deleted"` // skipped: the user removed them
	Shadowed []string `json:"shadowed"`
}

// Changed reports whether anything was written.
func (r Report) Changed() bool { return len(r.Added)+len(r.Upgraded) > 0 }

// Seed brings dir up to date with the pack. Safe to call on every start.
func Seed(dir string, pack []Skill) (Report, error) {
	mu.Lock()
	defer mu.Unlock()
	var r Report
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return r, err
	}
	m := ReadManifest(dir)
	dirty := false
	for _, s := range pack {
		if has(m.Deleted, s.Name) {
			r.Deleted = append(r.Deleted, s.Name)
			continue
		}
		rec, known := m.Bundled[s.Name]
		cur, err := os.ReadFile(filepath.Join(dir, s.Name, "SKILL.md"))
		switch {
		case err != nil && known:
			// We wrote it before and it is gone: the user deleted the folder.
			m.Deleted = append(m.Deleted, s.Name)
			delete(m.Bundled, s.Name)
			r.Deleted = append(r.Deleted, s.Name)
			dirty = true
		case err != nil:
			if err := writeSkill(dir, s); err != nil {
				return r, err
			}
			m.Bundled[s.Name] = Entry{s.Version, s.SHA256}
			r.Added = append(r.Added, s.Name)
			dirty = true
		default:
			h := Hash(cur)
			switch {
			case h == s.SHA256:
				// Already the current bundled text (maybe written by another Blaze app).
				if rec != (Entry{s.Version, s.SHA256}) {
					m.Bundled[s.Name] = Entry{s.Version, s.SHA256}
					dirty = true
				}
			case known && h == rec.SHA256:
				// Untouched since we wrote it: take the new bundled version.
				if s.Version >= rec.Version {
					if err := writeSkill(dir, s); err != nil {
						return r, err
					}
					m.Bundled[s.Name] = Entry{s.Version, s.SHA256}
					r.Upgraded = append(r.Upgraded, s.Name)
					dirty = true
				}
			case known:
				r.Edited = append(r.Edited, s.Name)
			default:
				// The user's own skill with a bundled name: theirs wins.
				r.Shadowed = append(r.Shadowed, s.Name)
			}
		}
	}
	if dirty {
		return r, writeManifest(dir, m)
	}
	return r, nil
}

// ---------------- status & user actions ----------------

// State of one skill in the folder, for Settings › Skills.
const (
	StateBundled = "bundled" // Blaze's, as shipped
	StateEdited  = "edited"  // Blaze's, changed by the user (kept on upgrades)
	StateOwn     = "own"     // the user's (or one Blaze wrote for them)
	StateRemoved = "removed" // Blaze's, deleted by the user (restorable)
)

// View is one row of the skills list.
type View struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Version     int      `json:"version,omitempty"`
	State       string   `json:"state"`
	Enabled     bool     `json:"enabled"`
	Update      bool     `json:"update,omitempty"` // edited, and the pack has a newer version than the one edited
	Path        string   `json:"path,omitempty"`
}

// Status lists every skill in dir plus the bundled ones the user removed.
func Status(dir string, pack []Skill) []View {
	m := ReadManifest(dir)
	byName := map[string]Skill{}
	for _, s := range pack {
		byName[s.Name] = s
	}
	var out []View
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name(), "SKILL.md")
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		meta, _, _ := ParseFront(string(raw))
		v := View{Name: e.Name(), Description: meta.Description, Tags: meta.Tags, Version: meta.Version, State: StateOwn, Enabled: !has(m.Disabled, e.Name()), Path: p}
		if meta.Name != "" {
			v.Name = meta.Name
		}
		if rec, ok := m.Bundled[e.Name()]; ok {
			if Hash(raw) == rec.SHA256 {
				v.State = StateBundled
			} else {
				v.State = StateEdited
				if s, ok := byName[e.Name()]; ok && s.Version > rec.Version {
					v.Update = true
				}
			}
		}
		out = append(out, v)
	}
	for _, n := range m.Deleted {
		if s, ok := byName[n]; ok {
			out = append(out, View{Name: n, Description: s.Description, Tags: s.Tags, Version: s.Version, State: StateRemoved})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DisabledSet is the names left out of prompts.
func DisabledSet(dir string) map[string]bool {
	out := map[string]bool{}
	for _, n := range ReadManifest(dir).Disabled {
		out[n] = true
	}
	return out
}

// SetEnabled turns a skill on or off (it stays on disk either way).
func SetEnabled(dir, name string, on bool) error {
	mu.Lock()
	defer mu.Unlock()
	m := ReadManifest(dir)
	m.Disabled = without(m.Disabled, name)
	if !on {
		m.Disabled = append(m.Disabled, name)
	}
	return writeManifest(dir, m)
}

// Delete removes a skill folder. A bundled skill is remembered as deleted so it
// is not seeded again.
func Delete(dir, name string) error {
	if !ValidName(name) {
		return errors.New("not a skill name: " + name)
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
		return err
	}
	m := ReadManifest(dir)
	if _, ok := m.Bundled[name]; ok {
		delete(m.Bundled, name)
		if !has(m.Deleted, name) {
			m.Deleted = append(m.Deleted, name)
		}
	}
	m.Disabled = without(m.Disabled, name)
	return writeManifest(dir, m)
}

// Restore writes the bundled version of a skill again: undoes a delete, or
// resets an edited skill to the shipped text.
func Restore(dir, name string, pack []Skill) error {
	mu.Lock()
	defer mu.Unlock()
	for _, s := range pack {
		if s.Name != name {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name, "SKILL.md"))
		if err := writeSkill(dir, s); err != nil {
			return err
		}
		m := ReadManifest(dir)
		m.Deleted = without(m.Deleted, name)
		m.Bundled[name] = Entry{s.Version, s.SHA256}
		return writeManifest(dir, m)
	}
	return errors.New("Blaze has no bundled skill called " + name + ".")
}

// ---------------- index ----------------

// IndexEntry is one skill in skills/index.json (a generated table of contents
// for ports that can't list directories easily, e.g. Android assets).
type IndexEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Version     int      `json:"version"`
	SHA256      string   `json:"sha256"`
	Files       []string `json:"files"`
}

// Index renders skills/index.json for a pack.
func Index(pack []Skill) []byte {
	type idx struct {
		Format int          `json:"format"`
		Skills []IndexEntry `json:"skills"`
	}
	out := idx{Format: 1}
	for _, s := range pack {
		var files []string
		for f := range s.Files {
			files = append(files, f)
		}
		sort.Strings(files)
		tags := s.Tags
		if tags == nil {
			tags = []string{}
		}
		out.Skills = append(out.Skills, IndexEntry{s.Name, s.Description, tags, s.Version, s.SHA256, files})
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return append(b, '\n')
}
