package ui

// The command menu: typing "/" opens a filtered list of commands with one-line
// descriptions right above the input box, narrowing with every key; "@" opens the
// same list for files and folders, and a command with an argument menu ("/skills ")
// lists its choices (the skills) the same way. ↑ ↓ (or Ctrl+P/N) select, Tab
// completes, Enter runs (a command or choice) or picks (a file), Esc closes.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MenuItem is one row of the menu.
type MenuItem struct {
	Name string // "/model", a skill name, or a file name ("src/", "main.go")
	Desc string // one line
}

// ArgMenu lists the choices for a command's argument ("/skills <name>").
type ArgMenu struct {
	Items func() []MenuItem // the choices, read when the menu opens
	Skip  []string          // subcommand words that turn the menu off ("on", "delete"…)
	Title string            // the menu's caption ("skills")
	Empty string            // shown when nothing matches
}

// menuRows is how many rows the menu shows at once.
const menuRows = 8

// FilterCommands narrows a menu to what was typed: names that start with it first
// (in the given order), then names that contain it, then names whose letters
// appear in order (fuzzy), then descriptions that mention it. A leading "/" is ignored.
func FilterCommands(items []MenuItem, q string) []MenuItem {
	q = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q), "/"))
	if q == "" {
		return append([]MenuItem(nil), items...)
	}
	var pre, sub, fuzzy, desc []MenuItem
	for _, it := range items {
		n := strings.ToLower(strings.TrimPrefix(it.Name, "/"))
		switch {
		case strings.HasPrefix(n, q):
			pre = append(pre, it)
		case strings.Contains(n, q):
			sub = append(sub, it)
		case subsequence(n, q):
			fuzzy = append(fuzzy, it)
		case len(q) >= 2 && strings.Contains(strings.ToLower(it.Desc), q):
			desc = append(desc, it)
		}
	}
	return append(append(append(pre, sub...), fuzzy...), desc...)
}

func subsequence(s, q string) bool {
	i := 0
	for _, r := range s {
		if i < len(q) && rune(q[i]) == r {
			i++
		}
	}
	return i == len(q)
}

