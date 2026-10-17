package ui

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clipboard "github.com/lacksonkapapahue/blaze-cli/internal/clip"
)

func fakeClip(t *testing.T, img string, imgErr error, text string, textErr error) {
	t.Helper()
	oi, ot := ClipImage, ClipText
	ClipImage = func(context.Context) (string, error) { return img, imgErr }
	ClipText = func(context.Context) (string, error) { return text, textErr }
	t.Cleanup(func() { ClipImage, ClipText = oi, ot })
}

func quietOut(t *testing.T) *strings.Builder {
	t.Helper()
	var sb strings.Builder
	old := Out
	Out = bufio.NewWriter(&sb)
	t.Cleanup(func() { Out = old })
	return &sb
}

func TestImageChipInEditor(t *testing.T) {
	withColor(t)
	quietOut(t)
	fakeClip(t, "/tmp/blaze-clip-1.png", nil, "", nil)
	e := &Editor{}
	e.insert([]rune("look"))
	e.key(22, nil) // Ctrl+V
	e.insert([]rune("and"))
	e.key(22, nil)
	if got := string(e.buf); got != "look @image:/tmp/blaze-clip-1.png and @image:/tmp/blaze-clip-1.png " {
		t.Fatalf("buffer %q", got)
	}
	e.fixSpans()
	if got := collapsed(e.buf, e.pastes); got != "look [image 1] and [image 2] " {
		t.Fatalf("shown %q", got)
	}
	// ← from after the second chip's space skips the chip whole; backspace deletes it whole
	e.pos = len(e.buf) - 1
	if !e.chip("backspace") {
		t.Fatal("chip not deleted")
	}
	e.fixSpans()
	if got := collapsed(e.buf, e.pastes); got != "look [image 1] and  " {
		t.Fatalf("after delete %q", got)
	}
	e.pos = 5
	if !e.chip("right") || e.pos != 5+len([]rune("@image:/tmp/blaze-clip-1.png")) {
		t.Fatalf("right skipped to %d", e.pos)
	}
}

func TestCtrlVTextFallback(t *testing.T) {
	withColor(t)
	quietOut(t)
	fakeClip(t, "", clipboard.ErrNoImage, "1\n2\n3\n4\n5\n6\n", nil)
	e := &Editor{}
	e.key(22, nil)
	e.fixSpans()
	if collapsed(e.buf, e.pastes) != "[pasted 6 lines]" || string(e.buf) != "1\n2\n3\n4\n5\n6" {
		t.Fatalf("%q / %q", collapsed(e.buf, e.pastes), string(e.buf))
	}
	fakeClip(t, "", clipboard.ErrNoImage, "short", nil)
	e = &Editor{}
	e.key(22, nil)
	if string(e.buf) != "short" || len(e.pastes) != 0 {
		t.Fatalf("%q %v", string(e.buf), e.pastes)
	}
}

func TestCtrlVHints(t *testing.T) {
	withColor(t)
	sb := quietOut(t)
	fakeClip(t, "", &clipboard.NoToolError{Hint: "Can't read the clipboard: install xclip"}, "", nil)
	e := &Editor{}
	e.key(22, nil)
	if len(e.buf) != 0 || !strings.Contains(sb.String(), "install xclip") {
		t.Fatalf("%q", sb.String())
	}
	fakeClip(t, "", clipboard.ErrNoImage, "", clipboard.ErrEmpty)
	if c := readClipboard(false); c.hint != "The clipboard is empty" {
		t.Fatalf("%+v", c)
	}
	// an empty bracketed paste only looks for an image: no text, no hint
	fakeClip(t, "", clipboard.ErrNoImage, "should not paste", nil)
	if c := readClipboard(true); c != (clipResult{}) {
		t.Fatalf("%+v", c)
	}
}

func TestDraggedImagePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "My Shot.png")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{p, "'" + p + "'", `"` + p + `"`, "file://" + strings.ReplaceAll(p, " ", "%20") + "\n"} {
		if got, ok := imagePath(in); !ok || got != p {
			t.Errorf("imagePath(%q) = %q %v", in, got, ok)
		}
	}
	if os.PathSeparator == '/' {
		if got, ok := imagePath(strings.ReplaceAll(p, " ", `\ `)); !ok || got != p {
			t.Errorf("escaped: %q %v", got, ok)
		}
	}
	for _, in := range []string{"hello", filepath.Join(dir, "missing.png"), "shot.png", p + "\nmore", filepath.Join(dir, "a.txt")} {
		if _, ok := imagePath(in); ok {
			t.Errorf("imagePath(%q) took text as an image", in)
		}
	}
	var buf []rune
	pos := 0
	buf, spans := pasteInsert(buf, &pos, nil, "'"+p+"'")
	if collapsed(buf, spans) != "[image 1] " || !strings.Contains(string(buf), `@image:"`+p+`"`) {
		t.Fatalf("%q / %q", collapsed(buf, spans), string(buf))
	}
}

func TestDockCtrlV(t *testing.T) {
	resetTA(t)
	withColor(t)
	fakeClip(t, "/tmp/x.png", nil, "", nil)
	ta.on = true
	ta.typeKey('h')
	ta.typeKey('i')
	ta.clipPaste(false)
	out := plain(dockLines(80))
	if !strings.Contains(out, "hi [image 1] ▏") {
		t.Fatalf("dock:\n%s", out)
	}
	ta.typeKey('\r')
	if s, ok := NextQueued(); !ok || s != "hi @image:/tmp/x.png" {
		t.Fatalf("sent %q", s)
	}
}

func TestReceipt(t *testing.T) {
	withColor(t)
	got := stripped(Receipt([]ReceiptItem{{"@diff 214 lines", true}, {"@url example.com 3K", true}, {"@git not a git repository", false}}))
	if got != "  ■ @diff 214 lines · @url example.com 3K · □ @git not a git repository" {
		t.Fatalf("%q", got)
	}
	if Receipt(nil) != "" {
		t.Fatal("empty receipt")
	}
}

func TestMenuDescAccent(t *testing.T) {
	withColor(t)
	m := &MenuSet{Title: "context", Items: []MenuItem{{Name: "@diff", Desc: "Git working tree diff", DescAccent: true, Insert: "@diff "}, {Name: "@staged", Desc: "Git staged diff", DescAccent: true}}}
	lines := drawMenu(m, 0, 60)
	if !strings.Contains(lines[2], Accent("Git staged diff")) {
		t.Fatalf("%q", lines[2])
	}
}
