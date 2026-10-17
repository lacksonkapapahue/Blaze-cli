package ui

// A small line editor so the prompt behaves like a real shell prompt:
// ← → Home End, word jumps, ↑ ↓ history, Tab completion, ghost suggestions,
// bracketed paste and Ctrl shortcuts. Falls back to plain reads when piped.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"golang.org/x/term"
)

// ErrInterrupt is returned when Ctrl-C is pressed on an empty prompt.
var ErrInterrupt = errors.New("interrupt")

type Editor struct {
	HistoryFile string                     // persisted history, one entry per line ("" = memory only)
	Commands    []string                   // slash commands for Tab / ghost completion
	Placeholder string                     // dim hint shown while the line is empty
	Boxed       bool                       // draw the input inside a hairline box
	Glyph       func(line string) string   // prompt glyph for the current line (mode / shell)
	Footer      func(line string) string   // dim line under the box
	OnShiftTab  func()                     // cycle modes
	Menu        func(line string) *MenuSet // pop-up menu above the box for this line (nil = none)
	DoubleEsc   string                     // Esc twice on an empty prompt submits this ("/rewind"); "" = off
	RightRecord bool                       // a sent line stays in the scrollback right-aligned, like a chat bubble (UserLines)

	history []string
	loaded  bool
	killed  []rune // last Ctrl-K/U/W text, for Ctrl-Y

	// render state
	buf     []rune
	pos     int
	prompt  string
	curRow  int // row of the cursor relative to the prompt's first row
	histIdx int
	draft   []rune
	keep    []rune // typed text kept across a wake

	// pop-up menu state
	menuIdx  int
	menuLine string // the line the highlight belongs to
	menuOff  string // Esc closed the menu for exactly this line
	lastEsc  time.Time

	// big pastes shown as chips (w3_paste.go)
	pastes     []pasteSpan
	keepPastes []pasteSpan // the chips of keep
	spanBuf    []rune      // the buffer the chips were last checked against
	spanPos    int         // the caret then

	// placeholder tips (w3_box.go)
	prompts     int    // prompts opened so far
	tipHold     bool   // the last Read ended in a wake: the next keeps its tip
	placeholder string // the hint for this prompt
}

// StdinTTY is true when we can put the keyboard in raw mode.
func StdinTTY() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

func termWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 10 {
		return w
	}
	return 80
}

func (e *Editor) load() {
	if e.loaded {
		return
	}
	e.loaded = true
	if e.HistoryFile == "" {
		return
	}
	b, err := os.ReadFile(e.HistoryFile)
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			e.history = append(e.history, strings.ReplaceAll(l, `\n`, "\n"))
		}
	}
}

func (e *Editor) remember(s string) {
	if strings.TrimSpace(s) == "" {
		return
	}
	if n := len(e.history); n > 0 && e.history[n-1] == s {
		return
	}
	e.history = append(e.history, s)
	if len(e.history) > 500 {
		e.history = e.history[len(e.history)-500:]
	}
	if e.HistoryFile == "" {
		return
	}
	var b strings.Builder
	for _, h := range e.history {
		b.WriteString(strings.ReplaceAll(h, "\n", `\n`) + "\n")
	}
	_ = os.WriteFile(e.HistoryFile, []byte(b.String()), 0o600)
}

