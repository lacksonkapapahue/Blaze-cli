package main

// The "/" pop-up menu over the prompt, matching Blaze Desktop's composer menu:
// commands with a one-line description, filtered as you type, and a skills
// sub-menu under "/skills ".

import (
	"strings"

	"github.com/lacksonkapapahue/blaze-cli/internal/skillpack"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

type slashCmd struct {
	name, desc string
	sub        bool // Enter opens a sub-menu ("/skills ") instead of running it
}

// slashMenuCmds is the menu order (the same order as Desktop where they overlap).
var slashMenuCmds = []slashCmd{
	{"/clear", "Clear this chat and start fresh", false},
	{"/resume", "Reload this folder's last conversation", false},
	{"/compact", "Summarise the chat to free up context", false},
	{"/init", "Write BLAZE.md: how this project builds, tests, runs", false},
	{"/model", "Pick the model Blaze uses (saved for next time)", true},
	{"/skills", "Use one of your skills", true},
	{"/plan", "Plan mode: read and scout only, then a plan (Shift+Tab)", false},
	{"/build", "Build mode: every tool (Shift+Tab)", false},
	{"/autopilot", "Work until done; decide instead of asking (Shift+Tab)", false},
	{"/auto", "Ask before risky actions (Shift+Tab)", false},
	{"/agents", "Background agents: list · <name> · msg · cancel · resume", false},
	{"/jobs", "Background jobs and their logs", false},
	{"/schedule", "Scheduled jobs", false},
	{"/learn", "Learn memories and skills automatically: on | off", false},
	{"/login", "Sign in to your Blaze account", false},
	{"/whoami", "Who's signed in", false},
	{"/logout", "Sign out (here and in Blaze Desktop)", false},
	{"/connect", "Connect an app: google · github · notion · slack · spotify", false},
	{"/disconnect", "Disconnect an app", false},
	{"/keys", "Your model keys (shared with Desktop and your phone): sync", false},
	{"/notify", "Desktop notifications when a task finishes", false},
	{"/pc", "What Blaze detected about this PC", false},
	{"/doctor", "Check models, shell, tools and scheduler", false},
	{"/setup", "Detect this PC, add free models, install tools", false},
	{"/update", "Check for a new version of Blaze", false},
	{"/help", "Commands and keys", false},
	{"/exit", "Quit Blaze", false},
}

var skillActions = []ui.MenuItem{
	{Name: "list", Desc: "The whole skill library", Insert: "/skills list", Submit: true},
	{Name: "on", Desc: "Turn a skill on", Insert: "/skills on "},
	{Name: "off", Desc: "Turn a skill off", Insert: "/skills off "},
	{Name: "delete", Desc: "Delete a skill", Insert: "/skills delete "},
	{Name: "restore", Desc: "Restore a bundled skill", Insert: "/skills restore "},
}

// slashMenu is the Editor's Menu callback.
func slashMenu(line string) *ui.MenuSet {
	if !strings.HasPrefix(line, "/") {
		return nil
	}
	cmd, rest, spaced := strings.Cut(line, " ")
	if !spaced {
		all := make([]ui.MenuItem, 0, len(slashMenuCmds))
		for _, c := range slashMenuCmds {
			it := ui.MenuItem{Name: c.name, Desc: c.desc, Insert: c.name, Submit: true}
			if c.sub {
				it.Name += " ›"
				it.Insert += " "
				it.Submit = false
			}
			all = append(all, it)
		}
		q := strings.TrimPrefix(cmd, "/")
		items := ui.Rank(all, q, func(it ui.MenuItem) string { return strings.TrimSpace(strings.TrimPrefix(it.Insert, "/")) })
		return &ui.MenuSet{Title: "commands", Items: items, Full: len(all)}
	}
	if cmd == "/model" && menuCfg != nil {
		return modelMenu(rest, menuCfg)
	}
	if cmd == "/skills" {
		return skillsMenu(rest, tools.SkillStatus())
	}
	return nil
}

// skillsMenu: "/skills <q>" lists your skills (then the actions) filtered by q;
// "/skills on|off|delete|restore <q>" lists the skills that action applies to.
func skillsMenu(rest string, views []skillpack.View) *ui.MenuSet {
	byName := func(it ui.MenuItem) string { return it.Name }
	if act, q, ok := strings.Cut(rest, " "); ok {
		switch act {
		case "on", "off", "delete", "rm", "restore", "reset", "show":
		default:
			return nil // free text after a skill name: no menu
		}
		if strings.Contains(q, " ") {
			return nil
		}
		var all []ui.MenuItem
		for _, v := range views {
			removed := v.State == skillpack.StateRemoved
			switch act {
			case "on":
				if v.Enabled || removed {
					continue
				}
			case "off":
				if !v.Enabled || removed {
					continue
				}
			case "delete", "rm":
				if removed {
					continue
				}
			case "restore", "reset":
				if v.State == skillpack.StateOwn {
					continue
				}
			}
			all = append(all, ui.MenuItem{Name: v.Name, Desc: skillDesc(v), Insert: "/skills " + act + " " + v.Name, Submit: true})
		}
		return &ui.MenuSet{Title: "skills · " + act, Items: ui.Rank(all, q, byName), Full: len(all)}
	}
	var skills []ui.MenuItem
	for _, v := range views {
		if v.State == skillpack.StateRemoved {
			continue
		}
		// like Desktop: picking a skill starts a message that uses it
		skills = append(skills, ui.MenuItem{Name: v.Name, Desc: skillDesc(v), Insert: "Use the " + v.Name + " skill: "})
	}
	items := append(ui.Rank(skills, rest, byName), ui.Rank(skillActions, rest, byName)...)
	return &ui.MenuSet{Title: "skills", Items: items, Full: len(skills) + len(skillActions)}
}

func skillDesc(v skillpack.View) string {
	d := strings.Join(strings.Fields(v.Description), " ")
	switch {
	case v.State == skillpack.StateRemoved:
		return "removed · " + d
	case !v.Enabled:
		return "off · " + d
	}
	return d
}
