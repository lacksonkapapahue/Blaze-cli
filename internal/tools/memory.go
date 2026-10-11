package tools

// Memory and skills live in ~/.blaze/blaze.db (internal/store), the same SQLite
// file Blaze Desktop uses, so a fact the CLI saves shows up in Desktop at once
// and the other way round. Skills stay folders (~/.blaze/skills/<name>/SKILL.md,
// shared with Desktop and the Android app); the database keeps a searchable
// index of them.
//
// v0.6: memory.md is imported once on first open (kept as memory.md.migrated)
// and never written again. Memory ids (#12) are database ids, the same ones
// Desktop shows. A turn gets the pinned memories plus the ones that match the
// conversation; recall searches the rest.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/skillpack"
	"github.com/lacksonkapapahue/blaze-cli/internal/store"
)

// Prompt budget for memories: up to MemMatches matches, within MemBudget tokens
// (the same numbers Desktop uses).
const (
	MemMatches = 8
	MemBudget  = 600
)

// DB is the shared database, or nil (with the reason from DBError) when it
// can't be opened. Blaze still runs without memory then.
func DB() *store.DB {
	d, err := store.Default()
	if err != nil {
		return nil
	}
	return d
}

// DBError says why the database can't be used ("" when it can).
func DBError() string {
	if _, err := store.Default(); err != nil {
		return err.Error()
	}
	return ""
}

// DBPath is where blaze.db lives.
func DBPath() string { return filepath.Join(config.Dir(), "blaze.db") }

func memErr() string {
	err := errNoDB()
	if errors.Is(err, store.ErrNewerSchema) {
		return "Error: " + err.Error() + ". Tell the user."
	}
	return "Error: Blaze's memory database can't be opened (" + DBError() + ")."
}

func errNoDB() error {
	if _, err := store.Default(); err != nil {
		return err
	}
	return nil
}

// Fact is one saved memory.
type Fact struct {
	ID   int64 // database id, shared with Desktop
	Text string
	Date string // last changed, YYYY-MM-DD
}

func toFacts(ms []store.Memory) []Fact {
	out := make([]Fact, 0, len(ms))
	for _, m := range ms {
		out = append(out, Fact{ID: m.ID, Text: m.Text, Date: m.Updated.Format("2006-01-02")})
	}
	return out
}

// Facts lists every saved memory, oldest first.
func Facts() []Fact {
	d := DB()
	if d == nil {
		return nil
	}
	return toFacts(d.Memories())
}

// Memory is the memory block of the system prompt for a conversation about
// query: the pinned memories plus the best matches, with their ids.
func Memory(query string) string {
	d := DB()
	if d == nil {
		return ""
	}
	total := d.CountMemories()
	if total == 0 {
		return ""
	}
	ms := d.ForPrompt(query, MemMatches, MemBudget)
	var b strings.Builder
	if total > len(ms) {
		fmt.Fprintf(&b, "(%d of %d saved memories, picked for this conversation; recall(query) searches the rest)\n", len(ms), total)
	}
	for _, m := range ms {
		pin := ""
		if m.Pinned {
			pin = " pinned"
		}
		fmt.Fprintf(&b, "- %s [#%d%s]\n", m.Text, m.ID, pin)
	}
	return strings.TrimSpace(b.String())
}

func norm(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r)) })
}

// Near is the overlap at or above which two facts count as the same one.
const Near = store.Near

// AddFact saves a fact: identical text is kept once, a near-duplicate is
// updated in place. status is "added", "exists" or "updated".
func AddFact(text string) (id int64, status string, err error) {
	d := DB()
	if d == nil {
		return 0, "", errNoDB()
	}
	return d.Add(text, "", "", time.Time{})
}

// ReplaceFact rewrites fact #id.
func ReplaceFact(id int64, text string) error {
	d := DB()
	if d == nil {
		return errNoDB()
	}
	if _, ok := d.Memory(id); !ok {
		return fmt.Errorf("no memory #%d", id)
	}
	return d.Edit(id, text)
}

