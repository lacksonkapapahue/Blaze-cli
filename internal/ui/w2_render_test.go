package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withColor runs fn with colour on or off and links as given, restoring both.
func w2Color(t *testing.T, on bool, fn func()) {
	t.Helper()
	old := color
	color = on
	defer func() { color = old }()
	fn()
}

func w2Kinds(toks []codeTok) string {
	var b strings.Builder
	for _, t := range toks {
		b.WriteString("[" + "RKSCN"[t.kind:t.kind+1] + ":" + t.text + "]")
	}
	return b.String()
}

func TestCodeTokens(t *testing.T) {
	cases := []struct {
		lang, in, want string
	}{
		{"go", `func f() int { return 42 } // done`, `[K:func][R: f() int { ][K:return][R: ][N:42][R: } ][C:// done]`},
		{"go", `s := "a\"b"`, `[R:s := ][S:"a\"b"]`},
		{"python", `def f(x): # hi`, `[K:def][R: f(x): ][C:# hi]`},
		{"python", `x = 'it' + 0x1F`, `[R:x = ][S:'it'][R: + ][N:0x1F]`},
		{"js", "const a = `t` // c", "[K:const][R: a = ][S:`t`][R: ][C:// c]"},
		{"ts", `let x: number = 3.5;`, `[K:let][R: x: number = ][N:3.5][R:;]`},
		{"bash", `echo "$HOME" # x`, `[K:echo][R: ][S:"$HOME"][R: ][C:# x]`},
		{"bash", `ls --color=auto file#1`, `[R:ls --color=auto file#][N:1]`},
		{"json", `{"a": true, "n": -1}`, `[R:{][S:"a"][R:: ][K:true][R:, ][S:"n"][R:: -][N:1][R:}]`},
		{"rust", `fn x<'a>(c: char) { let y = 'b'; }`, `[K:fn][R: x<'a>(c: char) { ][K:let][R: y = ][S:'b'][R:; }]`},
		{"weird", `if x == 1 { return "y" } # c`, `[K:if][R: x == ][N:1][R: { ][K:return][R: ][S:"y"][R: } ][C:# c]`},
		{"text", `if x return`, `[R:if x return]`},
		{"go", `x1 := y2`, `[R:x1 := y2]`},
	}
	for _, c := range cases {
		st := &hlState{}
		got := w2Kinds(codeTokens(specFor(c.lang), st, c.in))
		if got != c.want {
			t.Errorf("%s %q:\n got %s\nwant %s", c.lang, c.in, got, c.want)
		}
	}
}

func TestCodeTokensMultiline(t *testing.T) {
	sp := specFor("go")
	st := &hlState{}
	codeTokens(sp, st, "a /* open")
	if !st.inBlock {
		t.Fatal("block comment should stay open")
	}
	if got := w2Kinds(codeTokens(sp, st, "still */ x")); got != "[C:still */][R: x]" {
		t.Errorf("got %s", got)
	}
	st = &hlState{}
	py := specFor("py")
	codeTokens(py, st, `s = """doc`)
	if st.inStr != `"""` {
		t.Fatalf("triple string should stay open, state %+v", st)
	}
	if got := w2Kinds(codeTokens(py, st, `end""" + 1`)); got != `[S:end"""][R: + ][N:1]` {
		t.Errorf("got %s", got)
	}
	st = &hlState{}
	codeTokens(sp, st, "q := `raw")
	if got := w2Kinds(codeTokens(sp, st, "\\n` ok")); got != "[S:\\n`][R: ok]" {
		t.Errorf("go raw string: got %s", got)
	}
}

func TestCodeHLPalette(t *testing.T) {
	w2Color(t, true, func() {
		out := codeHL(specFor("go"), &hlState{}, `return "s" // c`)
		for _, want := range []string{fg(pal.accent) + "return", fg(pal.body) + `"s"`, fg(pal.weak) + "// c"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in %q", want, out)
			}
		}
	})
	w2Color(t, false, func() {
		if out := codeHL(specFor("go"), &hlState{}, `return 1`); out != "return 1" {
			t.Errorf("no colour should be plain, got %q", out)
		}
	})
}

