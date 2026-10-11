// Package learn decides when Blaze should save something on its own: the rules
// added to the system prompt, the cheap end-of-turn check that decides whether a
// reflection call is worth making, the filter every auto-saved fact must pass,
// and the parser for the reflection's answer. It has no I/O so it is easy to test
// and to port (the CLI keeps a copy; the Android app can mirror the rules).
package learn

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Rules is the MEMORY & SKILLS block of the system prompt. auto is the
// "Learn automatically" setting.
func Rules(auto bool) string {
	if !auto {
		return `
MEMORY & SKILLS
- Learning automatically is OFF in Settings. Save a memory (remember) or a skill (write_skill) only when the user asks you to. forget still works when asked.
`
	}
	return `
MEMORY & SKILLS (learn as you go; nobody has to ask)
- Save silently with remember the moment a durable fact appears: preferences, how they like answers, their tools, projects, paths, accounts, people and recurring routines. One short sentence each, with its context ("Uses pnpm in the HomeGlow repo", not "Uses pnpm"). Keep their certainty: "usually" stays usually; mark guesses as inferred.
- Don't save one-off task details, things only true today, secrets (keys, passwords, card or ID numbers) or what is already in your prompt.
- When a fact changes, replace the old memory: remember(fact, replaces="#id") instead of adding a second one. When the user asks you to forget something, forget every matching memory and confirm.
- Create a skill with write_skill when a multi-step workflow repeats or the user teaches you a method ("here's how I deploy"). Name it kebab-case; description = when to use it; body = short numbered steps, commands, checks and pitfalls. Before a matching task, load_skill it. When you find a better way, rewrite the skill (load_skill, then write_skill with the same name).
- Memory saves need no announcement. When you create or change a skill, tell the user in one short line at the end (e.g. "Saved this as the deploy-homeglow skill.").
`
}

// Turn is what the end-of-turn check looks at.
type Turn struct {
	User        string   // the user's message(s) this turn
	Reply       string   // Blaze's final answer
	Tools       []string // tool names called this turn, in order
	SavedMemory bool     // remember/forget was called this turn
	SavedSkill  bool     // write_skill was called this turn
}

// Decision says whether a reflection call is worth it, and what it may save.
type Decision struct {
	Reflect bool
	Memory  bool // look for durable facts
	Skill   bool // a reusable workflow may be worth a skill
	Why     string
}

var smallTalk = map[string]bool{}

func init() {
	for _, w := range strings.Fields("hi hey hello yo thanks thank you thx ty ok okay cool nice great yes no yep nope sure continue go on ahead do it done lol k please pls and good perfect awesome alright right") {
		smallTalk[w] = true
	}
}

// Trivial reports whether a message is small talk or a short go-ahead.
func Trivial(s string) bool {
	w := words(s)
	if len(w) == 0 {
		return true
	}
	if len(w) > 4 {
		return false
	}
	for _, x := range w {
		if !smallTalk[x] {
			return len(w) <= 2 && !strings.Contains(strings.ToLower(s), "remember")
		}
	}
	return true
}

var factCues = []string{
	"i prefer", "i'd prefer", "i like", "i love", "i don't like", "i dont like", "i hate", "i want you to", "i'd rather",
	"always ", "never ", "from now on", "remember", "don't forget", "keep in mind", "for future", "next time",
	"my name", "call me", "i'm called", "i am called", "i use ", "i'm using", "i am using", "we use ", "i work", "i live", "i'm based", "i am based",
	"my setup", "my stack", "my project", "my repo", "my site", "my store", "my pc", "my laptop", "i usually", "i normally", "stop doing", "please don't", "instead of", "in future", "in the future",
	"my timezone", "my email", "my phone", "my birthday", "my wife", "my husband", "my partner", "my boss", "my team",
}

var teachCues = []string{
	"here's how", "here is how", "this is how", "the way i", "the way we", "how i do", "how we do", "steps:", "step 1", "first,",
	"follow these", "workflow", "the process is", "my process", "every time i", "whenever i", "each time", "routine",
	"save this as a skill", "make a skill", "make this a skill", "as a skill", "next time do", "do it like this",
}

func containsAny(s string, cues []string) bool {
	for _, c := range cues {
		if strings.Contains(s, c) {
			return true
		}
	}
	return false
}

// HasFactCue reports whether a message likely carries a durable fact or preference.
func HasFactCue(s string) bool { return containsAny(" "+strings.ToLower(s)+" ", factCues) }

// HasTeachCue reports whether the user is teaching a method.
func HasTeachCue(s string) bool { return containsAny(strings.ToLower(s), teachCues) }