// Related lists saved facts that look like the same subject (overlap ≥ lo, < Near).
func Related(text string, lo float64, limit int, skip int64) []Fact {
	d := DB()
	if d == nil {
		return nil
	}
	return toFacts(d.Related(text, lo, limit, skip))
}

// SearchFacts returns the facts that best match q (full-text, BM25).
func SearchFacts(q string, limit int) []Fact {
	d := DB()
	if d == nil {
		return nil
	}
	return toFacts(d.Search(q, limit))
}

func memID(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(s), "#"), 10, 64)
	return n, err == nil && n > 0
}

// ---------------- skills ----------------

func skillsDir() string { return dataDir("skills") }

type Skill struct {
	Name, Desc, Path string
	Tags             []string
	Off              bool // turned off (/skills off): kept on disk, left out of the prompt
}

type skillFile struct {
	path, dir string
	mtime     int64
	body      string
}

func skillFiles() []skillFile {
	var out []skillFile
	ents, _ := os.ReadDir(skillsDir())
	for _, en := range ents {
		if !en.IsDir() || strings.HasPrefix(en.Name(), ".") {
			continue
		}
		f := filepath.Join(skillsDir(), en.Name(), "SKILL.md")
		st, err := os.Stat(f)
		if err != nil || st.IsDir() {
			continue
		}
		out = append(out, skillFile{path: f, dir: en.Name(), mtime: st.ModTime().UnixNano()})
	}
	return out
}

// Skills lists ~/.blaze/skills/*/SKILL.md (same format as Desktop and the
// Android app) and keeps the skills index in blaze.db in step with the folder.
func Skills() []Skill {
	var out []Skill
	off := skillpack.DisabledSet(skillsDir())
	files := skillFiles()
	for i, f := range files {
		b, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		files[i].body = string(b)
		m, _, _ := skillpack.ParseFront(string(b))
		s := Skill{Name: f.dir, Desc: m.Description, Path: f.path, Tags: m.Tags, Off: off[f.dir]}
		if m.Name != "" {
			s.Name = m.Name
		}
		out = append(out, s)
	}
	if d := DB(); d != nil {
		syncSkillIndex(d, files, out)
	}
	return out
}

// SyncSkillIndex re-indexes the skills folder into blaze.db when a SKILL.md was
// added, changed or removed (the same check Desktop makes, so the two apps agree
// and neither rebuilds the index for nothing).
func SyncSkillIndex() { Skills() }

func syncSkillIndex(d *store.DB, files []skillFile, sk []Skill) {
	have := d.SkillMtimes()
	changed := len(files) != len(have)
	for _, f := range files {
		if have[f.path] != f.mtime {
			changed = true
			break
		}
	}
	if !changed {
		return
	}
	rows := make([]store.Skill, 0, len(sk))
	byPath := map[string]skillFile{}
	for _, f := range files {
		byPath[f.path] = f
	}
	for _, s := range sk {
		f := byPath[s.Path]
		rows = append(rows, store.Skill{Name: s.Name, Desc: s.Desc, Path: s.Path, Body: f.body, Mtime: f.mtime})
	}
	_ = d.ReplaceSkills(rows)
}

// SkillLimit is how many skills the prompt lists when there are more.
const SkillLimit = 10