func TestTable(t *testing.T) {
	rows := []string{"| Name | Size |", "|------|-----:|", "| a.go | 12 |", "| `long_name.go` | 3456 |"}
	w2Color(t, false, func() {
		out := renderTable(rows, 80)
		want := []string{
			"  ┌──────────────┬─────────┐",
			"  │ N A M E      │ S I Z E │",
			"  ├──────────────┼─────────┤",
			"  │ a.go         │      12 │",
			"  │ long_name.go │    3456 │",
			"  └──────────────┴─────────┘",
		}
		if strings.Join(out, "\n") != strings.Join(want, "\n") {
			t.Errorf("table:\n%s\nwant:\n%s", strings.Join(out, "\n"), strings.Join(want, "\n"))
		}
		// every grid line is the same width and fits
		narrow := renderTable([]string{"| Column one | Column two |", "|---|---|", "| " + strings.Repeat("x", 60) + " | " + strings.Repeat("y", 60) + " |"}, 40)
		for _, l := range narrow {
			if escWidth(l) > 40 {
				t.Errorf("line too wide (%d): %q", escWidth(l), l)
			}
			if escWidth(l) != escWidth(narrow[0]) {
				t.Errorf("ragged grid: %q", l)
			}
		}
		// far too narrow: rows degrade to · joined lines
		tiny := renderTable(rows, 12)
		if len(tiny) != 3 || !strings.HasPrefix(tiny[0], "  Name · Si") || escWidth(tiny[2]) > 12 {
			t.Errorf("tiny fallback: %q", tiny)
		}
		// no separator row: not a table
		if pl := renderTable([]string{"| a | b |", "| c | d |"}, 80); len(pl) != 2 || strings.Contains(pl[0], "┌") {
			t.Errorf("non-table: %q", pl)
		}
	})
	w2Color(t, true, func() {
		out := renderTable(rows, 80)
		if escWidth(out[1]) != escWidth(out[0]) || escWidth(out[4]) != escWidth(out[0]) {
			t.Errorf("styled widths differ: %d %d %d", escWidth(out[0]), escWidth(out[1]), escWidth(out[4]))
		}
	})
}

func TestTableStreaming(t *testing.T) {
	w2Color(t, false, func() {
		var out []string
		m := &MD{Out: func(s string) { out = append(out, s) }}
		m.Write("Intro\n| a | b |\n|---|")
		m.Write("---|\n| 1 | 2 |\n")
		if len(out) != 1 {
			t.Fatalf("table rows should be held, got %q", out)
		}
		m.Write("after\n")
		if len(out) != 7 || !strings.HasPrefix(out[1], "  ┌") || out[6] != "  after" {
			t.Errorf("got %q", out)
		}
		out = nil
		m.Write("| x | y |\n|---|---|\n| 1 | 2 |")
		m.Flush()
		if len(out) != 5 || !strings.HasPrefix(out[4], "  └") {
			t.Errorf("flush should close the table, got %q", out)
		}
	})
}

func TestCallouts(t *testing.T) {
	cases := []struct {
		in    string
		kind  string
		quote bool
		ok    bool
	}{
		{"> [!NOTE]", "note", true, true},
		{"> [!warning] careful", "warning", true, true},
		{"Note: this is it", "note", false, true},
		{"**Warning:** hot", "warning", false, true},
		{"**Tip**: try x", "tip", false, true},
		{"Notes: nope", "", false, false},
		{"> just a quote", "", false, false},
	}
	for _, c := range cases {
		k, _, q, ok := calloutStart(c.in)
		if k != c.kind || q != c.quote || ok != c.ok {
			t.Errorf("%q: got %q %v %v", c.in, k, q, ok)
		}
	}
	w2Color(t, false, func() {
		got := RenderMD("> [!WARNING]\n> Do not run this.\n> Really.\nAfter.")
		want := []string{"  │ ■ W A R N I N G", "  │ Do not run this.", "  │ Really.", "  After."}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("quote callout:\n%q\nwant\n%q", got, want)
		}
		got = RenderMD("Tip: use the flag\nit is faster\n\nNext para")
		want = []string{"  │ · T I P", "  │ use the flag", "  │ it is faster", "", "  Next para"}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("para callout:\n%q\nwant\n%q", got, want)
		}
		got = RenderMD("Note: a\n- list")
		if len(got) != 3 || got[2] != "  · list" {
			t.Errorf("block start should end a callout: %q", got)
		}
	})
}

func TestHeadings(t *testing.T) {
	w2Color(t, false, func() {
		got := RenderMD("# Big **one**\n## Mid\n### Small\n#notaheading")
		want := []string{"  B I G   O N E", "  ─────────────", "  ■ Mid", "  Small", "  #notaheading"}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("headings:\n%q\nwant\n%q", got, want)
		}
	})
}

