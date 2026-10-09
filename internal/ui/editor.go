package ui

// A small line editor so the prompt behaves like a real shell prompt:
// ← → Home End, word jumps, ↑ ↓ history, Tab completion, ghost suggestions,
// bracketed paste and Ctrl shortcuts. Falls back to plain reads when piped.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"golang.org/x/term"
)

// ErrInterrupt is returned when Ctrl-C is pressed on an empty prompt.
var ErrInterrupt = errors.New("interrupt")

type Editor struct {
	HistoryFile string   // persisted history, one entry per line ("" = memory only)
	Commands    []string // slash commands for Tab / ghost completion
	Placeholder string   // dim hint shown while the line is empty

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

	e.buf, e.pos, e.prompt, e.curRow = nil, 0, prompt, 0
	e.histIdx, e.draft = len(e.history), nil
	e.render(false)

	for {
		r, _, err := In.ReadRune()
		if err != nil {
			e.render(true)
			Printf("\r\n")
			return "", io.EOF
		}
		switch r {
		case '\r', '\n':
			out := string(e.buf)
			e.render(true)
			Printf("\r\n")
			e.remember(out)
			return out, nil
		case 3: // Ctrl-C
			if len(e.buf) > 0 {
				e.buf, e.pos = nil, 0
				break
			}
			e.render(true)
			Printf("\r\n")
			return "", ErrInterrupt
		case 4: // Ctrl-D
			if len(e.buf) == 0 {
				e.render(true)
				Printf("\r\n")
				return "", io.EOF
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
			Printf("\x1b[H\x1b[2J")
			e.curRow = 0
		case 16:
			e.histPrev()
		case 14:
			e.histNext()
		case 9:
			e.tab()
		case 27:
			e.escape()
		default:
			if r >= 32 || r == '\t' {
				e.insert([]rune{r})
			}
		}
		e.render(false)
	}
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

func (e *Editor) escape() {
	if In.Buffered() == 0 {
		return // a lone Esc key
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
		e.csi(r, seq)
	case 'b', 'B':
		e.pos = e.wordLeft()
	case 'f', 'F':
		e.pos = e.wordRight()
	case 127, 8:
		e.kill(e.wordLeft(), e.pos)
	case 'd':
		e.kill(e.pos, e.wordRight())
	}
}

func (e *Editor) csi(intro rune, seq string) {
	switch seq {
	case "A":
		e.histPrev()
	case "B":
		e.histNext()
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
	}
	_ = intro
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
	s := strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r", "\n")
	s = strings.TrimRight(s, "\n")
	e.insert([]rune(s))
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

// render redraws the prompt line in place. final=true drops the ghost and hint.
func (e *Editor) render(final bool) {
	W := termWidth()
	var sb strings.Builder
	if e.curRow > 0 {
		fmt.Fprintf(&sb, "\x1b[%dA", e.curRow)
	}
	sb.WriteString("\r\x1b[J")
	sb.WriteString(e.prompt)
	cols := width(e.prompt)
	curCols := cols
	for i, r := range e.buf {
		if i == e.pos {
			curCols = cols
		}
		s, w := dispRune(r)
		// a wide rune never splits across the edge
		if w == 2 && (cols%W) == W-1 {
			sb.WriteString(" ")
			cols++
		}
		if r == '\n' || r == '\t' {
			sb.WriteString(s + fg(pal.ink))
		} else {
			sb.WriteString(s)
		}
		cols += w
	}
	if e.pos >= len(e.buf) {
		curCols = cols
	}
	if !final {
		if g := e.ghost(); g != "" {
			g = clip(g, W-(cols%W)-1)
			sb.WriteString(Weak(g))
			cols += width(g)
		} else if len(e.buf) == 0 && e.Placeholder != "" {
			p := clip(e.Placeholder, W-cols-1)
			sb.WriteString(Weak(p))
			cols += width(p)
		}
	}
	sb.WriteString(reset())
	if cols > 0 && cols%W == 0 {
		sb.WriteString(" \r\x1b[K") // settle the pending wrap
	}
	endRow := cols / W
	row, col := curCols/W, curCols%W
	if endRow > row {
		fmt.Fprintf(&sb, "\x1b[%dA", endRow-row)
	}
	sb.WriteString("\r")
	if col > 0 {
		fmt.Fprintf(&sb, "\x1b[%dC", col)
	}
	e.curRow = row
	if final {
		// leave the cursor after the text so the next output starts below it
		if endRow > row {
			fmt.Fprintf(&sb, "\x1b[%dB", endRow-row)
		}
		e.curRow = endRow
	}
	Printf("%s", sb.String())
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
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return false
	}
	defer term.Restore(fd, old)
	for {
		r, _, err := In.ReadRune()
		if err != nil {
			fn("ctrl-c")
			return true
		}
		k := string(r)
		switch r {
		case '\r', '\n':
			k = "enter"
		case ' ':
			k = "space"
		case '\t':
			k = "tab"
		case 3, 4:
			k = "ctrl-c"
		case 16:
			k = "up"
		case 14:
			k = "down"
		case 27:
			k = "esc"
			if In.Buffered() > 0 {
				n, _, _ := In.ReadRune()
				if n == '[' || n == 'O' {
					seq := ""
					for {
						c, _, err := In.ReadRune()
						if err != nil {
							break
						}
						seq += string(c)
						if c >= 0x40 && c <= 0x7e {
							break
						}
					}
					k = map[string]string{"A": "up", "B": "down", "C": "right", "D": "left"}[seq]
				}
			}
		}
		if k != "" && fn(k) {
			return true
		}
	}
}

// Choose shows options inline ("  ? ▸ Yes   No") and returns the picked index.
// ← → or Tab move, Enter picks, the first letter of an option picks it, Esc picks cancel.
func Choose(prefix string, options []string, def, cancel int) int {
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
				if strings.EqualFold(k, o[:1]) {
					sel = i
					return true
				}
			}
		}
		draw()
		return false
	})
	// settle on the chosen answer only
	Printf("\r\x1b[2K%s%s\r\n\x1b[?25h", prefix, Accent("■ ")+Ink(options[sel]))
	return sel
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
