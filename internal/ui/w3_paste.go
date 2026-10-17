package ui

// v0.12.0 big pastes (W3 #18): a bracketed paste of more than pasteChipLines lines
// shows in the composer as one chip, [pasted 42 lines], while the buffer keeps the
// whole text (that is what gets sent). The chip moves and deletes as one character.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	clipboard "github.com/lacksonkapapahue/blaze-cli/internal/clip"
	"github.com/lacksonkapapahue/blaze-cli/internal/refs"
)

const pasteChipLines = 5 // pastes with more lines than this collapse into a chip

// pasteSpan is a collapsed paste: n runes of the buffer starting at at. An image
// chip (image) holds an "@image:PATH" reference and shows as [image N].
type pasteSpan struct {
	at, n, lines int
	image        bool
}

// spanLabel is what span j shows: [pasted 42 lines], or [image N] numbered by
// the image chips before it (the same numbers the sent message uses).
func spanLabel(spans []pasteSpan, j int) string {
	s := spans[j]
	if !s.image {
		return chipLabel(s.lines)
	}
	k := 1
	for _, o := range spans {
		if o.image && o.at < s.at {
			k++
		}
	}
	return "[image " + strconv.Itoa(k) + "]"
}

// pasteLines counts the lines of a paste.
func pasteLines(s string) int { return strings.Count(s, "\n") + 1 }

// chipLabel is what a collapsed paste shows: "[pasted 42 lines]".
func chipLabel(lines int) string { return "[pasted " + strconv.Itoa(lines) + " lines]" }

// cleanPaste normalises a pasted text's line ends and drops trailing newlines.
func cleanPaste(b []rune) string {
	s := strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r", "\n")
	return strings.TrimRight(s, "\n")
}

// readPaste reads a bracketed paste body from In up to the end mark (ESC [201~).
func readPaste() string {
	var b []rune
	const endSeq = "\x1b[201~"
	for {
		r, _, err := In.ReadRune()
		if err != nil {
			break
		}
		b = append(b, r)
		if len(b) >= len(endSeq) && string(b[len(b)-len(endSeq):]) == endSeq {
			b = b[:len(b)-len(endSeq)]
			break
		}
	}
	return cleanPaste(b)
}

// insertPaste puts text at *pos in buf, shifting the spans after it, and adds a
// span when the paste is big. It returns the new buffer and spans.
func insertPaste(buf []rune, pos *int, spans []pasteSpan, text string) ([]rune, []pasteSpan) {
	rs := []rune(text)
	if len(rs) == 0 {
		return buf, spans
	}
	p := *pos
	var out []pasteSpan
	for _, s := range spans {
		switch {
		case s.at >= p:
			s.at += len(rs)
		case s.at+s.n > p: // pasting inside a chip (shouldn't happen): it's text now
			continue
		}
		out = append(out, s)
	}
	nb := make([]rune, 0, len(buf)+len(rs))
	nb = append(nb, buf[:p]...)
	nb = append(nb, rs...)
	nb = append(nb, buf[p:]...)
	if l := pasteLines(text); l > pasteChipLines {
		out = append(out, pasteSpan{at: p, n: len(rs), lines: l})
	}
	*pos = p + len(rs)
	return nb, out
}

// syncSpans keeps spans pointing at the same text after an edit turned old into
// nu: spans before the changed region stay, spans after it shift, spans it
// touched are dropped (they became plain text or were deleted). The edit is
// taken to start at or before caret (the caret before the edit), which settles
// which copy of a repeated character was typed.
func syncSpans(old, nu []rune, spans []pasteSpan, caret int) []pasteSpan {
	if len(spans) == 0 {
		return nil
	}
	if string(old) == string(nu) {
		return spans
	}
	p := 0
	for p < len(old) && p < len(nu) && p < caret && old[p] == nu[p] {
		p++
	}
	s := 0
	for s < len(old)-p && s < len(nu)-p && old[len(old)-1-s] == nu[len(nu)-1-s] {
		s++
	}
	oldEnd := len(old) - s // the edit replaced old[p:oldEnd]
	d := len(nu) - len(old)
	var out []pasteSpan
	for _, sp := range spans {
		switch {
		case sp.at+sp.n <= p:
			out = append(out, sp)
		case sp.at >= oldEnd:
			sp.at += d
			out = append(out, sp)
		}
	}
	return out
}

