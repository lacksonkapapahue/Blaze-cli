package ui

// A small line editor so the prompt behaves like a real shell prompt:
// ← → Home End, word jumps, ↑ ↓ history, ghost suggestions, Ctrl shortcuts, and
// the command menu ("/", "@" and argument menus like "/skills ", menu.go). Pasting
// keeps a multi-line block as one message (bracketed paste, Ctrl+Shift+V, or
// Ctrl+V reading the clipboard). In live mode (live.go) it stays open while Blaze
// works. Falls back to plain reads when piped.

import (
	"errors"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"golang.org/x/term"
)

// ErrInterrupt is returned when Ctrl-C is pressed on an empty prompt.
var ErrInterrupt = errors.New("interrupt")

type Editor struct {
	HistoryFile string                   // persisted history, one entry per line ("" = memory only)
	Menu        []MenuItem               // slash commands for the "/" menu
	ArgMenus    map[string]ArgMenu       // argument menus by command ("/skills")
	Dir         func() string            // folder the "@" menu lists (nil = the process's)
	Placeholder string                   // dim hint shown while the line is empty
	Boxed       bool                     // draw the input inside a hairline box
	Glyph       func(line string) string // prompt glyph for the current line (mode / shell)
	Footer      func(line string) string // dim line under the box
	OnShiftTab  func()                   // cycle modes
	Busy        func() bool              // Blaze is working (Esc / Ctrl+C interrupt it)
	OnInterrupt func()                   // stop the current turn
	OnImage     func(png []byte) string  // a pasted picture: save it, return the text to insert ("" = can't)

	history []string
	loaded  bool
	killed  []rune // last Ctrl-K/U/W text, for Ctrl-Y

	// render state
	buf       []rune
	pos       int
	prompt    string
	curRow    int // row of the cursor relative to the top of what's drawn
	histIdx   int
	draft     []rune
	keep      []rune // typed text kept across a wake (or set by Prefill)
	sel       int    // menu selection
	menuOff   bool   // Esc closed the menu…
	menuOffAt string // …for this text
	hint      string // one-line note under the box until the next key
}

// Prefill puts text in the prompt the next time it opens ("/skills " opens the
// skills picker).
func (e *Editor) Prefill(s string) {
	edMu.Lock()
	e.keep = []rune(s)
	edMu.Unlock()
}

// StdinTTY is true when we can put the keyboard in raw mode.
func StdinTTY() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

func termWidth() int {
	if termW > 0 {
		return termW
	}
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
		Printf("%s", prompt[:i+1])
		prompt = prompt[i+1:]
	}
	fd := int(os.Stdin.Fd())
	restore, err := makeRaw(fd)
	if err != nil {
		return ReadLine(prompt)
	}
	// Bracketed paste and focus reports only while the keyboard is raw: in the
	// terminal's normal mode a focus report would be echoed as ^[[I / ^[[O.
	focusOn := color
	seq := "\x1b[?2004h"
	if focusOn {
		seq += "\x1b[?1004h"
	}
	emit(seq)
	defer func() {
		off := "\x1b[?2004l"
		if focusOn {
			off += "\x1b[?1004l"
		}
		emit(off)
		restore()
	}()

	FlushAbove()
	edMu.Lock()
	for modalN > 0 { // an approval or picker is on screen: open after it
		edMu.Unlock()
		time.Sleep(15 * time.Millisecond)
		edMu.Lock()
	}
	if wakeWanted {
		wakeWanted = false
		edMu.Unlock()
		return "", ErrWake
	}
	rawActive = true
	select {
	case <-stdinR.wake: // a stale wake from an earlier prompt
	default:
	}
	e.buf, e.pos, e.prompt, e.curRow, e.sel, e.menuOff, e.hint = nil, 0, prompt, 0, 0, false, ""
	if e.keep != nil {
		e.buf, e.pos, e.keep = e.keep, len(e.keep), nil
	}
	e.histIdx, e.draft = len(e.history), nil
	activeEd = e
	e.render(false)
	edMu.Unlock()
	defer func() {
		edMu.Lock()
		if activeEd == e {
			activeEd = nil
		}
		rawActive = false
		if m := modalReq; m != nil { // a modal asked while we were closing
			modalReq = nil
			close(m.ready)
		}
		edMu.Unlock()
	}()

	for {
		r, _, err := In.ReadRune()
		edMu.Lock()
		if errors.Is(err, ErrWake) {
			if e.takeModal() || !wakeWanted {
				edMu.Unlock()
				continue
			}
		}
		out, done, rerr := e.key(r, err)
		edMu.Unlock()
		if done {
			return out, rerr
		}
	}
}

