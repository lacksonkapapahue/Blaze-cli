package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

func memFile() string { return filepath.Join(config.Dir(), "memory.md") }

// Memory returns the saved facts for the system prompt.
func Memory() string {
	b, _ := os.ReadFile(memFile())
	return strings.TrimSpace(string(b))
}

func skillsDir() string { return dataDir("skills") }

type Skill struct{ Name, Desc, Path string }

// Skills lists ~/.blaze/skills/*/SKILL.md (same format as the Android app).
func Skills() []Skill {
	var out []Skill
	ents, _ := os.ReadDir(skillsDir())
	for _, en := range ents {
		if !en.IsDir() {
			continue
		}
		f := filepath.Join(skillsDir(), en.Name(), "SKILL.md")
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		s := Skill{Name: en.Name(), Path: f}
		txt := string(b)
		if strings.HasPrefix(txt, "---") {
			if end := strings.Index(txt[3:], "---"); end > 0 {
				for _, l := range strings.Split(txt[3:3+end], "\n") {
					if v, ok := strings.CutPrefix(strings.TrimSpace(l), "name:"); ok {
						s.Name = strings.Trim(strings.TrimSpace(v), `"'`)
					}
					if v, ok := strings.CutPrefix(strings.TrimSpace(l), "description:"); ok {
						s.Desc = strings.Trim(strings.TrimSpace(v), `"'`)
					}
				}
			}
		}
		out = append(out, s)
	}
	return out
}

func init() {
	register("remember", "Save a lasting fact about the user or their setup (preferences, project paths, how they like things). Not for one-off task details.",
		[]prop{p("fact", "string", "One short sentence")}, []string{"fact"},
		func(e *Env, a args) string {
			_ = os.MkdirAll(config.Dir(), 0o700)
			f, err := os.OpenFile(memFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return "Error: " + err.Error()
			}
			defer f.Close()
			fmt.Fprintf(f, "- %s (%s)\n", strings.TrimSpace(a.s("fact")), time.Now().Format("2006-01-02"))
			return "Remembered."
		})

	register("forget", "Remove saved facts containing some text.",
		[]prop{p("text", "string", "Text to match")}, []string{"text"},
		func(e *Env, a args) string {
			b, _ := os.ReadFile(memFile())
			var keep []string
			n := 0
			for _, l := range strings.Split(string(b), "\n") {
				if l != "" && strings.Contains(strings.ToLower(l), strings.ToLower(a.s("text"))) {
					n++
					continue
				}
				if l != "" {
					keep = append(keep, l)
				}
			}
			_ = os.WriteFile(memFile(), []byte(strings.Join(keep, "\n")+"\n"), 0o600)
			return fmt.Sprintf("Forgot %d fact(s).", n)
		})

	register("use_skill", "Load a skill's full instructions (listed in your prompt) before doing that kind of task.",
		[]prop{p("name", "string", "Skill name")}, []string{"name"},
		func(e *Env, a args) string {
			for _, s := range Skills() {
				if strings.EqualFold(s.Name, a.s("name")) || strings.EqualFold(filepath.Base(filepath.Dir(s.Path)), a.s("name")) {
					b, _ := os.ReadFile(s.Path)
					return "Skill folder: " + filepath.Dir(s.Path) + " (scripts inside can be run with run_shell)\n\n" + string(b)
				}
			}
			return "No skill called " + a.s("name") + "."
		})

	register("write_skill", "Save a reusable skill (a SKILL.md with name, description and steps) so next time the task goes faster.",
		[]prop{p("name", "string", "kebab-case name"), p("description", "string", "When to use it, one line"), p("body", "string", "Markdown steps")},
		[]string{"name", "description", "body"},
		func(e *Env, a args) string {
			d := filepath.Join(skillsDir(), a.s("name"))
			_ = os.MkdirAll(d, 0o755)
			txt := "---\nname: " + a.s("name") + "\ndescription: " + a.s("description") + "\n---\n\n" + a.s("body") + "\n"
			if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(txt), 0o644); err != nil {
				return "Error: " + err.Error()
			}
			return "Saved skill " + a.s("name") + "."
		})
}