// spanEndingAt / spanStartingAt find the chip just before / after the caret.
func spanEndingAt(spans []pasteSpan, pos int) int {
	for i, s := range spans {
		if s.at+s.n == pos {
			return i
		}
	}
	return -1
}

func spanStartingAt(spans []pasteSpan, pos int) int {
	for i, s := range spans {
		if s.at == pos {
			return i
		}
	}
	return -1
}

// chipKey applies a chip-aware edit for key k ("backspace", "del", "left",
// "right") at *pos. It reports whether it acted (the caller skips its own edit).
func chipKey(k string, buf []rune, pos *int, spans []pasteSpan) ([]rune, []pasteSpan, bool) {
	cut := func(i int) ([]rune, []pasteSpan, bool) {
		s := spans[i]
		nb := append(append([]rune(nil), buf[:s.at]...), buf[s.at+s.n:]...)
		var out []pasteSpan
		for j, o := range spans {
			if j == i {
				continue
			}
			if o.at > s.at {
				o.at -= s.n
			}
			out = append(out, o)
		}
		*pos = s.at
		return nb, out, true
	}
	switch k {
	case "backspace":
		if i := spanEndingAt(spans, *pos); i >= 0 {
			return cut(i)
		}
	case "del":
		if i := spanStartingAt(spans, *pos); i >= 0 {
			return cut(i)
		}
	case "left":
		if i := spanEndingAt(spans, *pos); i >= 0 {
			*pos = spans[i].at
			return buf, spans, true
		}
	case "right":
		if i := spanStartingAt(spans, *pos); i >= 0 {
			*pos = spans[i].at + spans[i].n
			return buf, spans, true
		}
	}
	return buf, spans, false
}

// outOfChip moves a caret that landed inside a chip (a word jump) to its end.
func outOfChip(pos int, spans []pasteSpan) int {
	for _, s := range spans {
		if pos > s.at && pos < s.at+s.n {
			return s.at + s.n
		}
	}
	return pos
}

// collapsed is buf as shown, with each chip in place of its paste (for the
// scrollback record).
func collapsed(buf []rune, spans []pasteSpan) string {
	if len(spans) == 0 {
		return string(buf)
	}
	var b strings.Builder
	for i := 0; i < len(buf); {
		if j := spanStartingAt(spans, i); j >= 0 {
			b.WriteString(spanLabel(spans, j))
			i += spans[j].n
			continue
		}
		b.WriteRune(buf[i])
		i++
	}
	return b.String()
}

// chipCells is buf as display cells for a one-row window (the docked composer):
// each chip is one cell. cur maps the caret to its cell index.
func chipCells(buf []rune, spans []pasteSpan, cur int) (cells []string, ws []int, ccur int) {
	ccur = -1
	for i := 0; i < len(buf); {
		if i == cur {
			ccur = len(cells)
		}
		if j := spanStartingAt(spans, i); j >= 0 {
			l := spanLabel(spans, j)
			cells = append(cells, Accent(l))
			ws = append(ws, width(l))
			i += spans[j].n
			continue
		}
		s, w := dispRune(buf[i])
		cells = append(cells, s)
		ws = append(ws, w)
		i++
	}
	if ccur < 0 {
		ccur = len(cells)
	}
	return
}

// inkJoin joins display cells in ink, restoring it after a styled cell (a chip).
func inkJoin(cells []string) string {
	var b strings.Builder
	b.WriteString(fg(pal.ink))
	for _, c := range cells {
		b.WriteString(c)
		if strings.HasPrefix(c, "\x1b") {
			b.WriteString(fg(pal.ink))
		}
	}
	b.WriteString(reset())
	return b.String()
}