// Read shows prompt (which may start with newlines) and edits one line.
func (e *Editor) Read(prompt string) (string, error) {
	e.load()
	if !StdinTTY() || !IsTTY() {
		return ReadLine(prompt)
	}
	if i := strings.LastIndex(prompt, "\n"); i >= 0 {
		Printf("%s", strings.ReplaceAll(prompt[:i+1], "\n", "\r\n"))
		prompt = prompt[i+1:]
	}
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return ReadLine(prompt)
	}
	Printf("\x1b[?2004h") // bracketed paste on
	defer func() {
		Printf("\x1b[?2004l")
		_ = term.Restore(fd, old)
	}()

	FlushAbove()
	edMu.Lock()
	if wakeWanted {
		wakeWanted = false
		edMu.Unlock()
		return "", ErrWake
	}
	select {
	case <-stdinR.wake: // a stale wake from an earlier prompt
	default:
	}
	e.buf, e.pos, e.prompt, e.curRow = nil, 0, prompt, 0
	e.pastes = nil
	if e.keep != nil {
		e.buf, e.pos, e.keep = e.keep, len(e.keep), nil
		e.pastes = e.keepPastes
	}
	e.keepPastes = nil
	e.spanBuf, e.spanPos = append([]rune(nil), e.buf...), e.pos
	if !e.tipHold {
		e.placeholder = placeholderFor(e.Placeholder, e.prompts, Tips)
		e.prompts++
	}
	e.tipHold = false
	e.histIdx, e.draft = len(e.history), nil
	e.menuIdx, e.menuLine, e.menuOff = 0, "", ""
	activeEd = e
	e.render(false)
	edMu.Unlock()
	defer func() {
		edMu.Lock()
		if activeEd == e {
			activeEd = nil
		}
		edMu.Unlock()
		touchActivity() // what follows the prompt prints over an expanded Ctrl+O view
	}()

	for {
		r, _, err := In.ReadRune()
		edMu.Lock()
		out, done, rerr := e.key(r, err)
		edMu.Unlock()
		if done {
			return out, rerr
		}
	}
}

// key handles one key (called with edMu held). done ends the Read.
func (e *Editor) key(r rune, err error) (string, bool, error) {
	if errors.Is(err, ErrWake) {
		e.clear()
		e.fixSpans()
		e.keepPastes = e.pastes
		e.tipHold = true
		e.keep = append([]rune(nil), e.buf...)
		wakeWanted = false
		activeEd = nil
		return "", true, ErrWake
	}
	if err != nil {
		e.render(true)
		Printf("\r\n")
		return "", true, io.EOF
	}
	switch r {
	case '\r', '\n':
		if submit, ok := e.menuPick(true); ok && !submit {
			break // filled in from the menu; Enter again (or keep typing)
		}
		out := string(e.buf)
		e.record(out)
		Printf("\r\n")
		e.remember(out)
		RecordRecent(out)
		return out, true, nil
	case 3: // Ctrl-C
		if len(e.buf) > 0 {
			e.buf, e.pos = nil, 0
			break
		}
		e.render(true)
		Printf("\r\n")
		return "", true, ErrInterrupt
	case 4: // Ctrl-D
		if len(e.buf) == 0 {
			e.render(true)
			Printf("\r\n")
			return "", true, io.EOF
		}
		if !e.chip("del") {
			e.del()
		}
	case 127, 8:
		if !e.chip("backspace") {
			e.backspace()
		}
	case 1:
		e.pos = 0
	case 5:
		e.end()
	case 2:
		if !e.chip("left") {
			e.left()
		}
	case 6:
		if !e.chip("right") {
			e.right()
		}
	case 11:
		e.kill(e.pos, len(e.buf))
	case 21:
		e.kill(0, e.pos)
	case 23:
		e.kill(e.wordLeft(), e.pos)
	case 25:
		e.insert(e.killed)
	case 22: // Ctrl+V: a clipboard image as an [image N] chip, else its text (w3_paste.go)
		e.clipPaste(false)
	case 12:
		Printf("\x1b[H\x1b[2J")
		e.curRow = 0
		touchActivity()
	case 15: // Ctrl+O: expand / collapse the last turn's details (activity.go)
		e.clear()
		toggleActivity()
	case 16:
		if !e.menuMove(-1) {
			e.histPrev()
			e.menuOff = string(e.buf) // a recalled line doesn't pop the menu
		}
	case 14:
		if !e.menuMove(1) {
			e.histNext()
			e.menuOff = string(e.buf) // a recalled line doesn't pop the menu
		}
	case 9:
		if _, ok := e.menuPick(false); !ok {
			e.tab()
		}
	case 27:
		if e.escape() && e.DoubleEsc != "" {
			e.render(true)
			Printf("\r\n")
			return e.DoubleEsc, true, nil
		}
	default:
		if r >= 32 || r == '\t' {
			e.insert([]rune{r})
		}
	}
	e.render(false)
	return "", false, nil
}

// clear wipes the drawn prompt so something can print where it was.
// record replaces the live input with what stays in the scrollback once a line
// is sent: the right-aligned bubble for a chat prompt, else the plain "› text".
func (e *Editor) record(out string) {
	if !e.RightRecord || strings.TrimSpace(out) == "" {
		e.render(true)
		return
	}
	e.clear()
	e.fixSpans()
	Printf("%s", strings.Join(UserLines(collapsed(e.buf, e.pastes), ""), "\r\n"))
	e.curRow = 0
}

