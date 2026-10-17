package ui

import (
	"strings"
	"testing"
)

func TestWrapText(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want []string
	}{
		{"hello world", 20, []string{"hello world"}},
		{"hello world", 5, []string{"hello", "world"}},
		{"aaaaaaaaaa", 4, []string{"aaaa", "aaaa", "aa"}},
		{"one\n\ntwo", 10, []string{"one", "", "two"}},
		{"a bb ccc dddd", 6, []string{"a bb", "ccc", "dddd"}},
		{"", 10, []string{""}},
		{"日本語テキスト", 6, []string{"日本語", "テキス", "ト"}},
	}
	for _, c := range cases {
		got := wrapText(c.in, c.n)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("wrapText(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		for _, l := range got {
			if strWidth(l) > c.n {
				t.Errorf("wrapText(%q, %d): line %q is %d wide", c.in, c.n, l, strWidth(l))
			}
		}
	}
}

func TestUserLinesRightAligned(t *testing.T) {
	old := color
	color = true
	defer func() { color = old }()
	W := termWidth()
	if W < 30 {
		t.Skip("terminal too narrow")
	}
	msg := strings.Repeat("word ", 60)
	lines := UserLines(msg, "sent while Blaze worked")
	if len(lines) < 3 {
		t.Fatalf("expected the long message to wrap, got %d lines", len(lines))
	}
	text := lines[:len(lines)-1] // the last line is the note
	start, widest := -1, 0
	for i, l := range text {
		plain := ansi.ReplaceAllString(l, "")
		lead := len(plain) - len(strings.TrimLeft(plain, " "))
		if i > 0 {
			lead -= 2 // continuation lines carry two spaces where line 1 has "‹ "
		}
		if start >= 0 && lead != start {
			t.Errorf("line %d starts at column %d, want %d (block not aligned): %q", i, lead, start, plain)
		}
		start = lead
		if lead < W/4 {
			t.Errorf("line %d isn't on the right: starts at column %d", i, lead)
		}
		widest = max(widest, strWidth(plain))
	}
	if widest != W-1 {
		t.Errorf("widest line ends at column %d, want %d", widest, W-1)
	}
	if p := ansi.ReplaceAllString(lines[0], ""); !strings.Contains(p, "‹ word") {
		t.Errorf("first line lacks the ‹ mark: %q", p)
	}
	if p := ansi.ReplaceAllString(lines[len(lines)-1], ""); !strings.HasSuffix(p, "· sent while Blaze worked") {
		t.Errorf("note line wrong: %q", p)
	}
}

func TestUserLinesPlainFallback(t *testing.T) {
	old := color
	color = false
	defer func() { color = old }()
	got := UserLines("hi there", "")
	if len(got) != 1 || !strings.Contains(ansi.ReplaceAllString(got[0], ""), "› hi there") {
		t.Errorf("plain fallback = %q", got)
	}
}