// pasteMode switches the terminal's bracketed paste on or off (only where the
// composer reads raw keys from a terminal with escapes).
func pasteMode(on bool) {
	if !color || !IsTTY() {
		return
	}
	if on {
		Printf("\x1b[?2004h")
	} else {
		Printf("\x1b[?2004l")
	}
}

// ---------- the docked composer's chips (called with ta.mu held) ----------

func (t *typeahead) fixSpans() {
	if t.cur > len(t.buf) || t.cur < 0 {
		t.cur = len(t.buf)
	}
	if len(t.pastes) > 0 {
		t.pastes = syncSpans(t.spanBuf, t.buf, t.pastes, t.spanPos)
		t.cur = outOfChip(t.cur, t.pastes)
	}
	t.spanBuf, t.spanPos = append(t.spanBuf[:0], t.buf...), t.cur
}

func (t *typeahead) chip(k string) bool {
	nb, sp, ok := chipKey(k, t.buf, &t.cur, t.pastes)
	if ok {
		t.buf, t.pastes = nb, sp
		t.spanBuf, t.spanPos = append(t.spanBuf[:0], t.buf...), t.cur
	}
	return ok
}

// paste inserts a bracketed paste at the caret (a big one as a chip, a dragged-in
// image path as an image chip).
func (t *typeahead) paste(text string) {
	t.fixSpans()
	t.buf, t.pastes = pasteInsert(t.buf, &t.cur, t.pastes, text)
	t.spanBuf, t.spanPos = append(t.spanBuf[:0], t.buf...), t.cur
}

// clipPaste is Ctrl+V (or an empty paste) in the docked composer. The clipboard
// is read without ta.mu held (it can take a moment); a hint prints above.
func (t *typeahead) clipPaste(imageOnly bool) {
	c := readClipboard(imageOnly)
	if c.hint != "" {
		Above("  " + Weak(c.hint))
		return
	}
	t.mu.Lock()
	t.fixSpans()
	t.buf, t.pastes = c.apply(t.buf, &t.cur, t.pastes)
	t.spanBuf, t.spanPos = append(t.spanBuf[:0], t.buf...), t.cur
	t.mu.Unlock()
}

// PasteOff switches bracketed paste off. Hosts call it on every way out (exit,
// fatal error, signal) so the shell after Blaze never gets ESC[200~ marks.
func PasteOff() { pasteMode(false) }

// ---------- images and the clipboard (v0.13: Ctrl+V) ----------

// ClipImage and ClipText read the system clipboard (internal/clip); tests swap them.
var (
	ClipImage = clipboard.Image
	ClipText  = clipboard.Text
)

// insertImage puts an image chip for the file at path at *pos: the buffer gets
// the reference "@image:PATH" (what is sent), the composer shows [image N].
func insertImage(buf []rune, pos *int, spans []pasteSpan, path string) ([]rune, []pasteSpan) {
	tok := "@image:" + refs.Quote(path)
	p := *pos
	pre, post := "", " " // a reference is a word of its own
	if p > 0 && buf[p-1] != ' ' && buf[p-1] != '\n' {
		pre = " "
	}
	if p < len(buf) && buf[p] == ' ' {
		post = ""
	}
	nb, out := insertPaste(buf, pos, spans, pre+tok+post)
	out = append(out, pasteSpan{at: p + len(pre), n: len([]rune(tok)), image: true})
	return nb, out
}