func (e *Editor) clear() {
	if e.curRow > 0 {
		Printf("\x1b[%dA", e.curRow)
	}
	Printf("\r\x1b[J")
	e.curRow = 0
}

func (e *Editor) insert(rs []rune) {
	if len(rs) == 0 {
		return
	}
	nb := make([]rune, 0, len(e.buf)+len(rs))
	nb = append(nb, e.buf[:e.pos]...)
	nb = append(nb, rs...)
	nb = append(nb, e.buf[e.pos:]...)
	e.buf = nb
	e.pos += len(rs)
}

func (e *Editor) backspace() {
	if e.pos > 0 {
		e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
		e.pos--
	}
}

func (e *Editor) del() {
	if e.pos < len(e.buf) {
		e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
	}
}

func (e *Editor) left() {
	if e.pos > 0 {
		e.pos--
	}
}

func (e *Editor) right() {
	if e.pos < len(e.buf) {
		e.pos++
		return
	}
	// At the end: → accepts the ghost suggestion, like fish.
	if g := e.ghost(); g != "" {
		e.insert([]rune(g))
	}
}

func (e *Editor) end() {
	if e.pos == len(e.buf) {
		if g := e.ghost(); g != "" {
			e.insert([]rune(g))
		}
	}
	e.pos = len(e.buf)
}