func TestWordDiff(t *testing.T) {
	a, b := wordDiff("x := compute(a, b)", "x := compute(a, c)")
	var ca, cb []string
	for _, t := range a {
		if t.changed {
			ca = append(ca, t.text)
		}
	}
	for _, t := range b {
		if t.changed {
			cb = append(cb, t.text)
		}
	}
	if strings.Join(ca, ",") != "b" || strings.Join(cb, ",") != "c" {
		t.Errorf("changed: %q / %q", ca, cb)
	}
	if a, _ := wordDiff("completely different", "nothing alike here at all"); a != nil {
		t.Errorf("unrelated lines should not be word-marked")
	}
	w2Color(t, true, func() {
		out := strings.Join(diffRender("nofile.go", "a\nreturn old + 1\nz\n", "a\nreturn new + 1\nz\n", 80), "\n")
		if !strings.Contains(out, "\x1b[1;4m"+fg(pal.accent)+"new") {
			t.Errorf("added word not highlighted: %q", out)
		}
		if !strings.Contains(out, "\x1b[1;4m"+fg(red)+"old") {
			t.Errorf("removed word not highlighted: %q", out)
		}
	})
	w2Color(t, false, func() {
		out := diffRender("p.go", "a\nb\n", "a\nc\nd\n", 80)
		if out[0] != "  ■ Updated p.go  +2 −1" {
			t.Errorf("header %q", out[0])
		}
		if out[1] != "    1 │   a" || out[2] != "    2 │ - b " || out[3] != "    2 │ + c " {
			t.Errorf("gutter: %q", out)
		}
	})
}

func TestLinks(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "main.go")
	os.WriteFile(f, []byte("package main\n"), 0o644)
	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)
	os.Unsetenv("BLAZE_NO_LINKS")

	w2Color(t, true, func() {
		out := inline("see [docs](https://x.dev/a) and https://y.io/p?q=1. Also main.go:12 and gone.go:3")
		if !strings.Contains(out, "\x1b]8;;https://x.dev/a\x1b\\") {
			t.Errorf("md link not wrapped: %q", out)
		}
		if !strings.Contains(out, "\x1b]8;;https://y.io/p?q=1\x1b\\") {
			t.Errorf("bare url (minus trailing dot) not wrapped: %q", out)
		}
		abs, _ := filepath.Abs("main.go")
		if !strings.Contains(out, "\x1b]8;;file://"+filepath.ToSlash(abs)) && !strings.Contains(out, "\x1b]8;;file:///"+filepath.ToSlash(abs)) {
			t.Errorf("file path not linked: %q", out)
		}
		if strings.Count(out, "\x1b]8;;\x1b\\") != 3 {
			t.Errorf("want 3 links (missing file not linked): %q", out)
		}
		if escWidth(out) != strWidth("see docs and https://y.io/p?q=1. Also main.go:12 and gone.go:3") {
			t.Errorf("escWidth counts escapes: %d", escWidth(out))
		}
		code := inline("open `main.go:4`")
		if !strings.Contains(code, "\x1b]8;;file://") {
			t.Errorf("path in code span not linked: %q", code)
		}
		os.Setenv("BLAZE_NO_LINKS", "1")
		off := inline("[docs](https://x.dev/a)")
		os.Unsetenv("BLAZE_NO_LINKS")
		if strings.Contains(off, "\x1b]8") || !strings.Contains(stripEsc(off), "docs (https://x.dev/a)") {
			t.Errorf("BLAZE_NO_LINKS: %q", off)
		}
	})
	w2Color(t, false, func() {
		if out := inline("[docs](https://x.dev/a) main.go:1"); out != "docs (https://x.dev/a) main.go:1" {
			t.Errorf("no colour: %q", out)
		}
	})
}

func TestCodeBlockRender(t *testing.T) {
	w2Color(t, false, func() {
		got := RenderMD("```go title\nfunc x() {}\n```")
		want := []string{"  ┌─ G O", "  │ func x() {}", "  └─"}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%q", got)
		}
	})
}

func TestPreOnlyWhenPrinting(t *testing.T) {
	pre, post := 0, 0
	m := &MD{Out: func(string) {}, Pre: func() { pre++ }, Post: func() { post++ }}
	m.Write("| a | b |\n")
	if pre != 0 || post != 0 {
		t.Errorf("held table row should not stop the spinner: pre %d post %d", pre, post)
	}
	m.Write("|---|---|\n\n")
	if pre != 1 || post != 1 {
		t.Errorf("pre %d post %d", pre, post)
	}
}
