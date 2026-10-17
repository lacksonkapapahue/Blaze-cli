package ui

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func stripped(s string) string { return ansi.ReplaceAllString(s, "") }

func TestMatchSpots(t *testing.T) {
	cases := []struct {
		name, q, want string // want marks matched runes with ^
	}{
		{"/skills", "sk", " ^^    "},
		{"/skills", "SK", " ^^    "},
		{"rewind", "wd", "  ^  ^"},
		{"compact", "pact", "   ^^^^"},
		{"model", "zz", ""},
		{"model", "", ""},
	}
	for _, c := range cases {
		sp := matchSpots(c.name, c.q)
		got := ""
		{
			for _, b := range sp {
				if b {
					got += "^"
				} else {
					got += " "
				}
			}
		}
		if got != c.want {
			t.Errorf("matchSpots(%q,%q) = %q, want %q", c.name, c.q, got, c.want)
		}
	}
}

func TestHighlightKeepsTextAndAccentsMatch(t *testing.T) {
	withColor(t)
	h := highlight("/skills", "kil", Body, false)
	if stripped(h) != "/skills" {
		t.Fatalf("text changed: %q", stripped(h))
	}
	if !strings.Contains(h, Accent("kil")) {
		t.Fatalf("match not in the accent: %q", h)
	}
	if highlight("abc", "zz", Body, false) != Body("abc") {
		t.Fatal("no match should be plain")
	}
}

func TestMenuQuery(t *testing.T) {
	for line, want := range map[string]string{
		"/sk": "sk", "/": "", "/skills on fo": "fo", "look at @src/ma": "ma", "@": "",
	} {
		if got := menuQuery(line); got != want {
			t.Errorf("menuQuery(%q) = %q, want %q", line, got, want)
		}
	}
}

func cmdMenu(names ...string) *MenuSet {
	var items []MenuItem
	for _, n := range names {
		items = append(items, MenuItem{Name: n, Insert: n, Submit: true})
	}
	return &MenuSet{Title: "commands", Items: items, Full: len(items)}
}

func TestRecentRanksFirstForShortQueries(t *testing.T) {
	m := cmdMenu("/agents", "/compact", "/help", "/model", "/rewind")
	recent := []string{"/rewind", "/model", "/gone"}
	got := withRecent("/", m, recent)
	var order []string
	for _, it := range got.Items {
		order = append(order, it.Name+":"+it.Section)
	}
	want := "/rewind:recent /model:recent /agents:commands /compact:commands /help:commands"
	if strings.Join(order, " ") != want {
		t.Fatalf("order = %v", order)
	}
	if m.Items[0].Name != "/agents" || m.Items[0].Section != "" {
		t.Fatal("withRecent changed the host's menu")
	}
	// longer queries keep the host's ranking
	if got := withRecent("/mo", cmdMenu("/model"), recent); got.Items[0].Section != "" || got.Query != "mo" {
		t.Fatalf("long query: %+v", got)
	}
	// sub-menus are left alone
	if got := withRecent("/skills ", cmdMenu("/rewind"), recent); got.Items[0].Section != "" {
		t.Fatal("recent applied to a sub-menu")
	}
}

func TestRecentPersists(t *testing.T) {
	old := RecentFile
	t.Cleanup(func() { RecentFile = old; recentInit = false })
	RecentFile = filepath.Join(t.TempDir(), "recent")
	recentInit = false
	RecordRecent("/model gpt")
	RecordRecent("hello there")
	RecordRecent("/rewind")
	RecordRecent("/model")
	if got := strings.Join(Recent(), " "); got != "/model /rewind" {
		t.Fatalf("recent = %q", got)
	}
	b, _ := os.ReadFile(RecentFile)
	if string(b) != "/model\n/rewind\n" {
		t.Fatalf("file = %q", b)
	}
	recentInit = false
	if got := strings.Join(Recent(), " "); got != "/model /rewind" {
		t.Fatalf("reloaded = %q", got)
	}
	l := []string{}
	for _, c := range []string{"/a", "/b", "/c", "/d"} {
		l = pushRecent(l, c, 3)
	}
	if strings.Join(l, " ") != "/d /c /b" {
		t.Fatalf("cap: %v", l)
	}
}

