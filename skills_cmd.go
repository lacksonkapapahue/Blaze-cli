package main

// v0.5: the bundled skill library (skills/, synced from Blaze Desktop with
// scripts/sync-skills.sh) is embedded and seeded into ~/.blaze/skills on start;
// /skills lists and manages skills, /learn shows whether Blaze learns on its own.

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/skillpack"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

//go:embed all:skills
var skillsFS embed.FS

// seedSkills brings ~/.blaze/skills up to date with the embedded pack (idempotent;
// never overwrites a skill the user edited or deleted) and prints learned changes.
func seedSkills() {
	if sub, err := fs.Sub(skillsFS, "skills"); err == nil {
		_, _ = tools.SeedSkills(sub)
	}
	tools.SetOnLearn(agent.QueueLearn)
}

var stateWord = map[string]string{
	skillpack.StateBundled: "bundled",
	skillpack.StateEdited:  "edited",
	skillpack.StateOwn:     "yours",
	skillpack.StateRemoved: "removed",
}

// skillsCmd: /skills [list|show <name>|on <name>|off <name>|delete <name>|restore <name>]
func skillsCmd(args []string) {
	sub, name := "list", ""
	if len(args) > 0 {
		sub = args[0]
	}
	if len(args) > 1 {
		name = args[1]
	}
	if sub != "list" && name == "" {
		if sub != "help" {
			// "/skills deploy-homeglow" shows that skill
			sub, name = "show", sub
		}
	}
	var err error
	switch sub {
	case "list":
		var rows []string
		on := 0
		for _, v := range tools.SkillStatus() {
			mark := ui.Accent("■")
			if !v.Enabled || v.State == skillpack.StateRemoved {
				mark = ui.Weak("□")
			} else {
				on++
			}
			tag := stateWord[v.State]
			if v.Version > 0 {
				tag += fmt.Sprintf(" v%d", v.Version)
			}
			if v.Update {
				tag += " · update available (/skills restore " + v.Name + ")"
			}
			desc := v.Description
			if r := []rune(desc); len(r) > 70 {
				desc = string(r[:70]) + "…"
			}
			// one row per line: ui.Box draws its borders per row
			rows = append(rows, mark+" "+ui.Ink(v.Name)+ui.Weak("  "+tag), "    "+ui.Sub(desc))
		}
		ui.Box(ui.AccentLabel(fmt.Sprintf("skills · %d on", on)), rows)
		ui.Println("  " + ui.Weak("/skills <name> · on|off|delete|restore <name> · shared with Blaze Desktop in ~/.blaze/skills"))
		return
	case "show":
		ui.Println(tools.Run(&tools.Env{}, "load_skill", fmt.Sprintf(`{"name":%q}`, name)))
		return
	case "on", "off":
		err = tools.SetSkillEnabled(name, sub == "on")
	case "delete", "rm":
		if !ui.Confirm("Delete the skill "+name+"?", "Bundled skills can be restored with /skills restore "+name+".") {
			return
		}
		err = tools.DeleteSkill(name)
	case "restore", "reset":
		err = tools.RestoreSkill(name)
	default:
		ui.Println("  " + ui.Weak("/skills · /skills <name> · /skills on|off|delete|restore <name>"))
		return
	}
	if err != nil {
		ui.Println("  " + ui.Ink("■ ") + ui.Sub(err.Error()))
		return
	}
	ui.Step("Skills: " + sub + " " + name + ".")
}

// learnCmd: /learn [on|off] (also blaze learn [on|off]).
func learnCmd(args []string) {
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "on", "off":
			if err := agent.SetLearn(args[0] == "on"); err != nil {
				ui.Println("  " + ui.Ink("■ ") + ui.Sub(err.Error()))
				return
			}
		default:
			ui.Println("  " + ui.Weak("/learn · /learn on · /learn off"))
			return
		}
	}
	if agent.LearnOn() {
		ui.Step("Learning automatically: on. Blaze saves lasting facts and repeated workflows (memory, ~/.blaze/skills). Turn off with /learn off (shared with Blaze Desktop › Settings › Behaviour › Learning).")
	} else {
		ui.Step("Learning automatically: off. Blaze saves memories and skills only when you ask (# note, \"save this as a skill\"). Turn on with /learn on.")
	}
	if v := os.Getenv("BLAZE_LEARN"); v != "" {
		ui.Println("  " + ui.Weak("BLAZE_LEARN="+v+" overrides the setting in this shell."))
	}
}