func (e *Editor) kill(a, b int) {
	if a >= b {
		return
	}
	e.killed = append([]rune(nil), e.buf[a:b]...)
	e.buf = append(e.buf[:a], e.buf[b:]...)
	e.pos = a
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

func (e *Editor) wordLeft() int {
	i := e.pos
	for i > 0 && !isWord(e.buf[i-1]) {
		i--
	}
	for i > 0 && isWord(e.buf[i-1]) {
		i--
	}
	return i
}

func (e *Editor) wordRight() int {
	i := e.pos
	n := len(e.buf)
	for i < n && !isWord(e.buf[i]) {
		i++
	}
	for i < n && isWord(e.buf[i]) {
		i++
	}
	return i
}

func (e *Editor) histPrev() {
	if e.histIdx == 0 {
		return
	}
	if e.histIdx == len(e.history) {
		e.draft = append([]rune(nil), e.buf...)
	}
	e.histIdx--
	e.buf = []rune(e.history[e.histIdx])
	e.pos = len(e.buf)
}

func (e *Editor) histNext() {
	if e.histIdx >= len(e.history) {
		return
	}
	e.histIdx++
	if e.histIdx == len(e.history) {
		e.buf = append([]rune(nil), e.draft...)
	} else {
		e.buf = []rune(e.history[e.histIdx])
	}
	e.pos = len(e.buf)
}

func (e *Editor) matches() []string {
	s := string(e.buf)
	var out []string
	for _, c := range e.Commands {
		if strings.HasPrefix(c, s) {
			out = append(out, c)
		}
	}
	return out
}

// ghost is the dim completion shown after the cursor when it's at the end.
func (e *Editor) ghost() string {
	if e.pos != len(e.buf) || len(e.buf) == 0 {
		return ""
	}
	if g, open := e.menuGhost(); open {
		return g
	}
	s := string(e.buf)
	if strings.HasPrefix(s, "/") && !strings.Contains(s, " ") {
		if m := e.matches(); len(m) > 0 && m[0] != s {
			return m[0][len(s):]
		}
		return ""
	}
	for i := len(e.history) - 1; i >= 0; i-- {
		h := e.history[i]
		if len(h) > len(s) && strings.HasPrefix(h, s) && !strings.Contains(h, "\n") {
			return h[len(s):]
		}
	}
	return ""
}

func (e *Editor) tab() {
	if e.atComplete() {
		return
	}
	s := string(e.buf)
	if !strings.HasPrefix(s, "/") || strings.Contains(s, " ") {
		if g := e.ghost(); g != "" {
			e.insert([]rune(g))
		}
		return
	}
	m := e.matches()
	switch len(m) {
	case 0:
		return
	case 1:
		e.buf = []rune(m[0] + " ")
		e.pos = len(e.buf)
		return
	}
	// Several: complete the shared prefix and list them.
	p := m[0]
	for _, c := range m[1:] {
		for !strings.HasPrefix(c, p) {
			p = p[:len(p)-1]
		}
	}
	if len(p) > len(s) {
		e.buf = []rune(p)
		e.pos = len(e.buf)
		return
	}
	e.render(true)
	var parts []string
	for _, c := range m {
		parts = append(parts, Accent(c))
	}
	Printf("\r\n  %s\r\n", strings.Join(parts, Weak("  ")))
	e.curRow = 0
}

// escape handles a key sequence starting with Esc. It reports a lone Esc pressed
// twice quickly on an empty prompt.
func (e *Editor) escape() (double bool) {
	first, ok := readEscapeFirst()
	if !ok {
		e.menuOff = string(e.buf) // a lone Esc key closes the menu
		if len(e.buf) == 0 {
			if time.Since(e.lastEsc) < 800*time.Millisecond {
				e.lastEsc = time.Time{}
				return true
			}
			e.lastEsc = time.Now()
		}
		return false
	}
	seq, skipped := skipTerminalReply(first, inputReadRune)
	if skipped {
		return false
	}
	switch first {
	case '[', 'O':
		e.csi(first, seq)
	case 'b', 'B':
		e.pos = e.wordLeft()
	case 'f', 'F':
		e.pos = e.wordRight()
	case 127, 8:
		e.kill(e.wordLeft(), e.pos)
	case 'd':
		e.kill(e.pos, e.wordRight())
	case 27: // Esc Esc arrived together
		if len(e.buf) == 0 && In.Buffered() == 0 {
			return true
		}
	}
	return false
}

func (e *Editor) csi(intro rune, seq string) {
	switch seq {
	case "A":
		if !e.menuMove(-1) {
			e.histPrev()
			e.menuOff = string(e.buf) // a recalled line doesn't pop the menu
		}
	case "B":
		if !e.menuMove(1) {
			e.histNext()
			e.menuOff = string(e.buf) // a recalled line doesn't pop the menu
		}
	case "C":
		if !e.chip("right") {
			e.right()
		}
	case "D":
		if !e.chip("left") {
			e.left()
		}
	case "H", "1~", "7~":
		e.pos = 0
	case "F", "4~", "8~":
		e.end()
	case "3~":
		if !e.chip("del") {
			e.del()
		}
	case "1;5C", "1;3C", "5C":
		e.pos = e.wordRight()
	case "1;5D", "1;3D", "5D":
		e.pos = e.wordLeft()
	case "3;5~":
		e.kill(e.pos, e.wordRight())
	case "200~":
		e.paste()
	case "Z":
		if e.OnShiftTab != nil {
			e.OnShiftTab()
		}
	}
	_ = intro
}

// paste reads a bracketed paste verbatim (newlines kept, nothing submitted). A
// paste of more than pasteChipLines lines shows as one chip (w3_paste.go).
func (e *Editor) paste() {
	text := readPaste()
	if text == "" {
		// An empty paste: Windows Terminal pastes an image-only clipboard as
		// nothing (it binds Ctrl+V itself), so look for an image there.
		e.clipPaste(true)
		return
	}
	e.fixSpans()
	e.buf, e.pastes = pasteInsert(e.buf, &e.pos, e.pastes, text) // a dragged-in image path becomes a chip
	e.spanBuf, e.spanPos = append([]rune(nil), e.buf...), e.pos
}

// clipPaste reads the system clipboard into the line: an image as a chip, or
// (unless imageOnly) its text as a paste. A hint shows above the prompt when
// there is nothing to paste or no tool to read the clipboard.
func (e *Editor) clipPaste(imageOnly bool) {
	c := readClipboard(imageOnly)
	if c.hint != "" {
		e.clear()
		Printf("  %s\r\n", Weak(c.hint))
		return
	}
	e.fixSpans()
	e.buf, e.pastes = c.apply(e.buf, &e.pos, e.pastes)
	e.spanBuf, e.spanPos = append([]rune(nil), e.buf...), e.pos
}

// fixSpans brings the chips up to date with edits made since they were last
// checked, and keeps the caret out of them.
func (e *Editor) fixSpans() {
	if len(e.pastes) > 0 {
		e.pastes = syncSpans(e.spanBuf, e.buf, e.pastes, e.spanPos)
		e.pos = outOfChip(e.pos, e.pastes)
	}
	e.spanBuf, e.spanPos = append(e.spanBuf[:0], e.buf...), e.pos
}

// chip applies key k to a chip next to the caret; it reports whether it acted.
func (e *Editor) chip(k string) bool {
	e.fixSpans()
	nb, sp, ok := chipKey(k, e.buf, &e.pos, e.pastes)
	if ok {
		e.buf, e.pastes = nb, sp
		e.spanBuf, e.spanPos = append(e.spanBuf[:0], e.buf...), e.pos
	}
	return ok
}

// ---------- drawing ----------

func runeW(r rune) int {
	switch {
	case r == 0 || unicode.Is(unicode.Mn, r) || r == 0x200d || (r >= 0xfe00 && r <= 0xfe0f):
		return 0
	case r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) || (r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) || (r >= 0x1f300 && r <= 0x1f64f) ||
		(r >= 0x1f900 && r <= 0x1f9ff) || (r >= 0x20000 && r <= 0x3fffd)):
		return 2
	}
	return 1
}

