package main

// The "/" pop-up menu over the prompt, matching Blaze Desktop's composer menu:
// commands with a one-line description, filtered as you type, and a skills
// sub-menu under "/skills ". "@" opens the files menu: the folder's files and
// folders, filtered as you type; picking a folder lists what's inside it.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	{"/mcp", "MCP servers: status · reload · add · remove · enable|disable", false},
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
	i := strings.LastIndexAny(line, " \n\t")
	if tok := line[i+1:]; strings.HasPrefix(tok, "@") {
		return filesMenu(line[:i+1], tok[1:])
	}
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

const filesMenuMax = 500 // entries read from one folder

// filesMenu lists the folder named by frag ("src/ma" lists src/, filtered by "ma").
// Each item turns the line into before + "@path": a folder ends in "/" so the menu
// moves into it, a file ends in " " so the menu closes and you keep typing.
func filesMenu(before, frag string) *ui.MenuSet {
	i := strings.LastIndexAny(frag, `/\`)
	dir, base := frag[:i+1], frag[i+1:] // i is -1 without a separator: dir "", base frag
	look := dir
	if look == "" {
		look = "."
	}
	if strings.HasPrefix(look, "~") {
		if h, err := os.UserHomeDir(); err == nil {
			look = h + look[1:]
		}
	}
	ents, err := os.ReadDir(look)
	if err != nil {
		return nil
	}
	var dirs, files []ui.MenuItem
	for _, en := range ents {
		nm := en.Name()
		if nm == ".git" || (strings.HasPrefix(nm, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		info, err := os.Stat(filepath.Join(look, nm)) // follows symlinks
		if err != nil {
			continue
		}
		if info.IsDir() {
			dirs = append(dirs, ui.MenuItem{Name: nm + "/", Desc: "folder", Insert: before + "@" + dir + nm + "/"})
		} else {
			files = append(files, ui.MenuItem{Name: nm, Desc: humanSize(info.Size()), Insert: before + "@" + dir + nm + " "})
		}
		if len(dirs)+len(files) >= filesMenuMax {
			break
		}
	}
	byName := func(a []ui.MenuItem) {
		sort.SliceStable(a, func(i, j int) bool { return strings.ToLower(a[i].Name) < strings.ToLower(a[j].Name) })
	}
	byName(dirs)
	byName(files)
	all := append(dirs, files...)
	title := "files"
	if dir != "" {
		title += " · " + dir
	}
	return &ui.MenuSet{Title: title, Items: ui.Rank(all, base, nil), Full: len(all)}
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
}