func (e *Editor) busy() bool { return e.Busy != nil && e.Busy() }

func (e *Editor) interrupt() {
	if e.OnInterrupt != nil {
		go e.OnInterrupt() // never under the screen lock
	}
}

// submit ends the Read with the buffer.
func (e *Editor) submit() (string, bool, error) {
	out := string(e.buf)
	e.render(true)
	emit("\r\n")
	e.remember(out)
	return out, true, nil
}

// key handles one key (called with edMu held). done ends the Read.
func (e *Editor) key(r rune, err error) (string, bool, error) {
	if errors.Is(err, ErrWake) {
		emit(e.clearSeq())
		e.keep = append([]rune(nil), e.buf...)
		wakeWanted = false
		activeEd = nil
		return "", true, ErrWake
	}
	if err != nil {
		e.render(true)
		emit("\r\n")
		return "", true, io.EOF
	}
	before := string(e.buf)
	e.hint = ""
	m := e.menu()
	switch r {
	case '\r', '\n':
		if (m.kind == "/" || m.kind == "arg") && len(m.items) > 0 && !e.pasting() {
			typed := strings.TrimSpace(string(e.buf))
			exact := false
			for _, it := range m.items {
				exact = exact || it.Name == typed || m.cmd+" "+it.Name == typed
			}
			if !exact {
				e.menuAccept(m)
				e.buf = []rune(strings.TrimRight(string(e.buf), " "))
				e.pos = len(e.buf)
			}
			return e.submit()
		}
		if m.kind == "@" && len(m.items) > 0 {
			e.menuAccept(m)
			break
		}
		if e.pasting() { // a paste without bracketed paste: keep the lines together
			e.insert([]rune{'\n'})
			break
		}
		return e.submit()
	case 3: // Ctrl-C
		switch {
		case len(e.buf) > 0:
			e.buf, e.pos = nil, 0
		case e.busy():
			e.interrupt()
			e.hint = "Stopping…"
		default:
			e.render(true)
			emit("\r\n")
			return "", true, ErrInterrupt
		}
	case 4: // Ctrl-D
		if len(e.buf) == 0 && !e.busy() {
			e.render(true)
			emit("\r\n")
			return "", true, io.EOF
		}
		e.del()
	case 127, 8:
		e.backspace()
	case 1:
		e.pos = 0
	case 5:
		e.end()
	case 2:
		e.left()
	case 6:
		e.right()
	case 11:
		e.kill(e.pos, len(e.buf))
	case 21:
		e.kill(0, e.pos)
	case 23:
		e.kill(e.wordLeft(), e.pos)
	case 25:
		e.insert(e.killed)
	case 12:
		emit("\x1b[H\x1b[2J")
		e.curRow = 0
	case 15: // Ctrl-O: the last turn's steps, exactly
		e.expand()
		return "", false, nil
	case 16:
		e.up(m)
	case 14:
		e.down(m)
	case 22: // Ctrl-V: read the system clipboard
		e.clipboardPaste()
	case 9:
		e.tab(m)
	case 27:
		e.escape(m)
	default:
		if r >= 32 {
			e.insert([]rune{r})
		}
	}
	if string(e.buf) != before {
		e.sel = 0
	}
	e.render(false)
	return "", false, nil
}

// pasting reports whether a newline arrived with more text right behind it (a
// paste in a terminal without bracketed paste).
func (e *Editor) pasting() bool {
	if In.Buffered() == 0 {
		return false
	}
	b, err := In.Peek(1)
	return err == nil && b[0] != 0x1b && b[0] != 3
}

func (e *Editor) up(m menuState) {
	if m.kind != "" && len(m.items) > 0 {
		e.sel--
		e.clampSel(len(m.items))
		return
	}
	e.histPrev()
}

func (e *Editor) down(m menuState) {
	if m.kind != "" && len(m.items) > 0 {
		e.sel++
		e.clampSel(len(m.items))
		return
	}
	e.histNext()
}

func (e *Editor) tab(m menuState) {
	if m.kind != "" {
		e.menuAccept(m)
		return
	}
	if g := e.ghost(); g != "" {
		e.insert([]rune(g))
	}
}

