package tools

// The bundled skill library (v0.5) and the "learned something" hook.
//
// The pack ships inside the binary (skills/ at the repo root, a copy of Blaze Desktop's
// pack synced by scripts/sync-skills.sh, embedded by the main package) and is seeded into ~/.blaze/skills on start by internal/skillpack;
// the folder stays the source of truth.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lacksonkapapahue/blaze-cli/internal/skillpack"
)

var pack struct {
	sync.Mutex
	skills []skillpack.Skill
	report skillpack.Report
}

// SeedSkills loads the embedded pack and brings the skills folder up to date
// (idempotent; never touches a skill the user edited or deleted).
func SeedSkills(fsys fs.FS) (skillpack.Report, error) {
	p, err := skillpack.Load(fsys)
	if err != nil {
		return skillpack.Report{}, err
	}
	r, err := skillpack.Seed(skillsDir(), p)
	pack.Lock()
	pack.skills, pack.report = p, r
	pack.Unlock()
	return r, err
}

// BundledSkills is the pack this build ships.
func BundledSkills() []skillpack.Skill {
	pack.Lock()
	defer pack.Unlock()
	return pack.skills
}

// SkillStatus lists every skill for Settings › Skills.
func SkillStatus() []skillpack.View { return skillpack.Status(skillsDir(), BundledSkills()) }

// SetSkillEnabled turns a skill on or off for prompts.
func SetSkillEnabled(name string, on bool) error { return skillpack.SetEnabled(skillsDir(), name, on) }

// DeleteSkill removes a skill (a bundled one stays removed until restored).
func DeleteSkill(name string) error {
	if err := skillpack.Delete(skillsDir(), name); err != nil {
		return err
	}
	return nil
}

// RestoreSkill writes the shipped version of a bundled skill again.
func RestoreSkill(name string) error {
	if err := skillpack.Restore(skillsDir(), name, BundledSkills()); err != nil {
		return err
	}
	return nil
}

func disabledSkills() map[string]bool { return skillpack.DisabledSet(skillsDir()) }

// WriteSkill creates or replaces ~/.blaze/skills/<name>/SKILL.md. It keeps the
// skill's version number moving up so other Blaze apps see a change.
func WriteSkill(name, desc string, tags []string, body string) (created bool, err error) {
	name = strings.TrimSpace(strings.ToLower(name))
	if !skillpack.ValidName(name) {
		return false, errBadSkillName
	}
	f := filepath.Join(skillsDir(), name, "SKILL.md")
	ver := 1
	if old, e := os.ReadFile(f); e == nil {
		m, _, _ := skillpack.ParseFront(string(old))
		ver = max(m.Version, 1) + 1
		if len(tags) == 0 {
			tags = m.Tags
		}
	} else {
		created = true
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return created, err
	}
	txt := skillpack.Render(skillpack.Meta{Name: name, Description: desc, Tags: tags, Version: ver}, body)
	if err := os.WriteFile(f, []byte(txt), 0o644); err != nil {
		return created, err
	}
	return created, nil
}

type skillErr string

func (e skillErr) Error() string { return string(e) }

const errBadSkillName = skillErr("Skill names are kebab-case: lower-case letters, digits and dashes, 2-63 characters (e.g. deploy-homeglow).")

func splitTags(s string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		if t = strings.TrimSpace(strings.ToLower(t)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ---------------- the learned-something hook ----------------

// LearnEvent tells the UI that Blaze saved, changed or removed a memory or skill
// (the chat shows a small mark and a toast).
type LearnEvent struct {
	Kind   string `json:"kind"`   // memory, skill
	Action string `json:"action"` // saved, updated, replaced, forgot, created, edited
	Text   string `json:"text"`   // the memory, or the skill's name
	Ref    string `json:"ref"`    // "#12" or the skill name
	Auto   bool   `json:"auto"`   // from the end-of-turn reflection, not a tool call
}

// OnLearn is set by the app (nil = nobody listening).
var OnLearn func(LearnEvent)

var learnMu sync.Mutex

// Learned reports a change to OnLearn.
func Learned(ev LearnEvent) {
	learnMu.Lock()
	fn := OnLearn
	learnMu.Unlock()
	if fn != nil {
		fn(ev)
	}
}

// SetOnLearn installs the listener.
func SetOnLearn(fn func(LearnEvent)) {
	learnMu.Lock()
	OnLearn = fn
	learnMu.Unlock()
}
