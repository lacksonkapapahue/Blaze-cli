package ui

import (
	"strings"
	"testing"
)

func withColor(t *testing.T) {
	t.Helper()
	old := color
	color = true
	t.Cleanup(func() { color = old; DockMenu = nil; DockGlyph = nil })
}

func plain(lines []string) string { return ansi.ReplaceAllString(strings.Join(lines, "\n"), "") }

func TestDockDrawsTheComposerBox(t *testing.T) {
	resetTA(t)
	withColor(t)
	if dockLines(80) != nil {
		t.Fatal("a composer outside a turn")
	}
	ta.on = true
	out := plain(dockLines(80))
	if !strings.Contains(out, "┌") || !strings.Contains(out, "└") || !strings.Contains(out, "Type your next message") {
		t.Fatalf("empty composer:\n%s", out)
	}
	for _, r := range "fix the tests" {
		ta.key(r)
	}
	ta.key('\r')
	for _, r := range "then commit" {
		ta.key(r)
	}
	out = plain(dockLines(80))
	if !strings.Contains(out, "› then commit▏") || !strings.Contains(out, "1 queued") {
		t.Fatalf("composer:\n%s", out)
	}
	if typeaheadTail() != "" {
		t.Error("the working line still carries the one-line composer")
	}
	for _, l := range dockLines(80) {
		if width(l) > 79 {
			t.Errorf("line wider than the terminal: %q", l)
		}
	}
	// long lines keep their end (where you type) in view
	for range 200 {
		ta.key('x')
	}
	if out := plain(dockLines(60)); !strings.Contains(out, "…") || !strings.Contains(out, "x▏") {
		t.Fatalf("long line:\n%s", out)
	}
}

func TestDockMenuPicksLikeThePrompt(t *testing.T) {
	resetTA(t)
	withColor(t)
	DockMenu = func(line string) *MenuSet {
		if !strings.HasSuffix(line, "@") && !strings.Contains(line, "@s") {
			return nil
		}
		pre := line[:strings.LastIndex(line, "@")]
		items := []MenuItem{
			{Name: "main.go", Insert: pre + "@main.go "},
			{Name: "src/", Insert: pre + "@src/"},
		}
		return &MenuSet{Title: "files", Items: Rank(items, line[strings.LastIndex(line, "@")+1:], nil), Full: 2}
	}
	ta.on = true
	for _, r := range "read @" {
		ta.key(r)
	}
	if out := plain(dockLines(80)); !strings.Contains(out, "F I L E S") || !strings.Contains(out, "▸ main.go") {
		t.Fatalf("menu:\n%s", out)
	}
	ta.mu.Lock()
	ta.dockKey("down")
	ta.mu.Unlock()
	ta.key('\t')
	if got := string(ta.buf); got != "read @src/" {
		t.Fatalf("tab filled %q", got)
	}
	ta.buf = []rune("read @")
	ta.menuLine = ""
	if _, queued := ta.key('\r'); queued {
		t.Fatal("Enter on the menu queued the line instead of filling it in")
	}
	if got := string(ta.buf); got != "read @main.go " {
		t.Fatalf("enter filled %q", got)
	}
	if line, queued := ta.key('\r'); !queued || line != "read @main.go" {
		t.Fatalf("second Enter queued %q, %v", line, queued)
	}
	// Esc closes the menu for this line
	for _, r := range "@" {
		ta.key(r)
	}
	ta.mu.Lock()
	ta.dockKey("esc")
	m := ta.dockMenu()
	ta.mu.Unlock()
	if m != nil {
		t.Fatal("Esc didn't close the menu")
	}
}