func TestDrawMenuSectionsAndWidth(t *testing.T) {
	withColor(t)
	m := withRecent("/", cmdMenu("/agents", "/compact", "/help", "/model", "/rewind"), []string{"/help"})
	lines := drawMenu(m, 0, 60)
	out := plain(lines)
	if !strings.Contains(out, "R E C E N T") || !strings.Contains(out, "C O M M A N D S") {
		t.Fatalf("no sections:\n%s", out)
	}
	if !strings.Contains(out, "▸ /help") {
		t.Fatalf("selection:\n%s", out)
	}
	for _, l := range lines {
		if width(l) != 62 {
			t.Errorf("row %d wide: %q", width(l), stripped(l))
		}
	}
	// selection deep in the list stays visible
	lines = drawMenu(withRecent("/", cmdMenu("/a", "/b", "/c", "/d", "/e", "/f", "/g", "/h", "/i", "/j"), []string{"/c"}), 9, 60)
	if !strings.Contains(plain(lines), "▸ /j") {
		t.Fatalf("scrolled:\n%s", plain(lines))
	}
}

func TestGaugeText(t *testing.T) {
	old := color
	color = false
	t.Cleanup(func() { color = old })
	if g := gaugeText(41, 100, false); g != "ctx ━━━───── 41%" {
		t.Fatalf("41%%: %q", g)
	}
	if g := gaugeText(0, 100, false); g != "ctx ──────── 0%" {
		t.Fatalf("0%%: %q", g)
	}
	if g := gaugeText(1, 1000, false); g != "ctx ━─────── 0%" && g != "ctx ──────── 0%" {
		t.Fatalf("tiny: %q", g)
	}
	if g := gaugeText(90, 100, false); g != "ctx ━━━━━━━─ 90% · /compact" {
		t.Fatalf("hot: %q", g)
	}
	if g := gaugeText(500, 100, false); !strings.Contains(g, "100%") {
		t.Fatalf("clamp: %q", g)
	}
	if g := gaugeText(41, 100, true); g != "ctx 41%" {
		t.Fatalf("compact: %q", g)
	}
	if gaugeText(1, 0, false) != "" {
		t.Fatal("no window, no gauge")
	}
	color = true
	if g := gaugeText(90, 100, false); !strings.Contains(g, bold()) {
		t.Fatal("hot gauge is not bold")
	}
}

func TestWithGauge(t *testing.T) {
	g := "ctx 41%"
	if got := withGauge("  build", g, 40); width(got) != 40 || !strings.HasSuffix(got, g) || !strings.HasPrefix(got, "  build") {
		t.Fatalf("%q", got)
	}
	long := "  " + strings.Repeat("x", 80)
	if got := withGauge(long, g, 50); width(got) > 50 || !strings.HasSuffix(got, g) {
		t.Fatalf("clipped: %q", got)
	}
	if got := withGauge(long, g, 25); strings.Contains(got, "ctx") || width(got) > 25 {
		t.Fatalf("narrow keeps the footer: %q", got)
	}
	if got := withGauge("", g, 30); width(got) != 30 {
		t.Fatalf("alone: %q", got)
	}
	if got := withGauge("abc", "", 30); got != "abc" {
		t.Fatalf("no gauge: %q", got)
	}
}