func dispRune(r rune) (string, int) {
	switch r {
	case '\n':
		return Weak("↵"), 1
	case '\t':
		return " ", 1
	}
	return string(r), runeW(r)
}

type cell struct {
	s string
	w int
}

// layout wraps cells into rows of at most n columns; it returns the rows and
// the row/col of cell index pos.
func layout(cells []cell, n, pos int) (rows [][]cell, cr, cc int) {
	rows = [][]cell{{}}
	col := 0
	for i, c := range cells {
		if i == pos {
			cr, cc = len(rows)-1, col
		}
		if c.s == "\n" {
			rows = append(rows, []cell{})
			col = 0
			continue
		}
		if col+c.w > n {
			rows = append(rows, []cell{})
			col = 0
			if i == pos {
				cr, cc = len(rows)-1, 0
			}
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], c)
		col += c.w
	}
	if pos >= len(cells) {
		cr, cc = len(rows)-1, col
		if col >= n {
			rows = append(rows, []cell{})
			cr, cc = len(rows)-1, 0
		}
	}
	return
}

// render redraws the input in place. final=true leaves a plain "› text" record
// (no box, ghost or hint) so the scrollback stays clean.
func (e *Editor) render(final bool) {
	W := termWidth()
	boxed := e.Boxed && W >= 30 && color
	glyph := e.prompt
	if e.Glyph != nil {
		glyph = e.Glyph(string(e.buf))
	}
	if boxed && final {
		glyph = "  " + glyph // line up with the box in the scrollback
	}
	pw := width(glyph)
	n := W - 1 - pw     // text columns per row
	BW := min(W-3, 110) // box width, same as the welcome card
	if boxed {
		n = BW - 4 - pw
	}
	if n < 5 {
		n = 5
	}
	e.fixSpans()
	cells := make([]cell, 0, len(e.buf)+16)
	pos := -1 // cursor index in cells (newlines take two cells while editing, a chip one)
	for i := 0; i < len(e.buf); i++ {
		if i == e.pos {
			pos = len(cells)
		}
		if j := spanStartingAt(e.pastes, i); j >= 0 {
			l := clip(spanLabel(e.pastes, j), n)
			cells = append(cells, cell{Accent(l), width(l)})
			i += e.pastes[j].n - 1
			continue
		}
		r := e.buf[i]
		if r == '\n' && !final {
			cells = append(cells, cell{Weak("↵"), 1}, cell{"\n", 0})
			continue
		}
		if r == '\n' {
			cells = append(cells, cell{"\n", 0})
			continue
		}
		s, w := dispRune(r)
		cells = append(cells, cell{s, w})
	}
	if pos < 0 {
		pos = len(cells)
	}
	nText := len(cells)
	if !final {
		extra := e.ghost()
		if extra == "" && len(e.buf) == 0 {
			extra = e.placeholder
			if e.prompts == 0 {
				extra = e.Placeholder
			}
		}
		for _, r := range extra {
			_, w := dispRune(r)
			cells = append(cells, cell{Weak(string(r)), w})
		}
	}
	rows, cr, cc := layout(cells, n, pos)
	_ = nText

	var sb strings.Builder
	if e.curRow > 0 {
		fmt.Fprintf(&sb, "\x1b[%dA", e.curRow)
	}
	sb.WriteString("\r\x1b[J")
	var menu []string
	if !final {
		mw := BW
		if !boxed {
			mw = min(W-3, 110)
		}
		menu = e.menuLines(mw)
		for _, l := range menu {
			sb.WriteString(l + "\r\n")
		}
	}
	line := func(i int, r []cell) string {
		var b strings.Builder
		if i == 0 {
			b.WriteString(glyph)
		} else {
			b.WriteString(strings.Repeat(" ", pw))
		}
		b.WriteString(fg(pal.ink))
		w := 0
		for _, c := range r {
			b.WriteString(c.s)
			if strings.HasPrefix(c.s, "\x1b") {
				b.WriteString(fg(pal.ink))
			}
			w += c.w
		}
		b.WriteString(reset())
		if boxed && !final {
			b.WriteString(strings.Repeat(" ", max(0, n-w)))
		}
		return b.String()
	}
	if boxed && !final {
		sb.WriteString("  " + boxTop(BW) + "\r\n")
		for i, r := range rows {
			sb.WriteString("  " + hair("│") + " " + line(i, r) + " " + hair("│") + "\r\n")
		}
		sb.WriteString("  " + hair("└"+strings.Repeat("─", BW-2)+"┘"))
		f := ""
		if e.Footer != nil {
			f = e.Footer(string(e.buf))
		}
		f = withGauge(f, gauge(W), W-2)
		if f != "" {
			sb.WriteString("\r\n" + f)
		}
		total := 1 + len(rows) + 1 // rows from top border to bottom border
		if f != "" {
			total++
		}
		target := 1 + cr
		fmt.Fprintf(&sb, "\x1b[%dA\r", total-1-target)
		fmt.Fprintf(&sb, "\x1b[%dC", 4+pw+cc)
		e.curRow = len(menu) + target
	} else {
		for i, r := range rows {
			if i > 0 {
				sb.WriteString("\r\n")
			}
			sb.WriteString(line(i, r))
		}
		if final {
			e.curRow = len(rows) - 1
		} else {
			if up := len(rows) - 1 - cr; up > 0 {
				fmt.Fprintf(&sb, "\x1b[%dA", up)
			}
			sb.WriteString("\r")
			if c := pw + cc; c > 0 {
				fmt.Fprintf(&sb, "\x1b[%dC", c)
			}
			e.curRow = len(menu) + cr
		}
	}
	Printf("%s", sb.String())
}

