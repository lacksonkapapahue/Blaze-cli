package tools

// Memory (~/.blaze/memory.md, one "- fact (date)" line each, shared with Blaze
// Desktop, which imports new lines into its database) and skills
// (~/.blaze/skills/<name>/SKILL.md, shared with Desktop and the Android app).
//
// v0.5: memories carry ids in the prompt (#n = the n-th fact) so remember can
// replace a stale one and forget can target it; near-duplicates update the
// existing line; the bundled skill pack is seeded on start (skills.go) and the
// prompt lists only the skills that match the conversation.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/skillpack"
)

func memFile() string { return filepath.Join(config.Dir(), "memory.md") }

var dateRe = regexp.MustCompile(`\s*\((\d{4}-\d{2}-\d{2})\)\s*$`)

// Fact is one saved memory line.
type Fact struct {
	ID   int // 1-based position among the facts
	Text string
	Date string
}

// Facts reads memory.md (lines starting with "- "; other lines are kept as-is on rewrite).
func Facts() []Fact {
	b, _ := os.ReadFile(memFile())
	var out []Fact
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "- ") && !strings.HasPrefix(t, "* ") {
			continue
		}
		t = strings.TrimSpace(t[2:])
		d := ""
		if m := dateRe.FindStringSubmatch(t); m != nil {
			d = m[1]
			t = strings.TrimSpace(dateRe.ReplaceAllString(t, ""))
		}
		if t != "" {
			out = append(out, Fact{ID: len(out) + 1, Text: t, Date: d})
		}
	}
	return out
}

func writeFacts(fs []Fact) error {
	_ = os.MkdirAll(config.Dir(), 0o700)
	var b strings.Builder
	for _, f := range fs {
		if f.Date != "" {
			fmt.Fprintf(&b, "- %s (%s)\n", f.Text, f.Date)
		} else {
			fmt.Fprintf(&b, "- %s\n", f.Text)
		}
	}
	return os.WriteFile(memFile(), []byte(b.String()), 0o600)
}

// Memory is the memory block of the system prompt, with ids.
func Memory() string {
	var b strings.Builder
	for _, f := range Facts() {
		fmt.Fprintf(&b, "- %s [#%d]\n", f.Text, f.ID)
	}
	return strings.TrimSpace(b.String())
}

func norm(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r)) })
}

// similar is the Jaccard overlap of two texts' word sets.
func similar(a, b string) float64 {
	wa, wb := map[string]bool{}, map[string]bool{}
	for _, w := range norm(a) {
		wa[w] = true
	}
	for _, w := range norm(b) {
		wb[w] = true
	}
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	in := 0
	for w := range wa {
		if wb[w] {
			in++
		}
	}
	return float64(in) / float64(len(wa)+len(wb)-in)
}

// Near is the overlap at or above which two facts count as the same one.
const Near = 0.85

// AddFact saves a fact: identical text is kept once, a near-duplicate is
// updated in place. status is "added", "exists" or "updated".
func AddFact(text string) (id int, status string, err error) {
	text = strings.Join(strings.Fields(text), " ")
	fs := Facts()
	best, bs := -1, 0.0
	for i, f := range fs {
		if strings.EqualFold(strings.Join(norm(f.Text), " "), strings.Join(norm(text), " ")) {
			return f.ID, "exists", nil
		}
		if s := similar(f.Text, text); s > bs {
			best, bs = i, s
		}
	}
	today := time.Now().Format("2006-01-02")
	if best >= 0 && bs >= Near {
		fs[best].Text, fs[best].Date = text, today
		return fs[best].ID, "updated", writeFacts(fs)
	}
	fs = append(fs, Fact{ID: len(fs) + 1, Text: text, Date: today})
	return len(fs), "added", writeFacts(fs)
}

// ReplaceFact rewrites fact #id.
func ReplaceFact(id int, text string) error {
	fs := Facts()
	if id < 1 || id > len(fs) {
		return fmt.Errorf("no memory #%d", id)
	}
	fs[id-1].Text, fs[id-1].Date = strings.Join(strings.Fields(text), " "), time.Now().Format("2006-01-02")
	return writeFacts(fs)
}

// Related lists saved facts that look like the same subject (overlap ≥ lo, < Near).
func Related(text string, lo float64, limit, skip int) []Fact {
	type sc struct {
		f Fact
		s float64
	}
	var all []sc
	for _, f := range Facts() {
		if f.ID == skip {
			continue
		}
		if s := similar(f.Text, text); s >= lo && s < Near {
			all = append(all, sc{f, s})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].s > all[j].s })
	var out []Fact
	for i := 0; i < len(all) && i < limit; i++ {
		out = append(out, all[i].f)
	}
	return out
}

// SearchFacts returns the facts sharing the most words with q.
func SearchFacts(q string, limit int) []Fact {
	type sc struct {
		f Fact
		n int
	}
	words := map[string]bool{}
	for _, w := range norm(q) {
		if len(w) > 2 {
			words[w] = true
		}
	}
	var all []sc
	for _, f := range Facts() {
		n := 0
		for _, w := range norm(f.Text) {
			if words[w] {
				n++
			}
		}
		if n > 0 {
			all = append(all, sc{f, n})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].n > all[j].n })
	var out []Fact
	for i := 0; i < len(all) && i < limit; i++ {
		out = append(out, all[i].f)
	}
	return out
}

func memID(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	return n, err == nil && n > 0
}

// ---------------- skills ----------------

func skillsDir() string { return dataDir("skills") }

type Skill struct {
	Name, Desc, Path string
	Tags             []string
	Off              bool // turned off (/skills off): kept on disk, left out of the prompt
}

// Skills lists ~/.blaze/skills/*/SKILL.md (same format as Desktop and the Android app).
func Skills() []Skill {
	var out []Skill
	off := skillpack.DisabledSet(skillsDir())
	ents, _ := os.ReadDir(skillsDir())
	for _, en := range ents {
		if !en.IsDir() || strings.HasPrefix(en.Name(), ".") {
			continue
		}
		f := filepath.Join(skillsDir(), en.Name(), "SKILL.md")
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		m, _, _ := skillpack.ParseFront(string(b))
		s := Skill{Name: en.Name(), Desc: m.Description, Path: f, Tags: m.Tags, Off: off[en.Name()]}
		if m.Name != "" {
			s.Name = m.Name
		}
		out = append(out, s)
	}
	return out
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
	for i := 0; i < limit; i++ {
		pick = append(pick, all[i].s)
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
			fs := Facts()
			var keep, gone []Fact
			id, byID := memID(a.s("text"))
			q := strings.ToLower(strings.TrimSpace(a.s("text")))
			for _, f := range fs {
				if (byID && f.ID == id) || (!byID && q != "" && strings.Contains(strings.ToLower(f.Text), q)) {
					gone = append(gone, f)
					continue
				}
				keep = append(keep, f)
			}
			if len(gone) == 0 {
				return "No saved memory matches " + a.s("text") + "."
			}
			if err := writeFacts(keep); err != nil {
				return "Error: " + err.Error()
			}
			for _, f := range gone {
				Learned(LearnEvent{Kind: "memory", Action: "forgot", Text: f.Text, Ref: fmt.Sprintf("#%d", f.ID)})
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