func TestTopBorderKeepsWidth(t *testing.T) {
	withColor(t)
	for _, bw := range []int{10, 16, 20, 30, 77, 110} {
		for _, l := range []string{"", "build · auto", "plan", "a very long mode label that will never fit · auto"} {
			b := topBorder(bw, l)
			if width(b) != bw {
				t.Errorf("bw %d label %q: width %d %q", bw, l, width(b), stripped(b))
			}
		}
	}
	if got := stripped(topBorder(40, "build · auto")); !strings.HasPrefix(got, "┌─ B U I L D · A U T O ─") || !strings.HasSuffix(got, "┐") {
		t.Fatalf("label: %q", got)
	}
	if got := stripped(topBorder(20, "build · auto")); !strings.HasPrefix(got, "┌─ BUILD · AUTO") {
		t.Fatalf("compact: %q", got)
	}
}

func TestPlaceholderRotates(t *testing.T) {
	tips := []string{"a", "b", "c"}
	var got []string
	for n := 0; n < 5; n++ {
		got = append(got, placeholderFor("first", n, tips))
	}
	if strings.Join(got, " ") != "first a b c a" {
		t.Fatalf("%v", got)
	}
	if placeholderFor("", 0, tips) != "a" || placeholderFor("x", 3, nil) != "x" {
		t.Fatal("edges")
	}
}

func TestPasteChip(t *testing.T) {
	big := "l1\nl2\nl3\nl4\nl5\nl6\nl7"
	buf, pos := []rune("hi "), 3
	buf, spans := insertPaste(buf, &pos, nil, big)
	if string(buf) != "hi "+big || len(spans) != 1 || spans[0].lines != 7 || pos != len(buf) {
		t.Fatalf("insert: %q %+v %d", string(buf), spans, pos)
	}
	if got := collapsed(buf, spans); got != "hi [pasted 7 lines]" {
		t.Fatalf("collapsed: %q", got)
	}
	// small pastes stay text
	b2, p2 := []rune(""), 0
	if _, s2 := insertPaste(b2, &p2, nil, "a\nb"); len(s2) != 0 {
		t.Fatal("small paste chipped")
	}
	// typing after the chip keeps it; typing before shifts it
	old := append([]rune(nil), buf...)
	buf = append(buf, 'x')
	spans = syncSpans(old, buf, spans, len(old))
	if len(spans) != 1 || spans[0].at != 3 {
		t.Fatalf("after: %+v", spans)
	}
	old = append([]rune(nil), buf...)
	buf = append([]rune("l"), buf...)
	spans = syncSpans(old, buf, spans, 0)
	if len(spans) != 1 || spans[0].at != 4 {
		t.Fatalf("before: %+v", spans)
	}
	// left jumps over it, backspace at its end deletes it whole
	pos = 4 + len([]rune(big))
	nb, sp, ok := chipKey("left", buf, &pos, spans)
	if !ok || pos != 4 {
		t.Fatalf("left: %d", pos)
	}
	nb, sp, ok = chipKey("right", nb, &pos, sp)
	if !ok || pos != 4+len([]rune(big)) {
		t.Fatalf("right: %d", pos)
	}
	nb, sp, ok = chipKey("backspace", nb, &pos, sp)
	if !ok || string(nb) != "lhi x" || len(sp) != 0 || pos != 4 {
		t.Fatalf("backspace: %q %+v %d", string(nb), sp, pos)
	}
	// an edit inside a chip turns it back into text
	buf, pos = nil, 0
	buf, spans = insertPaste(buf, &pos, nil, big)
	old = append([]rune(nil), buf...)
	buf = append(append(append([]rune(nil), buf[:4]...), 'Z'), buf[4:]...)
	if s := syncSpans(old, buf, spans, 4); len(s) != 0 {
		t.Fatalf("edit inside kept: %+v", s)
	}
	if outOfChip(3, spans) != len([]rune(big)) || outOfChip(0, spans) != 0 {
		t.Fatal("outOfChip")
	}
}

func TestEditorShowsChipAndSubmitsFullText(t *testing.T) {
	withColor(t)
	e := &Editor{}
	e.buf, e.pastes = insertPaste(nil, &e.pos, nil, "a\nb\nc\nd\ne\nf")
	e.spanBuf, e.spanPos = append([]rune(nil), e.buf...), e.pos
	if !e.chip("backspace") || len(e.buf) != 0 {
		t.Fatalf("backspace left %q", string(e.buf))
	}
}