// PickSkills ranks the enabled skills against query (name and tags weigh most)
// and returns at most limit; all of them when there are only a few.
func PickSkills(query string, limit int) (pick []Skill, total int) {
	var on []Skill
	for _, s := range Skills() {
		if !s.Off {
			on = append(on, s)
		}
	}
	if len(on) <= limit {
		return on, len(on)
	}
	// Full-text search over the shared index first (name and description weigh
	// most), topped up by the word ranking below.
	seen := map[string]bool{}
	if d := DB(); d != nil {
		byPath := map[string]Skill{}
		for _, s := range on {
			byPath[s.Path] = s
		}
		for _, r := range d.SearchSkills(query, limit+4) {
			if s, ok := byPath[r.Path]; ok && !seen[s.Path] && len(pick) < limit {
				seen[s.Path] = true
				pick = append(pick, s)
			}
		}
	}
	words := map[string]bool{}
	for _, w := range norm(query) {
		if len(w) > 2 {
			words[w] = true
			words[strings.TrimSuffix(w, "s")] = true
		}
	}
	type sc struct {
		s Skill
		n int
	}
	var all []sc
	for _, s := range on {
		n := 0
		for _, w := range norm(s.Name + " " + strings.Join(s.Tags, " ")) {
			if words[w] || words[strings.TrimSuffix(w, "s")] {
				n += 3
			}
		}
		for _, w := range norm(s.Desc) {
			if words[w] {
				n++
			}
		}
		all = append(all, sc{s, n})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].n > all[j].n })
	for i := 0; i < len(all) && len(pick) < limit; i++ {
		if !seen[all[i].s.Path] {
			seen[all[i].s.Path] = true
			pick = append(pick, all[i].s)
		}
	}
	return pick, len(on)
}

// SkillsFor is the SKILLS block of the system prompt for a conversation about query.
func SkillsFor(query string) string {
	pick, total := PickSkills(query, SkillLimit)
	if len(pick) == 0 {
		return ""
	}
	var b strings.Builder
	if len(pick) < total {
		fmt.Fprintf(&b, "\nSKILLS (%d of %d, matched to this conversation; load_skill(name) reads one before that kind of task, or load_skill with a guess to find others)\n", len(pick), total)
	} else {
		b.WriteString("\nSKILLS (load_skill(name) reads one before that kind of task)\n")
	}
	for _, s := range pick {
		b.WriteString("- " + s.Name + ": " + s.Desc + "\n")
	}
	return b.String()
}

func loadSkill(name string) string {
	name = strings.TrimSpace(name)
	all := Skills()
	for _, s := range all {
		if strings.EqualFold(s.Name, name) || strings.EqualFold(filepath.Base(filepath.Dir(s.Path)), name) {
			b, _ := os.ReadFile(s.Path)
			return "Skill folder: " + filepath.Dir(s.Path) + " (scripts and assets inside can be used with run_shell / file_read)\n\n" + string(b)
		}
	}
	msg := "No skill called " + name + "."
	if pick, _ := PickSkills(name, 5); len(pick) > 0 && len(all) > 5 {
		var ns []string
		for _, s := range pick {
			ns = append(ns, s.Name)
		}
		msg += " Closest: " + strings.Join(ns, ", ") + "."
	}
	return msg
}