// lookTools are memory and lookup tools: they do not make a turn a "workflow".
var lookTools = map[string]bool{"remember": true, "recall": true, "forget": true, "load_skill": true, "use_skill": true, "find_tools": true, "write_skill": true, "notify": true}

// Steps counts the real work steps of a turn (memory and lookup tools excluded).
func Steps(tools []string) (steps, distinct int) {
	seen := map[string]bool{}
	for _, t := range tools {
		if lookTools[t] {
			continue
		}
		steps++
		if !seen[t] {
			seen[t] = true
			distinct++
		}
	}
	return
}

// Decide is the cheap check run after every turn. repeat says whether this
// turn's workflow matches an earlier one (see Signature / Repeats).
func Decide(t Turn, repeat bool) Decision {
	user := strings.TrimSpace(t.User)
	steps, distinct := Steps(t.Tools)
	fact := HasFactCue(user)
	teach := HasTeachCue(user)
	workflow := steps >= 5 && distinct >= 2
	d := Decision{}
	switch {
	case Trivial(user) && !workflow:
		d.Why = "trivial turn"
		return d
	case t.SavedSkill:
		d.Why = "a skill was already saved this turn"
		return d
	}
	d.Memory = fact && !t.SavedMemory
	d.Skill = teach || (workflow && repeat)
	d.Reflect = d.Memory || d.Skill
	switch {
	case !d.Reflect && t.SavedMemory:
		d.Why = "memory already saved this turn"
	case !d.Reflect:
		d.Why = "nothing durable in this turn"
	case d.Skill && teach:
		d.Why = "the user taught a method"
	case d.Skill:
		d.Why = "a multi-step workflow repeated"
	default:
		d.Why = "the user stated a fact or preference"
	}
	return d
}

// ---------------- workflow signatures ----------------

