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
}

// MenuSet is what the Menu callback returns for the current line.
type MenuSet struct {
	Title string
	Items []MenuItem // already filtered and ranked
	Full  int        // size of the unfiltered set, so the menu keeps its height while filtering
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
	m := e.Menu(s)
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
	start := 0
	if idx >= height {
		start = idx - height + 1
	}
	nameW := 0
	for _, it := range m.Items {
		nameW = max(nameW, width(it.Name))
	}
	nameW = min(nameW+3, inner/2)

	title := AccentLabel(m.Title)
	var out []string
	top := hair("┌─ ") + title + " " + hair(strings.Repeat("─", max(0, bw-width(title)-5))+"┐")
	if m.Title == "" {
		top = hair("┌" + strings.Repeat("─", bw-2) + "┐")
	}
	out = append(out, "  "+top)
	for i := start; i < start+height; i++ {
		row := ""
		if i < len(m.Items) {
			it := m.Items[i]
			name := clip(it.Name, nameW-3)
			pad := strings.Repeat(" ", max(1, nameW-1-width(name)))
			desc := clip(it.Desc, max(0, inner-nameW-1))
			if i == idx {
				row = Accent("▸ "+name) + pad + Ink(desc)
			} else {
				row = "  " + Body(name) + pad + Sub(desc)
			}
		}
		out = append(out, "  "+hair("│ ")+row+strings.Repeat(" ", max(0, inner-width(row)))+hair(" │"))
	}
	hint := "↑↓ move · enter pick · tab fill in · esc close"
	if len(m.Items) > height {
		hint = fmt.Sprintf("%d/%d · ", idx+1, len(m.Items)) + hint
	}
	out = append(out, "  "+hair("└─ ")+Weak(hint)+" "+hair(strings.Repeat("─", max(0, bw-width(hint)-5))+"┘"))
	return out
}
