package ui

// The pop-up menu over the prompt, like Blaze Desktop's composer menu: type "/"
// and the commands appear above the box, filtered as you type; ↑ ↓ move,
// Enter or Tab picks, Esc closes. Sub-menus (e.g. "/skills ") work the same.

import (
	"fmt"
	"strings"
)

// MenuItem is one row of the pop-up menu.
type MenuItem struct {
	Name   string // shown in the left column (e.g. "/skills", "deploy-homeglow")
	Desc   string // dim text after it
	Insert string // what the line becomes when picked
	Submit bool   // Enter on it also submits the line (Tab only fills it in)
	// Section groups rows under a thin label (R E C E N T); "" = no label. The
	// recent commands get it automatically (withRecent).
	Section string
	// DescAccent draws the description in the accent instead of the sub tone (the
	// @ menu's reference types: @diff, @file:, @url:…).
	DescAccent bool
}

// MenuSet is what the Menu callback returns for the current line.
type MenuSet struct {
	Title string
	Items []MenuItem // already filtered and ranked
	Full  int        // size of the unfiltered set, so the menu keeps its height while filtering
	Query string     // what the rows were filtered by, drawn in the accent ("" = the line's last word)
}

const menuRows = 8 // visible rows; the list scrolls past this

// Rank filters items by q the way Desktop does: names that start with q first,
// then names that contain it, then (from three letters on) descriptions. key picks the text to match.
func Rank(items []MenuItem, q string, key func(MenuItem) string) []MenuItem {
	q = strings.ToLower(q)
	if key == nil {
		key = func(it MenuItem) string { return it.Name }
	}
	var exact, head, tail []MenuItem
	for _, it := range items {
		k := strings.ToLower(key(it))
		switch {
		case k == q:
			exact = append(exact, it)
		case strings.HasPrefix(k, q):
			head = append(head, it)
		case strings.Contains(k, q) || (len(q) >= 3 && strings.Contains(strings.ToLower(it.Desc), q)):
			tail = append(tail, it)
		}
	}
	return append(append(exact, head...), tail...)
}

// menuNow is the open menu for the current line, or nil.
func (e *Editor) menuNow() *MenuSet {
	if e.Menu == nil || e.pos != len(e.buf) {
		return nil
	}
	s := string(e.buf)
	if s == "" || strings.Contains(s, "\n") || s == e.menuOff {
		return nil
	}
	m := withRecent(s, e.Menu(s), Recent())
	if m == nil || len(m.Items) == 0 {
		return nil
	}
	if s != e.menuLine { // the filter changed: back to the top, like Desktop
		e.menuLine, e.menuIdx = s, 0
	}
	if e.menuIdx >= len(m.Items) {
		e.menuIdx = len(m.Items) - 1
	}
	return m
}

// menuMove moves the highlight; it reports whether a menu was open.
func (e *Editor) menuMove(d int) bool {
	m := e.menuNow()
	if m == nil {
		return false
	}
	n := len(m.Items)
	e.menuIdx = ((e.menuIdx+d)%n + n) % n
	return true
}

// menuPick fills in the highlighted item. submit reports whether Enter should
// also send the line. ok is false when no menu was open.
func (e *Editor) menuPick(enter bool) (submit, ok bool) {
	m := e.menuNow()
	if m == nil {
		return false, false
	}
	it := m.Items[e.menuIdx]
	if string(e.buf) == it.Insert && !strings.HasSuffix(it.Insert, " ") {
		// already exactly this: Enter just runs it
		return enter, true
	}
	e.buf = []rune(it.Insert)
	e.pos = len(e.buf)
	e.menuLine, e.menuIdx = "", 0
	return enter && it.Submit, true
}

// menuGhost is the dim rest of the highlighted item after the cursor.
func (e *Editor) menuGhost() (string, bool) {
	m := e.menuNow()
	if m == nil {
		return "", false
	}
	s, ins := string(e.buf), m.Items[e.menuIdx].Insert
	if strings.HasPrefix(ins, s) {
		return strings.TrimSuffix(ins[len(s):], " "), true
	}
	return "", true
}

// menuLines draws the open menu as a hairline box bw wide (nil when closed).
func (e *Editor) menuLines(bw int) []string {
	m := e.menuNow()
	if m == nil {
		return nil
	}
	return drawMenu(m, e.menuIdx, bw)
}