// atComplete completes an @path under the cursor. It reports whether it acted.
func (e *Editor) atComplete() bool {
	start := e.pos
	for start > 0 && e.buf[start-1] != ' ' && e.buf[start-1] != '\n' {
		start--
	}
	word := string(e.buf[start:e.pos])
	if !strings.HasPrefix(word, "@") {
		return false
	}
	frag := word[1:]
	for _, k := range []string{"file:", "folder:", "image:"} { // @file:src/ma completes like @src/ma
		if strings.HasPrefix(frag, k) {
			frag = strings.TrimPrefix(frag[len(k):], `"`)
			break
		}
	}
	dir, base := filepath.Split(frag)
	look := dir
	if look == "" {
		look = "."
	}
	if strings.HasPrefix(look, "~") {
		h, _ := os.UserHomeDir()
		look = h + look[1:]
	}
	ents, err := os.ReadDir(look)
	if err != nil {
		return true
	}
	var ms []string
	for _, en := range ents {
		nm := en.Name()
		if strings.HasPrefix(nm, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if strings.HasPrefix(nm, base) {
			if en.IsDir() {
				nm += "/"
			}
			ms = append(ms, nm)
		}
	}
	if len(ms) == 0 {
		return true
	}
	p := ms[0]
	for _, c := range ms[1:] {
		for !strings.HasPrefix(c, p) {
			p = p[:len(p)-1]
		}
	}
	if len(ms) == 1 && !strings.HasSuffix(p, "/") {
		p += " "
	}
	if len(p) > len(base) {
		e.insert([]rune(p[len(base):]))
		return true
	}
	e.render(true)
	var parts []string
	for i, c := range ms {
		if i == 24 {
			parts = append(parts, Weak(fmt.Sprintf("+%d more", len(ms)-24)))
			break
		}
		parts = append(parts, Accent(c))
	}
	Printf("\r\n  %s\r\n", strings.Join(parts, Weak("  ")))
	e.curRow = 0
	return true
}

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// ---------- choosers ----------

// rawKeys runs fn for each key in raw mode until fn returns true.
// Keys are reported as names: "up", "down", "left", "right", "enter", "space",
// "tab", "esc", "ctrl-c", or the typed character.
func rawKeys(fn func(key string) bool) bool {
	defer holdKeys()() // mid-turn: take the keyboard from the composer (typeahead.go)
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return false
	}
	defer term.Restore(fd, old)
	return rawKeysRead(fn, func() (rune, error) {
		r, _, err := In.ReadRune()
		return r, err
	}, In.Buffered, stdinR.waitInput)
}