// FileItems is the "@" menu: entries of frag's folder (relative to dir) whose
// names start with frag's last part, folders first. Hidden ones only when asked for.
func FileItems(dir, frag string) []MenuItem {
	d, base := filepath.Split(frag)
	look := d
	if strings.HasPrefix(look, "~") {
		h, _ := os.UserHomeDir()
		look = h + look[1:]
	}
	if look == "" {
		look = "."
	}
	if !filepath.IsAbs(look) && dir != "" {
		look = filepath.Join(dir, look)
	}
	ents, err := os.ReadDir(look)
	if err != nil {
		return nil
	}
	var dirs, files []MenuItem
	lb := strings.ToLower(base)
	for _, en := range ents {
		nm := en.Name()
		if strings.HasPrefix(nm, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(nm), lb) {
			continue
		}
		if en.IsDir() {
			dirs = append(dirs, MenuItem{Name: d + nm + "/", Desc: "folder"})
			continue
		}
		desc := "file"
		if info, err := en.Info(); err == nil {
			desc = humanSize(info.Size())
		}
		files = append(files, MenuItem{Name: d + nm, Desc: desc})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	out := append(dirs, files...)
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// menuState is what the open menu shows.
type menuState struct {
	kind  string // "/" commands, "@" files, "arg" a command's choices ("" = closed)
	items []MenuItem
	start int    // where the @word starts in the buffer
	cmd   string // kind "arg": the command ("/skills")
	title string
	empty string
}

// menu works out the menu for the current buffer.
func (e *Editor) menu() menuState {
	s := string(e.buf)
	if e.menuOff {
		if s == e.menuOffAt {
			return menuState{}
		}
		e.menuOff = false
	}
	atEnd := e.pos == len(e.buf)
	if strings.HasPrefix(s, "/") && !strings.ContainsAny(s, " \n") && atEnd && len(e.Menu) > 0 {
		return menuState{kind: "/", items: FilterCommands(e.Menu, s), title: "commands", empty: "no matching command"}
	}
	if strings.HasPrefix(s, "/") && atEnd && !strings.Contains(s, "\n") {
		if cmd, rest, ok := strings.Cut(s, " "); ok {
			if am, ok := e.ArgMenus[cmd]; ok && am.Items != nil {
				first, _, more := strings.Cut(strings.TrimLeft(rest, " "), " ")
				skip := false
				for _, w := range am.Skip {
					skip = skip || (more && strings.EqualFold(first, w))
				}
				if !skip && !strings.Contains(strings.TrimSpace(rest), " ") {
					title, empty := am.Title, am.Empty
					if title == "" {
						title = strings.TrimPrefix(cmd, "/")
					}
					if empty == "" {
						empty = "nothing matches"
					}
					return menuState{kind: "arg", cmd: cmd, items: FilterCommands(am.Items(), rest), title: title, empty: empty}
				}
			}
		}
	}
	start := e.pos
	for start > 0 && e.buf[start-1] != ' ' && e.buf[start-1] != '\n' {
		start--
	}
	word := string(e.buf[start:e.pos])
	if strings.HasPrefix(word, "@") && (atEnd || e.buf[e.pos] == ' ' || e.buf[e.pos] == '\n') {
		dir := ""
		if e.Dir != nil {
			dir = e.Dir()
		}
		if dir == "" {
			dir, _ = os.Getwd()
		}
		return menuState{kind: "@", items: FileItems(dir, word[1:]), start: start, title: "files", empty: "nothing here matches"}
	}
	return menuState{}
}

// clampSel keeps the selection inside the list.
func (e *Editor) clampSel(n int) {
	if n == 0 {
		e.sel = 0
		return
	}
	e.sel = ((e.sel % n) + n) % n
}

// menuAccept puts the selected item into the buffer. It returns true when the
// item is a folder (the menu stays open to go inside it).
func (e *Editor) menuAccept(m menuState) bool {
	if len(m.items) == 0 {
		return false
	}
	e.clampSel(len(m.items))
	it := m.items[e.sel]
	e.sel = 0
	switch m.kind {
	case "/":
		e.buf = []rune(it.Name + " ")
		e.pos = len(e.buf)
	case "arg":
		e.buf = []rune(m.cmd + " " + it.Name)
		e.pos = len(e.buf)
	case "@":
		ins := "@" + it.Name
		folder := strings.HasSuffix(it.Name, "/")
		if !folder {
			ins += " "
		}
		nb := append([]rune{}, e.buf[:m.start]...)
		nb = append(nb, []rune(ins)...)
		nb = append(nb, e.buf[e.pos:]...)
		e.buf, e.pos = nb, m.start+len([]rune(ins))
		return folder
	}
	return false
}

// menuFrame draws the open menu as a box over the input box; the input box's top
// border becomes the joint (├──┤). BW is the box width.
func (e *Editor) menuFrame(m menuState, BW int) []string {
	if m.kind == "" {
		return nil
	}
	inner := BW - 2
	title := m.title
	head := hair("┌─ ") + Weak(title) + " " + hair(strings.Repeat("─", max(0, inner-len([]rune(title))-3))+"┐")
	out := []string{"  " + head}
	if len(m.items) == 0 {
		msg := clip(m.empty, inner-4)
		out = append(out, "  "+hair("│")+"   "+Weak(msg)+strings.Repeat(" ", max(0, inner-3-width(msg)))+hair("│"))
		return out
	}
	e.clampSel(len(m.items))
	first := 0
	if e.sel >= menuRows {
		first = e.sel - menuRows + 1
	}
	last := min(len(m.items), first+menuRows)
	nameW := 0
	for _, it := range m.items[first:last] {
		nameW = max(nameW, len([]rune(it.Name)))
	}
	nameW = min(nameW, max(10, inner/3))
	for i := first; i < last; i++ {
		it := m.items[i]
		name := clip(it.Name, nameW)
		pad := strings.Repeat(" ", nameW-len([]rune(name)))
		room := inner - 3 - nameW - 3 - 1
		desc := clip(it.Desc, max(0, room))
		fill := strings.Repeat(" ", max(0, inner-3-nameW-3-width(desc)))
		var row string
		if i == e.sel {
			row = bgTint(pal.accent) + fg(pal.accent) + " ▸ " + bold() + name + reset() + bgTint(pal.accent) + pad + "   " + fg(pal.ink) + desc + fill + reset()
		} else {
			row = "   " + Ink(name) + pad + "   " + Sub(desc) + fill
		}
		out = append(out, "  "+hair("│")+row+hair("│"))
	}
	if len(m.items) > last-first {
		more := fmt.Sprintf("%d of %d", e.sel+1, len(m.items))
		out = append(out, "  "+hair("│")+strings.Repeat(" ", max(0, inner-len(more)-1))+Weak(more)+" "+hair("│"))
	}
	return out
}