// drawMenu draws m with row idx highlighted as a hairline box bw wide. The prompt
// and the composer under the working line share it.
func drawMenu(m *MenuSet, idx, bw int) []string {
	if bw < 30 {
		bw = 30
	}
	inner := bw - 4 // "│ " + text + " │"
	height := min(max(m.Full, len(m.Items)), menuRows)
	nameW := 0
	for _, it := range m.Items {
		nameW = max(nameW, width(it.Name))
	}
	nameW = min(nameW+3, inner/2)

	// display rows: the items, with a section label where the section changes
	// (only when the menu has more than one section)
	type drow struct {
		item  int    // index into m.Items, -1 for a label
		label string // section label
	}
	var rows []drow
	sections := 0
	prev := "\x00"
	for _, it := range m.Items {
		if it.Section != prev {
			if it.Section != "" {
				sections++
			}
			prev = it.Section
		}
	}
	prev = "\x00"
	sel := 0
	for i, it := range m.Items {
		if sections > 1 && it.Section != prev && it.Section != "" {
			rows = append(rows, drow{item: -1, label: it.Section})
		}
		prev = it.Section
		if i == idx {
			sel = len(rows)
		}
		rows = append(rows, drow{item: i})
	}
	if sections > 1 {
		height += sections
	}
	start := 0
	if sel >= height {
		start = sel - height + 1
	}
	if start > 0 && rows[start-1].item == -1 && sel-start+1 < height {
		start-- // keep the label of the first visible section in view
	}

	title := AccentLabel(m.Title)
	var out []string
	top := hair("┌─ ") + title + " " + hair(strings.Repeat("─", max(0, bw-width(title)-5))+"┐")
	if m.Title == "" {
		top = hair("┌" + strings.Repeat("─", bw-2) + "┐")
	}
	out = append(out, "  "+top)
	for k := start; k < start+height; k++ {
		row := ""
		if k < len(rows) && rows[k].item < 0 {
			row = Label(clip(rows[k].label, max(1, (inner-2)/2)))
		} else if k < len(rows) {
			i := rows[k].item
			it := m.Items[i]
			name := clip(it.Name, nameW-3)
			pad := strings.Repeat(" ", max(1, nameW-1-width(name)))
			desc := clip(it.Desc, max(0, inner-nameW-1))
			if i == idx {
				row = Accent("▸ ") + highlight(name, m.Query, Accent, true) + pad + Ink(desc)
			} else {
				d := Sub(desc)
				if it.DescAccent {
					d = Accent(desc)
				}
				row = "  " + highlight(name, m.Query, Body, false) + pad + d
			}
		}
		out = append(out, "  "+hair("│ ")+row+strings.Repeat(" ", max(0, inner-width(row)))+hair(" │"))
	}
	hint := "↑↓ move · enter pick · tab fill in · esc close"
	if len(rows) > height {
		hint = fmt.Sprintf("%d/%d · ", idx+1, len(m.Items)) + hint
	}
	out = append(out, "  "+hair("└─ ")+Weak(hint)+" "+hair(strings.Repeat("─", max(0, bw-width(hint)-5))+"┘"))
	return out
}

// PickList shows rows as a list (like the setup picker): ↑ ↓ move, Enter picks, Esc
// cancels. It returns the picked index, or -1. Long lists scroll.
func PickList(title string, rows []string, def int) int {
	if len(rows) == 0 {
		return -1
	}
	if def < 0 || def >= len(rows) {
		def = 0
	}
	if !StdinTTY() || !IsTTY() {
		if title != "" {
			Println("  " + Ink(title))
		}
		Numbered(rows)
		ans, err := ReadLine("  " + Ink("Which one? ") + Weak(fmt.Sprintf("number · Enter = %d ", def+1)))
		if err != nil {
			return -1
		}
		ans = strings.TrimSpace(ans)
		if ans == "" {
			return def
		}
		var n int
		if _, err := fmt.Sscan(ans, &n); err == nil && n >= 1 && n <= len(rows) {
			return n - 1
		}
		return -1
	}
	cur := def
	W := termWidth()
	h := min(len(rows), 10)
	start := 0
	if title != "" {
		Println("  " + Ink(title))
	}
	draw := func(first bool) {
		if !first {
			Printf("\x1b[%dA", h+1)
		}
		if cur < start {
			start = cur
		}
		if cur >= start+h {
			start = cur - h + 1
		}
		for i := start; i < start+h; i++ {
			ptr, row := "  ", Body(rows[i])
			if i == cur {
				ptr, row = Accent("▸ "), Ink(rows[i])
			}
			line := "  " + ptr + row
			if width(line) > W-1 {
				line = "  " + ptr + clip(ansi.ReplaceAllString(rows[i], ""), W-6)
			}
			Printf("\r\x1b[2K%s\r\n", line)
		}
		hint := "↑ ↓ move · enter pick · esc cancel"
		if len(rows) > h {
			hint = fmt.Sprintf("%d/%d · ", cur+1, len(rows)) + hint
		}
		Printf("\r\x1b[2K  %s\r\n", Weak(hint))
	}
	Printf("\x1b[?25l")
	draw(true)
	canceled := false
	rawKeys(func(k string) bool {
		switch k {
		case "up", "k":
			cur = (cur + len(rows) - 1) % len(rows)
		case "down", "j", "tab":
			cur = (cur + 1) % len(rows)
		case "enter", "space":
			return true
		case "esc", "ctrl-c", "q":
			canceled = true
			return true
		}
		draw(false)
		return false
	})
	// settle on the choice only
	Printf("\x1b[%dA\r\x1b[J\x1b[?25h", h+1)
	if canceled {
		return -1
	}
	Println("  " + Accent("■ ") + Ink(ansi.ReplaceAllString(rows[cur], "")))
	return cur
}