func rawKeysRead(fn func(key string) bool, readRune func() (rune, error), buffered func() int, wait func(time.Duration) bool) bool {
	for {
		r, err := readRune()
		if err != nil {
			fn("ctrl-c")
			return true
		}
		k := rawKeyName(r)
		switch r {
		case 27:
			var hasKey bool
			k, hasKey = rawEscapeKey(buffered(), wait, readRune)
			if !hasKey {
				continue
			}
		}
		if k != "" && fn(k) {
			return true
		}
	}
}

func rawKeyName(r rune) string {
	switch r {
	case '\r', '\n':
		return "enter"
	case ' ':
		return "space"
	case '\t':
		return "tab"
	case 3, 4:
		return "ctrl-c"
	case 16:
		return "up"
	case 14:
		return "down"
	default:
		return string(r)
	}
}

func rawEscapeKey(buffered int, wait func(time.Duration) bool, readRune func() (rune, error)) (string, bool) {
	first, ok := readEscapeFirstWith(buffered, wait, readRune)
	if !ok {
		return "esc", true
	}
	seq, skipped := skipTerminalReply(first, readRune)
	if skipped {
		return "", false
	}
	switch first {
	case '[', 'O':
		return map[string]string{"A": "up", "B": "down", "C": "right", "D": "left"}[seq], true
	case 27:
		return "esc", true
	default:
		return string(first), true
	}
}

func readEscapeFirst() (rune, bool) {
	return readEscapeFirstWith(In.Buffered(), stdinR.waitInput, func() (rune, error) {
		r, _, err := In.ReadRune()
		return r, err
	})
}

func readEscapeFirstWith(buffered int, wait func(time.Duration) bool, readRune func() (rune, error)) (rune, bool) {
	if buffered == 0 && !wait(50*time.Millisecond) {
		return 0, false
	}
	r, err := readRune()
	return r, err == nil
}

func inputReadRune() (rune, error) {
	r, _, err := In.ReadRune()
	return r, err
}

func skipTerminalReply(first rune, readRune func() (rune, error)) (string, bool) {
	switch first {
	case ']', 'P', '_', '^':
		escaped := false
		for {
			r, err := readRune()
			if err != nil {
				return "", true
			}
			if r == 7 || escaped && r == '\\' {
				return "", true
			}
			escaped = r == 27
		}
	case '[', 'O':
		seq := ""
		for {
			r, err := readRune()
			if err != nil {
				return "", true
			}
			seq += string(r)
			if r >= 0x40 && r <= 0x7e {
				if first == '[' && (strings.HasPrefix(seq, "?") || strings.HasPrefix(seq, ">")) {
					return "", true
				}
				return seq, false
			}
		}
	default:
		return string(first), false
	}
}