func TestDockChip(t *testing.T) {
	resetTA(t)
	withColor(t)
	ta.on = true
	ta.mu.Lock()
	ta.paste("one\ntwo\nthree\nfour\nfive\nsix")
	ta.mu.Unlock()
	out := plain(dockLines(80))
	if !strings.Contains(out, "[pasted 6 lines]▏") {
		t.Fatalf("dock chip:\n%s", out)
	}
	ta.typeKey(127)
	if len(ta.buf) != 0 {
		t.Fatalf("backspace left %q", string(ta.buf))
	}
	ta.mu.Lock()
	ta.paste("one\ntwo\nthree\nfour\nfive\nsix")
	ta.mu.Unlock()
	ta.typeKey('!')
	ta.typeKey('\r')
	if s, ok := NextQueued(); !ok || s != "one\ntwo\nthree\nfour\nfive\nsix!" {
		t.Fatalf("sent %q", s)
	}
}

func TestDockModeLabelAndGauge(t *testing.T) {
	resetTA(t)
	withColor(t)
	ModeLabel = func() string { return "plan" }
	ContextUsage = func() (int, int) { return 50, 100 }
	t.Cleanup(func() { ModeLabel = nil; ContextUsage = nil })
	ta.on = true
	lines := dockLines(80)
	out := plain(lines)
	if !strings.Contains(out, "┌─ P L A N ─") || !strings.Contains(out, "ctx ━━━━──── 50%") {
		t.Fatalf("dock:\n%s", out)
	}
	for _, l := range lines {
		if width(l) > 79 {
			t.Errorf("too wide: %q", stripped(l))
		}
	}
}

func TestEditorRenderBoxWithLabelGaugeAndChip(t *testing.T) {
	withColor(t)
	var sb strings.Builder
	oldOut := Out
	Out = bufio.NewWriter(&sb)
	ModeLabel = func() string { return "build · auto" }
	ContextUsage = func() (int, int) { return 90, 100 }
	t.Cleanup(func() { Out = oldOut; ModeLabel = nil; ContextUsage = nil })
	e := &Editor{Boxed: true, Footer: func(string) string { return "  build · auto" }}
	e.insert([]rune("see "))
	e.paste2("1\n2\n3\n4\n5\n6\n7\n8")
	e.render(false)
	out := stripped(sb.String())
	for _, want := range []string{"┌─ B U I L D · A U T O ─", "see [pasted 8 lines]", "ctx ━━━━━━━─ 90% · /compact"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%q", want, out)
		}
	}
	// top border, one text row, bottom border, footer: the cursor goes up 2 to the text row
	if !strings.Contains(sb.String(), "\x1b[2A\r") || e.curRow != 1 {
		t.Fatalf("cursor bookkeeping: curRow %d\n%q", e.curRow, sb.String())
	}
	for _, l := range strings.Split(out, "\r\n") {
		l = strings.Trim(regexpCSI.ReplaceAllString(l, ""), "\r")
		if w := len([]rune(l)); w > 79 {
			t.Errorf("row %d wide: %q", w, l)
		}
	}
	// the scrollback record collapses the paste, the buffer keeps it all
	if collapsed(e.buf, e.pastes) != "see [pasted 8 lines]" || !strings.HasSuffix(string(e.buf), "7\n8") {
		t.Fatal("record / buffer")
	}
}

var regexpCSI = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// paste2 is Editor.paste with the text given (no keyboard).
func (e *Editor) paste2(text string) {
	e.fixSpans()
	e.buf, e.pastes = insertPaste(e.buf, &e.pos, e.pastes, text)
	e.spanBuf, e.spanPos = append([]rune(nil), e.buf...), e.pos
}