// imagePath reports the image file a paste names: a dragged-in path, quoted
// ('…' or "…"), backslash-escaped or a file:// URL, that is an existing PNG,
// JPEG or GIF. Anything else is text.
func imagePath(text string) (string, bool) {
	s := strings.TrimSpace(text)
	if s == "" || strings.ContainsAny(s, "\n\r") {
		return "", false
	}
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		s = s[1 : len(s)-1]
	} else if runtime.GOOS != "windows" && strings.Contains(s, `\`) {
		var b strings.Builder // "My\ Shot.png" → "My Shot.png"
		for i := 0; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
			}
			b.WriteByte(s[i])
		}
		s = b.String()
	}
	if strings.HasPrefix(s, "file://") {
		s = strings.TrimPrefix(s, "file://")
		if runtime.GOOS == "windows" {
			s = strings.TrimPrefix(s, "/")
		}
		s = strings.ReplaceAll(s, "%20", " ")
	}
	if !refs.ImageExt(s) || !filepath.IsAbs(s) && !strings.HasPrefix(s, "~") {
		return "", false
	}
	if strings.HasPrefix(s, "~") {
		if h, err := os.UserHomeDir(); err == nil {
			s = filepath.Join(h, s[1:])
		}
	}
	if st, err := os.Stat(s); err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	return s, true
}

// pasteInsert inserts a paste: a dragged image path becomes an image chip,
// anything else text (a big paste a chip).
func pasteInsert(buf []rune, pos *int, spans []pasteSpan, text string) ([]rune, []pasteSpan) {
	if p, ok := imagePath(text); ok {
		return insertImage(buf, pos, spans, p)
	}
	return insertPaste(buf, pos, spans, text)
}

// clipResult is what the clipboard gave a paste key: an image file, text, or a
// short hint to show (nothing usable, or no tool to read it).
type clipResult struct {
	image, text, hint string
}

// readClipboard reads the clipboard for Ctrl+V (image first, then text) or for
// an empty bracketed paste (imageOnly: a terminal that pasted an image-only
// clipboard as nothing, like Windows Terminal).
func readClipboard(imageOnly bool) clipResult {
	ctx := context.Background()
	p, err := ClipImage(ctx)
	if err == nil {
		return clipResult{image: p}
	}
	var nt *clipboard.NoToolError
	if errors.As(err, &nt) {
		return clipResult{hint: nt.Hint}
	}
	if imageOnly {
		return clipResult{}
	}
	t, err := ClipText(ctx)
	switch {
	case err == nil && t != "":
		return clipResult{text: cleanPaste([]rune(t))}
	case errors.As(err, &nt):
		return clipResult{hint: nt.Hint}
	case err != nil && !errors.Is(err, clipboard.ErrEmpty):
		return clipResult{hint: "Couldn't read the clipboard: " + err.Error()}
	}
	return clipResult{hint: "The clipboard is empty"}
}

// apply puts a clipboard result into buf at *pos.
func (c clipResult) apply(buf []rune, pos *int, spans []pasteSpan) ([]rune, []pasteSpan) {
	switch {
	case c.image != "":
		return insertImage(buf, pos, spans, c.image)
	case c.text != "":
		return pasteInsert(buf, pos, spans, c.text)
	}
	return buf, spans
}

// ReceiptItem is one part of the receipt under a sent message.
type ReceiptItem struct {
	Text string // "@diff 214 lines"
	OK   bool
}

// Receipt draws the one-line receipt shown under a message whose @ references
// were attached: "■ @diff 214 lines · @url example.com 3K"; a part that failed
// gets □ and the sub tone. Clipped to the terminal; "" when there is nothing.
func Receipt(items []ReceiptItem) string {
	if len(items) == 0 {
		return ""
	}
	mark := Accent("■ ")
	allBad := true
	for _, it := range items {
		if it.OK {
			allBad = false
		}
	}
	if allBad {
		mark = Weak("□ ")
	}
	W := termWidth()
	var b strings.Builder
	b.WriteString("  " + mark)
	used := 4
	for i, it := range items {
		t := it.Text
		sep := ""
		if i > 0 {
			sep = " · "
		}
		if used+len([]rune(sep))+len([]rune(t)) > W-2 {
			if rest := len(items) - i; rest > 0 {
				b.WriteString(Weak(" · +" + strconv.Itoa(rest)))
			}
			break
		}
		used += len([]rune(sep)) + len([]rune(t))
		if sep != "" {
			b.WriteString(Weak(sep))
		}
		name, detail, _ := strings.Cut(t, " ")
		switch {
		case !it.OK:
			b.WriteString(Weak("□ ") + Sub(t))
		case detail == "":
			b.WriteString(Ink(name))
		default:
			b.WriteString(Ink(name) + " " + Sub(detail))
		}
	}
	return b.String()
}