// Choose shows options inline ("  ? ▸ Yes   No") and returns the picked index.
// ← → or Tab move, Enter picks, the first letter of an option picks it, Esc picks cancel.
func Choose(prefix string, options []string, def, cancel int) int {
	if len(options) == 0 {
		return cancel
	}
	if !StdinTTY() || !IsTTY() {
		ans, err := ReadLine(prefix + Weak("["+strings.Join(options, "/")+"] "))
		if err != nil {
			return cancel
		}
		a := strings.ToLower(strings.TrimSpace(ans))
		if a == "" {
			return def
		}
		for i, o := range options {
			if strings.HasPrefix(strings.ToLower(o), a[:1]) {
				return i
			}
		}
		return cancel
	}
	sel := def
	if sel < 0 || sel >= len(options) {
		sel = 0
	}
	draw := func() {
		var parts []string
		for i, o := range options {
			if i == sel {
				parts = append(parts, Accent("▸ ")+Bold(o))
			} else {
				parts = append(parts, "  "+Weak(o))
			}
		}
		Printf("\r\x1b[2K%s%s  %s", prefix, strings.Join(parts, "  "), Weak("← → enter"))
	}
	Printf("\x1b[?25l")
	draw()
	rawKeys(func(k string) bool {
		switch k {
		case "left", "up":
			sel = (sel + len(options) - 1) % len(options)
		case "right", "down", "tab":
			sel = (sel + 1) % len(options)
		case "enter", "space":
			return true
		case "esc", "ctrl-c":
			sel = cancel
			return true
		default:
			for i, o := range options {
				if o != "" && strings.EqualFold(k, o[:1]) {
					sel = i
					return true
				}
			}
		}
		draw()
		return false
	})
	// settle on the chosen answer only
	Printf("\r\x1b[2K%s%s\r\n\x1b[?25h", prefix, chosenLabel(options, sel))
	return sel
}

// chosenLabel is what Choose leaves on screen once it settles. A sel outside
// options (Esc, Ctrl-C or EOF with cancel = -1) shows "cancelled" and never
// indexes options.
func chosenLabel(options []string, sel int) string {
	if sel < 0 || sel >= len(options) {
		return Weak("cancelled")
	}
	return Accent("■ ") + Ink(options[sel])
}

// Checklist lets the user tick rows with ↑ ↓ and Space, Enter to confirm.
// It returns the indexes that are ticked.
func Checklist(rows []string, ticked []bool) []int {
	on := append([]bool(nil), ticked...)
	for len(on) < len(rows) {
		on = append(on, false)
	}
	if !StdinTTY() || !IsTTY() {
		Numbered(rows)
		ans, _ := ReadLine("  " + Ink("Which ones? ") + Weak("numbers like 1,2 · Enter = ticked "))
		ans = strings.TrimSpace(ans)
		if ans != "" {
			for i := range on {
				on[i] = false
			}
			for _, f := range strings.FieldsFunc(ans, func(r rune) bool { return r == ',' || r == ' ' }) {
				var n int
				if _, err := fmt.Sscan(f, &n); err == nil && n >= 1 && n <= len(rows) {
					on[n-1] = true
				}
			}
		}
		return picked(on)
	}
	cur := 0
	W := termWidth()
	draw := func(first bool) {
		if !first {
			Printf("\x1b[%dA", len(rows)+1)
		}
		for i, r := range rows {
			box := Weak("□")
			if on[i] {
				box = Accent("■")
			}
			ptr := "  "
			if i == cur {
				ptr = Accent("▸ ")
			}
			line := "  " + ptr + box + "  " + Weak(fmt.Sprintf("%02d", i+1)) + "  " + r
			if width(line) > W-1 {
				line = "  " + ptr + box + "  " + Weak(fmt.Sprintf("%02d", i+1)) + "  " + clip(ansi.ReplaceAllString(r, ""), W-12)
			}
			Printf("\r\x1b[2K%s\r\n", line)
		}
		Printf("\r\x1b[2K  %s\r\n", Weak("↑ ↓ move · space tick · enter done"))
	}
	Printf("\x1b[?25l")
	draw(true)
	rawKeys(func(k string) bool {
		switch k {
		case "up", "k":
			cur = (cur + len(rows) - 1) % len(rows)
		case "down", "j", "tab":
			cur = (cur + 1) % len(rows)
		case "space", "x":
			on[cur] = !on[cur]
		case "enter":
			return true
		case "esc", "ctrl-c":
			for i := range on {
				on[i] = false
			}
			return true
		default:
			var n int
			if _, err := fmt.Sscan(k, &n); err == nil && n >= 1 && n <= len(rows) {
				cur = n - 1
				on[cur] = !on[cur]
			}
		}
		draw(false)
		return false
	})
	draw(false)
	Printf("\x1b[1A\r\x1b[2K\x1b[?25h")
	return picked(on)
}

func picked(on []bool) []int {
	var out []int
	for i, v := range on {
		if v {
			out = append(out, i)
		}
	}
	return out
}