func init() {
	register("remember", "Save a lasting fact about the user or their setup (preferences, project paths, how they like things), with its context. Not for one-off task details or secrets. Near-duplicates update the existing memory; pass replaces=#id when the fact supersedes a saved one.",
		[]prop{p("fact", "string", "One short sentence"), p("replaces", "string", "Optional: the id (#3) of a saved memory this one replaces because it changed")}, []string{"fact"},
		func(e *Env, a args) string {
			fact := strings.Join(strings.Fields(a.s("fact")), " ")
			if fact == "" {
				return "Error in remember: write the fact."
			}
			if DB() == nil {
				return memErr()
			}
			if id, ok := memID(a.s("replaces")); ok {
				if err := ReplaceFact(id, fact); err == nil {
					Learned(LearnEvent{Kind: "memory", Action: "replaced", Text: fact, Ref: fmt.Sprintf("#%d", id)})
					return fmt.Sprintf("Replaced memory #%d with the new fact.", id)
				}
			}
			id, st, err := AddFact(fact)
			switch {
			case err != nil:
				return "Error: " + err.Error()
			case st == "exists":
				return fmt.Sprintf("Already remembered (#%d).", id)
			case st == "updated":
				Learned(LearnEvent{Kind: "memory", Action: "updated", Text: fact, Ref: fmt.Sprintf("#%d", id)})
				return fmt.Sprintf("Updated memory #%d with the new wording.", id)
			}
			Learned(LearnEvent{Kind: "memory", Action: "saved", Text: fact, Ref: fmt.Sprintf("#%d", id)})
			out := fmt.Sprintf("Remembered (#%d).", id)
			if rel := Related(fact, 0.34, 3, id); len(rel) > 0 {
				out += " Similar saved memories:"
				for _, f := range rel {
					out += fmt.Sprintf(" #%d %q;", f.ID, f.Text)
				}
				out += " If one is now wrong, forget it (or next time use remember with replaces=#id)."
			}
			return out
		})

	register("recall", "Search everything you've saved about the user. Use before asking the user something you may already know.",
		[]prop{p("query", "string", "Words to look for")}, []string{"query"},
		func(e *Env, a args) string {
			if DB() == nil {
				return memErr()
			}
			fs := SearchFacts(a.s("query"), 10)
			if len(fs) == 0 {
				return "No saved memory matches " + a.s("query") + "."
			}
			var b strings.Builder
			for _, f := range fs {
				fmt.Fprintf(&b, "#%d %s (%s)\n", f.ID, f.Text, f.Date)
			}
			return b.String()
		})

	register("forget", "Remove saved memories: by id (#3 from your prompt) or every memory containing some text.",
		[]prop{p("text", "string", "A memory id like #3, or text to match")}, []string{"text"},
		func(e *Env, a args) string {
			d := DB()
			if d == nil {
				return memErr()
			}
			var gone []store.Memory
			if id, ok := memID(a.s("text")); ok {
				if m, found := d.Memory(id); found && d.Delete(id) == nil {
					gone = append(gone, m)
				}
			} else {
				gone = d.ForgetMatching(a.s("text"))
			}
			if len(gone) == 0 {
				return "No saved memory matches " + a.s("text") + "."
			}
			for _, m := range gone {
				Learned(LearnEvent{Kind: "memory", Action: "forgot", Text: m.Text, Ref: fmt.Sprintf("#%d", m.ID)})
			}
			return fmt.Sprintf("Forgot %d fact(s).", len(gone))
		})

	register("load_skill", "Load a skill's full instructions (listed in your prompt) before doing that kind of task.",
		[]prop{p("name", "string", "Skill name")}, []string{"name"},
		func(e *Env, a args) string { return loadSkill(a.s("name")) })
	register("use_skill", "Same as load_skill.", []prop{p("name", "string", "Skill name")}, []string{"name"},
		func(e *Env, a args) string { return loadSkill(a.s("name")) })

	register("write_skill", "Create or rewrite a reusable skill (~/.blaze/skills/<name>/SKILL.md) so the next task of that kind goes faster. Use when a multi-step workflow repeats, the user teaches a method, or you found a better way for an existing skill (load_skill it first, then write the whole improved body under the same name).",
		[]prop{p("name", "string", "kebab-case name, e.g. deploy-homeglow"), p("description", "string", "When to use it, one line"), p("body", "string", "Markdown: numbered steps, exact commands, checks, pitfalls"), p("tags", "string", "Optional comma-separated tags")},
		[]string{"name", "description", "body"},
		func(e *Env, a args) string {
			name := strings.ToLower(strings.TrimSpace(a.s("name")))
			created, err := WriteSkill(name, a.s("description"), splitTags(a.s("tags")), a.s("body"))
			if err != nil {
				return "Error: " + err.Error()
			}
			if created {
				Learned(LearnEvent{Kind: "skill", Action: "created", Text: name, Ref: name})
				return "Saved skill " + name + ". Tell the user in one short line."
			}
			Learned(LearnEvent{Kind: "skill", Action: "edited", Text: name, Ref: name})
			return "Updated skill " + name + ". Tell the user in one short line what changed."
		})
}