var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by can could do does for from had has have how i if in into is it its me my no not of on or our please so than that the their them then there these they this to too up us was we were what when where which who why will with would you your can't don't i'm it's let make want need get got use using also very really some any all about just now again`) {
		stopWords[w] = true
	}
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r)) })
}

// Signature is a turn's workflow fingerprint: the distinct work tools plus the
// user's main words, sorted. Two turns doing the same job look alike.
func Signature(user string, tools []string) []string {
	set := map[string]bool{}
	for _, t := range tools {
		if !lookTools[t] {
			set["tool:"+t] = true
		}
	}
	n := 0
	for _, w := range words(user) {
		if len(w) < 3 || stopWords[w] {
			continue
		}
		if !set[w] {
			set[w] = true
			n++
		}
		if n >= 8 {
			break
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Similarity is the Jaccard index of two signatures.
func Similarity(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	in := map[string]bool{}
	for _, x := range a {
		in[x] = true
	}
	both := 0
	for _, y := range b {
		if in[y] {
			both++
		}
	}
	return float64(both) / float64(len(a)+len(b)-both)
}

// RepeatAt is how alike two workflows must be to count as a repeat.
const RepeatAt = 0.5

// Repeats reports whether sig matches one of the earlier signatures.
func Repeats(sig []string, earlier [][]string) bool {
	for _, e := range earlier {
		if Similarity(sig, e) >= RepeatAt {
			return true
		}
	}
	return false
}

// ---------------- the fact filter ----------------

var (
	secretRe    = regexp.MustCompile(`(?i)(sk-[a-z0-9_-]{8,}|sk-ant-|aiza[0-9a-z_-]{20,}|gh[pousr]_[a-z0-9]{16,}|xox[abpr]-|AKIA[0-9A-Z]{12,}|-----BEGIN|\bpassword\s*(is|:|=)|\bpasscode\b|\bpin\s*(is|:|=)|\bcvv\b|\bseed phrase\b|\b(one[- ]time|otp|2fa|verification) code\b)`)
	longDigitRe = regexp.MustCompile(`\d[\d -]{11,}\d`) // card, account and ID numbers
	transientRe = regexp.MustCompile(`(?i)\b(right now|at the moment|just now|this time|this turn|this chat|this conversation|today|tonight|this morning|this afternoon|for now|currently (?:running|open|broken|failing))\b`)
	taskRe      = regexp.MustCompile(`(?i)^(the user (asked|wants|wanted|requested|is asking)|user asked|asked (me|blaze) to|blaze (ran|did|fixed|created))`)
)

// Accept cleans a fact a reflection proposed and says whether to keep it.
// It refuses secrets, one-off task details and things only true for a moment.
func Accept(fact string) (string, bool, string) {
	f := strings.Join(strings.Fields(fact), " ")
	f = strings.TrimPrefix(strings.TrimPrefix(f, "- "), "* ")
	n := len(strings.Fields(f))
	switch {
	case n < 3:
		return f, false, "too short"
	case n > 45 || len(f) > 300:
		return f, false, "too long for one memory"
	case secretRe.MatchString(f) || longDigitRe.MatchString(f):
		return f, false, "looks like a secret or an account number"
	case transientRe.MatchString(f):
		return f, false, "only true for the moment"
	case taskRe.MatchString(f):
		return f, false, "a task log, not a fact about the user"
	}
	return f, true, ""
}

// ---------------- the reflection call ----------------

// MemoryNote is one memory the reflection wants saved.
type MemoryNote struct {
	Text     string `json:"text"`
	Replaces string `json:"replaces,omitempty"` // "#12" when it supersedes a saved memory
}

// SkillNote is a skill the reflection wants written.
type SkillNote struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Body        string   `json:"body"`
}

// Reflection is the parsed answer of the reflection call.
type Reflection struct {
	Memories []MemoryNote `json:"memories"`
	Skill    *SkillNote   `json:"skill"`
}

// Prompt builds the reflection request. known lists saved memories that look
// related ("#12 text"), skills the existing skill names.
func Prompt(t Turn, d Decision, known []string, skills []string) string {
	var b strings.Builder
	b.WriteString("You maintain the long-term memory of Blaze, a personal assistant. Read the last exchange and decide what is worth keeping for future conversations. Most turns have nothing: then return empty lists.\n\n")
	b.WriteString("Rules:\n- A memory is one short sentence about the user, their setup, preferences, projects or people, with its context. Keep the user's certainty; never generalise one occurrence into a habit.\n- Never save secrets, one-off task details, or things only true today.\n- If a new fact changes a saved one below, set replaces to its id (\"#12\") instead of adding a duplicate. Skip facts already saved.\n- At most 2 memories.\n")
	if d.Skill {
		b.WriteString("- A skill is a reusable method: save one only if the user taught a method or this is a repeated multi-step workflow worth replaying. name kebab-case; description says when to use it; body = numbered steps with the exact commands/checks and pitfalls (under 40 lines). Don't duplicate an existing skill.\n")
	} else {
		b.WriteString("- Do not propose a skill this time (skill: null).\n")
	}
	b.WriteString("\nAnswer with JSON only: {\"memories\":[{\"text\":\"...\",\"replaces\":\"#id or empty\"}],\"skill\":null or {\"name\":\"...\",\"description\":\"...\",\"tags\":[\"...\"],\"body\":\"...\"}}\n\n")
	if len(known) > 0 {
		b.WriteString("Saved memories that may be related:\n" + strings.Join(known, "\n") + "\n\n")
	}
	if d.Skill && len(skills) > 0 {
		b.WriteString("Existing skills: " + strings.Join(skills, ", ") + "\n\n")
	}
	b.WriteString("USER:\n" + clip(t.User, 2400) + "\n\n")
	if len(t.Tools) > 0 {
		b.WriteString("TOOLS USED: " + strings.Join(t.Tools, ", ") + "\n\n")
	}
	b.WriteString("BLAZE ANSWERED:\n" + clip(t.Reply, 1600) + "\n")
	return b.String()
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// Parse reads the reflection answer (tolerating code fences and chatter around
// the JSON) and drops whatever doesn't pass the filters.
func Parse(raw string) (Reflection, error) {
	var r Reflection
	s := strings.TrimSpace(raw)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	} else {
		return r, errors.New("no JSON object in the reflection answer")
	}
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return r, err
	}
	var keep []MemoryNote
	for _, m := range r.Memories {
		if f, ok, _ := Accept(m.Text); ok {
			m.Text = f
			m.Replaces = strings.TrimSpace(m.Replaces)
			if m.Replaces != "" && !regexp.MustCompile(`^#?\d+$`).MatchString(m.Replaces) {
				m.Replaces = ""
			}
			keep = append(keep, m)
		}
		if len(keep) == 2 {
			break
		}
	}
	r.Memories = keep
	if r.Skill != nil && !GoodSkill(*r.Skill) {
		r.Skill = nil
	}
	return r, nil
}

var kebab = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// GoodSkill is the bar for a skill written without the user asking: a proper
// name and description and a body with real steps.
func GoodSkill(s SkillNote) bool {
	body := strings.TrimSpace(s.Body)
	steps := 0
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		if len(l) > 2 && (l[0] >= '0' && l[0] <= '9' || strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "* ")) {
			steps++
		}
	}
	return kebab.MatchString(s.Name) && !strings.Contains(s.Name, "--") && len(strings.Fields(s.Description)) >= 4 &&
		len(body) >= 120 && steps >= 3 && !secretRe.MatchString(body)
}