// expand prints the last turn's steps with their exact commands above the box.
func (e *Editor) expand() { writeAbove(expandLines()) }

// clipboardPaste is Ctrl+V: text goes in as one block, a picture is attached.
func (e *Editor) clipboardPaste() {
	c, err := ReadClipboard()
	var nc ErrNoClipboard
	switch {
	case errors.As(err, &nc):
		e.hint = nc.Hint
	case err != nil:
		e.hint = "Couldn't read the clipboard: " + err.Error()
	case len(c.Image) > 0:
		if e.OnImage == nil {
			e.hint = "The clipboard holds a picture; this prompt can't attach pictures"
			return
		}
		if ins := e.OnImage(c.Image); ins != "" {
			e.insertPaste(ins)
		} else {
			e.hint = "Couldn't save the pasted picture"
		}
	case c.Text == "":
		e.hint = "The clipboard is empty"
	default:
		e.insertPaste(c.Text)
	}
}

// insertPaste puts pasted text in as one block (newlines kept, nothing submitted).
func (e *Editor) insertPaste(s string) { e.insert([]rune(normalizePaste(s))) }

// clear wipes the drawn prompt so something can print where it was.
func (e *Editor) clear() { emit(e.clearSeq()) }

// clearSeq moves to the top of what the editor drew and erases it.
func (e *Editor) clearSeq() string {
	s := ""
	if e.curRow > 0 {
		s = "\x1b[" + itoa(e.curRow) + "A"
	}
	e.curRow = 0
	return s + "\r\x1b[J"
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
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

// ghost is the dim completion shown after the cursor when it's at the end: the
// newest history entry that starts with what's typed (commands use the menu).
func (e *Editor) ghost() string {
	if e.pos != len(e.buf) || len(e.buf) == 0 {
		return ""
	}
	s := string(e.buf)
	if strings.HasPrefix(s, "/") {
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

func (e *Editor) escape(m menuState) {
	if In.Buffered() == 0 { // a lone Esc key
		switch {
		case m.kind != "":
			e.menuOff, e.menuOffAt = true, string(e.buf)
		case e.busy():
			e.interrupt()
			e.hint = "Stopping…"
		}
		return
	}
	r, _, _ := In.ReadRune()
	switch r {
	case '[', 'O':
		seq := ""
		for {
			c, _, err := In.ReadRune()
			if err != nil {
				return
			}
			seq += string(c)
			if c >= 0x40 && c <= 0x7e {
				break
			}
		}
		e.csi(seq, m)
	case 'b', 'B':
		e.pos = e.wordLeft()
	case 'f', 'F':
		e.pos = e.wordRight()
	case 127, 8:
		e.kill(e.wordLeft(), e.pos)
	case 'd':
		e.kill(e.pos, e.wordRight())
	case '\r', '\n': // Alt+Enter: a new line
		e.insert([]rune{'\n'})
	}
}

// csi handles one CSI sequence. Anything unknown (focus reports ESC[I / ESC[O,
// mouse, kitty keys…) is swallowed, never typed.
func (e *Editor) csi(seq string, m menuState) {
	switch seq {
	case "A":
		e.up(m)
	case "B":
		e.down(m)
	case "C":
		e.right()
	case "D":
		e.left()
	case "H", "1~", "7~":
		e.pos = 0
	case "F", "4~", "8~":
		e.end()
	case "3~":
		e.del()
	case "1;5C", "1;3C", "5C":
		e.pos = e.wordRight()
	case "1;5D", "1;3D", "5D":
		e.pos = e.wordLeft()
	case "3;5~":
		e.kill(e.pos, e.wordRight())
	case "200~":
		e.paste()
	case "118;5u", "118;6u", "86;6u", "27;6;118~", "27;6;86~", "27;5;118~":
		// Ctrl+V / Ctrl+Shift+V sent as a key (kitty / modifyOtherKeys) instead of a paste
		e.clipboardPaste()
	case "13;2u", "27;2;13~", "13;5u", "27;5;13~": // Shift/Ctrl+Enter: a new line
		e.insert([]rune{'\n'})
	case "111;5u": // Ctrl+O sent as a key
		e.expand()
	case "Z":
		if e.OnShiftTab != nil {
			e.OnShiftTab()
		}
	case "I", "O": // focus in / out (input.go records them when they arrive whole)
		focused.Store(seq == "I")
		focusSeen.Store(true)
	}
}

// paste reads a bracketed paste verbatim (newlines kept, nothing submitted).
func (e *Editor) paste() {
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
	e.insertPaste(string(b))
}

// render redraws the input in place. final=true leaves a plain "› text" record
// (no box, ghost, menu or live lines) so the scrollback stays clean.
func (e *Editor) render(final bool) {
	emit(syncOn() + e.clearSeq() + e.frame(final) + syncOff())
}

// frame draws the editor from the top of its area (cursor at column 0 there) and
// leaves the cursor in the text; it sets curRow.
func (e *Editor) frame(final bool) string {
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
	cells := make([]cell, 0, len(e.buf)+16)
	for _, r := range e.buf {
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
	pos := 0 // cursor index in cells (newlines take two cells while editing)
	for i := 0; i < e.pos && i < len(e.buf); i++ {
		pos++
		if e.buf[i] == '\n' && !final {
			pos++
		}
	}
	if !final {
		extra := e.ghost()
		if extra == "" && len(e.buf) == 0 {
			extra = e.Placeholder
		}
		for _, r := range extra {
			_, w := dispRune(r)
			cells = append(cells, cell{Weak(string(r)), w})
		}
	}
	rows, cr, cc := layout(cells, n, pos)

	var sb strings.Builder
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
	if final {
		for i, r := range rows {
			if i > 0 {
				sb.WriteString("\r\n")
			}
			sb.WriteString(line(i, r))
		}
		e.curRow = len(rows) - 1
		return sb.String()
	}

	var top []string
	if live {
		top = preLines(W)
	}
	m := e.menu()
	var menuLines []string
	if boxed {
		menuLines = e.menuFrame(m, BW)
	} else if m.kind != "" {
		e.clampSel(len(m.items))
		if len(m.items) == 0 {
			menuLines = append(menuLines, "    "+Weak(m.empty))
		}
		for i, it := range m.items {
			if i == menuRows {
				break
			}
			mark := "  "
			if i == e.sel {
				mark = Accent("▸ ")
			}
			menuLines = append(menuLines, clipStyled("  "+mark+Ink(it.Name)+"  "+Sub(it.Desc), W-1))
		}
	}
	top = append(top, menuLines...)
	for _, l := range top {
		sb.WriteString(l + "\x1b[K\r\n")
	}
	foot := ""
	switch {
	case e.hint != "":
		foot = "  " + Accent("■ ") + Sub(e.hint)
	case m.kind == "/" || m.kind == "arg":
		foot = "  " + Weak("↑ ↓ select · tab complete · enter run · esc close")
	case m.kind == "@":
		foot = "  " + Weak("↑ ↓ select · tab or enter pick · esc close")
	case e.Footer != nil:
		foot = e.Footer(string(e.buf))
	}
	if boxed {
		edge := hair("┌" + strings.Repeat("─", BW-2) + "┐")
		if len(menuLines) > 0 {
			edge = hair("├" + strings.Repeat("─", BW-2) + "┤")
		}
		sb.WriteString("  " + edge + "\r\n")
		for i, r := range rows {
			sb.WriteString("  " + hair("│") + " " + line(i, r) + " " + hair("│") + "\r\n")
		}
		sb.WriteString("  " + hair("└"+strings.Repeat("─", BW-2)+"┘"))
		total := len(top) + 1 + len(rows) + 1
		if foot != "" {
			sb.WriteString("\r\n" + clipStyled(foot, W-2))
			total++
		}
		target := len(top) + 1 + cr
		if up := total - 1 - target; up > 0 {
			sb.WriteString("\x1b[" + itoa(up) + "A")
		}
		sb.WriteString("\r\x1b[" + itoa(4+pw+cc) + "C")
		e.curRow = target
		return sb.String()
	}
	for i, r := range rows {
		if i > 0 {
			sb.WriteString("\r\n")
		}
		sb.WriteString(line(i, r))
	}
	total := len(top) + len(rows)
	if foot != "" && live {
		sb.WriteString("\r\n" + clipStyled(foot, W-2))
		total++
	}
	target := len(top) + cr
	if up := total - 1 - target; up > 0 {
		sb.WriteString("\x1b[" + itoa(up) + "A")
	}
	sb.WriteString("\r")
	if c := pw + cc; c > 0 {
		sb.WriteString("\x1b[" + itoa(c) + "C")
	}
	e.curRow = target
	return sb.String()
}

var _ = unicode.IsLetter
