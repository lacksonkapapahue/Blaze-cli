package ui

// The docked composer (v0.8.2). While a turn runs, the same boxed chat bar as the
// prompt stays on screen under the working line instead of a short "› type to queue"
// tail beside it: what you type shows in the box, "/" and "@" open the same pop-up
// menu above it (↑ ↓ move, Tab or Enter fills in, Esc closes), and Enter queues the
// message for Blaze's next turn. The working line, the menu, the box and its footer
// are one live region at the bottom of the screen: every spinner tick erases and
// redraws it, and anything printed during the turn (Above) clears it first.
//
// It needs color and a terminal at least 30 columns wide (like the boxed prompt);
// otherwise the working line keeps the one-line composer tail (typeaheadTail).

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

var (
	// DockGlyph is the prompt glyph in the box (the mode: › ▤ »); set by the host.
	DockGlyph func() string
	// DockMenu is the pop-up menu for the typed line ("/" commands, "@" files); set by the host.
	DockMenu func(line string) *MenuSet

	dockMu    sync.Mutex
	dockDrawn int // rows the live region took when last drawn (0: nothing on screen)
)

const dockPlaceholder = "Type your next message · Enter queues it for when this step is done"

// dockUsable reports whether the composer box can be drawn under the working line.
func dockUsable() bool { return color && termWidth() >= 30 }

// dockMenu is the open menu for the composer's line, or nil. Called with ta.mu held.
func (t *typeahead) dockMenu() *MenuSet {
	if DockMenu == nil || len(t.buf) == 0 {
		return nil
	}
	s := string(t.buf)
	if s == t.menuOff {
		return nil
	}
	m := DockMenu(s)
	if m == nil || len(m.Items) == 0 {
		return nil
	}
	if s != t.menuLine {
		t.menuLine, t.menuIdx = s, 0
	}
	if t.menuIdx >= len(m.Items) {
		t.menuIdx = len(m.Items) - 1
	}
	return m
}

// dockLines draws the menu (when open), the box and its footer for width W.
// nil while no turn runs or when the box can't be drawn.
func dockLines(W int) []string {
	if !dockUsable() {
		return nil
	}
	ta.mu.Lock()
	defer ta.mu.Unlock()
	if !ta.on {
		return nil
	}
	BW := min(W-3, 110) // same width as the prompt's box and the welcome card
	glyph := Accent("› ")
	if DockGlyph != nil {
		glyph = DockGlyph()
	}
	pw := width(glyph)
	n := max(BW-4-pw, 5) // text columns inside the box

	var out []string
	if m := ta.dockMenu(); m != nil {
		out = append(out, drawMenu(m, ta.menuIdx, BW)...)
	}

	var text string
	if len(ta.buf) == 0 {
		text = Weak(clip(dockPlaceholder, n-1))
	} else {
		// the end of the line (where you type) always shows; older text scrolls off the left
		var parts []string
		w := 1 // the caret
		i := len(ta.buf)
		for i > 0 {
			s, cw := dispRune(ta.buf[i-1])
			if w+cw > n {
				break
			}
			parts = append([]string{s}, parts...)
			w += cw
			i--
		}
		if i > 0 && len(parts) > 0 {
			parts[0] = "…"
		}
		text = Ink(strings.Join(parts, "")) + Accent("▏")
	}
	row := glyph + text
	out = append(out,
		"  "+hair("┌"+strings.Repeat("─", BW-2)+"┐"),
		"  "+hair("│")+" "+row+strings.Repeat(" ", max(0, BW-4-width(row)))+" "+hair("│"),
		"  "+hair("└"+strings.Repeat("─", BW-2)+"┘"),
	)
	foot := "  " + Weak("enter queues · / commands · @ files · ctrl-c stops Blaze")
	if q := len(ta.queue); q > 0 {
		foot = "  " + Accent(strconv.Itoa(q)+" queued") + Weak(" · Blaze reads "+dockPlural(q, "it", "them")+" next · enter queues another")
	}
	out = append(out, foot)
	for i, l := range out {
		out[i] = clipStyled(l, W-1)
	}
	return out
}

func dockPlural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// eraseLive returns the escape codes that erase the live region and leave the
// cursor where it started. Called with dockMu held.
func eraseLive() string {
	s := "\r"
	if dockDrawn > 1 {
		s = fmt.Sprintf("\x1b[%dA\r", dockDrawn-1)
	}
	dockDrawn = 0
	return s + "\x1b[J"
}

// drawLive redraws the working line and, under it, the composer.
func drawLive(working string) {
	W := termWidth()
	lines := append([]string{working}, dockLines(W)...)
	dockMu.Lock()
	defer dockMu.Unlock()
	Printf("%s%s", eraseLive(), strings.Join(lines, "\r\n"))
	dockDrawn = len(lines)
}

// clearLive erases the working line and the composer.
func clearLive() {
	dockMu.Lock()
	defer dockMu.Unlock()
	Printf("%s", eraseLive())
}

// printOverLive prints lines above the live region (it comes back on the next tick).
func printOverLive(lines []string) {
	dockMu.Lock()
	defer dockMu.Unlock()
	var sb strings.Builder
	sb.WriteString(eraseLive())
	for _, l := range lines {
		sb.WriteString(l + "\r\n")
	}
	Printf("%s", sb.String())
}

// dockKey handles the menu keys in the composer: it reports whether k was used.
// k is "up", "down", "tab", "enter" or "esc". Called with ta.mu held.
func (t *typeahead) dockKey(k string) (used, submit bool) {
	m := t.dockMenu()
	if m == nil {
		return false, false
	}
	n := len(m.Items)
	switch k {
	case "up":
		t.menuIdx = ((t.menuIdx-1)%n + n) % n
	case "down":
		t.menuIdx = (t.menuIdx + 1) % n
	case "esc":
		t.menuOff = string(t.buf)
	case "tab", "enter":
		it := m.Items[t.menuIdx]
		if string(t.buf) == it.Insert && !strings.HasSuffix(it.Insert, " ") {
			return false, false // already exactly this: Enter queues it as typed
		}
		t.buf = []rune(it.Insert)
		t.menuLine, t.menuIdx = "", 0
		return true, k == "enter" && it.Submit
	default:
		return false, false
	}
	return true, false
}
