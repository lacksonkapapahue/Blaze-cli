package ui

// The composer stays open while Blaze works (v0.7.0). From the moment a turn starts
// until it ends, a small reader owns the keyboard: what you type shows on the working
// line ("› your words▏"), Enter sends it (OnSend answers it right away; commands
// wait in a queue and run after the turn, in order), and whatever is half-typed when
// the turn ends comes back in the prompt. A prompt that opens mid-turn (an approval,
// a question) takes the keyboard over for as long as it is open and hands it back
// after, so approvals keep working.
//
// It needs the terminal's "working" mode (Quiet: no echo, no line buffering), so it is
// off where Quiet is a no-op (Windows consoles) and when stdin or stdout isn't a
// terminal; keys typed there wait for the prompt as before.

import (
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

type typeahead struct {
	mu      sync.Mutex
	on      bool // a turn is running
	reading bool // the reader goroutine is running
	held    int  // prompts that took the keyboard over
	stop    bool
	done    chan struct{}
	buf     []rune
	cur     int // the caret, an index into buf (← → Home End move it)
	queue   []string

	// the composer's pop-up menu (dock.go)
	menuIdx  int
	menuLine string
	menuOff  string

	// big pastes shown as chips (w3_paste.go)
	pastes  []pasteSpan
	spanBuf []rune
	spanPos int
}

// OnSend, when set, takes a message sent from the composer while a turn runs
// (v0.9.1: Blaze answers right away instead of queueing it). It returns false to
// queue the line instead (commands, shell lines).
var OnSend func(line string) bool

// turnOn reports whether a turn is running (the composer is open).
func turnOn() bool { ta.mu.Lock(); defer ta.mu.Unlock(); return ta.on }

var (
	ta        typeahead
	taReading atomic.Bool // the reader may be woken (see wakeable)
)

// typeaheadOK is whether the composer can stay open on this terminal.
var typeaheadOK = func() bool { return runtime.GOOS != "windows" && StdinTTY() && IsTTY() }

// StartTypeahead keeps the composer open while a turn runs. Call it after Quiet.
func StartTypeahead() {
	if !typeaheadOK() {
		return
	}
	ta.mu.Lock()
	ta.on = true
	ta.mu.Unlock()
	ta.spawn()
}

// StopTypeahead takes the keyboard back when the turn ends and returns what was
// half-typed (for the prompt). Queued messages stay queued for NextQueued.
func StopTypeahead() string {
	ta.mu.Lock()
	if !ta.on {
		ta.mu.Unlock()
		return ""
	}
	ta.on = false
	ta.mu.Unlock()
	ta.halt()
	ta.mu.Lock()
	defer ta.mu.Unlock()
	rest := string(ta.buf)
	ta.buf, ta.cur, ta.pastes, ta.spanBuf = nil, 0, nil, nil
	return rest
}

// NextQueued pops the oldest message typed while Blaze was working.
func NextQueued() (string, bool) {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	if len(ta.queue) == 0 {
		return "", false
	}
	s := ta.queue[0]
	ta.queue = ta.queue[1:]
	return s, true
}

// Queued is how many messages wait.
func Queued() int { ta.mu.Lock(); defer ta.mu.Unlock(); return len(ta.queue) }

// Queue adds a message as if it was typed mid-turn (tests, and hosts that queue).
func Queue(s string) {
	if s = strings.TrimSpace(s); s != "" {
		ta.mu.Lock()
		ta.queue = append(ta.queue, s)
		ta.mu.Unlock()
	}
}

// holdKeys gives the keyboard to a prompt that opens mid-turn; the returned func
// gives it back. Outside a turn it does nothing.
func holdKeys() func() {
	ta.mu.Lock()
	if !ta.on {
		ta.mu.Unlock()
		return func() {}
	}
	ta.held++
	ta.mu.Unlock()
	ta.halt()
	return func() {
		ta.mu.Lock()
		ta.held--
		ta.mu.Unlock()
		ta.spawn()
	}
}

// Keep puts text into the prompt for the next Read (after what a wake kept).
func (e *Editor) Keep(s string) {
	if s == "" {
		return
	}
	edMu.Lock()
	e.keep = append(e.keep, []rune(s)...)
	edMu.Unlock()
}

func (t *typeahead) spawn() {
	t.mu.Lock()
	if !t.on || t.held > 0 || t.reading {
		t.mu.Unlock()
		return
	}
	t.reading, t.stop = true, false
	t.done = make(chan struct{})
	done := t.done
	taReading.Store(true)
	t.mu.Unlock()
	pasteMode(true) // a paste arrives whole, so its newlines don't send it line by line
	go t.loop(done)
}

// halt stops the reader and waits until it no longer reads the keyboard.
func (t *typeahead) halt() {
	t.mu.Lock()
	t.stop = true
	reading, done := t.reading, t.done
	t.mu.Unlock()
	if !reading {
		return
	}
	select {
	case stdinR.wake <- struct{}{}:
	default:
	}
	<-done
	select { // a wake the reader didn't need
	case <-stdinR.wake:
	default:
	}
}

func (t *typeahead) loop(done chan struct{}) {
	defer func() {
		pasteMode(false)
		taReading.Store(false)
		t.mu.Lock()
		t.reading = false
		t.mu.Unlock()
		close(done)
	}()
	for {
		t.mu.Lock()
		stop := t.stop
		t.mu.Unlock()
		if stop {
			return
		}
		r, _, err := In.ReadRune()
		if err != nil {
			return // woken (a prompt wants the keys, or the turn ended) or stdin closed
		}
		if r == 27 { // Esc, or an escape sequence: ↑ ↓ move the menu, the rest is swallowed whole
			seq := ""
			for In.Buffered() > 0 {
				c, _, err := In.ReadRune()
				if err != nil {
					break
				}
				seq += string(c)
				if c >= 0x40 && c <= 0x7e && c != '[' && c != 'O' {
					break
				}
			}
			if seq == "[200~" { // bracketed paste: insert it whole, newlines and all
				text := readPaste()
				if text == "" { // Windows Terminal pastes an image-only clipboard as nothing
					t.clipPaste(true)
					continue
				}
				t.mu.Lock()
				t.paste(text)
				t.mu.Unlock()
				continue
			}
			k := map[string]string{"": "esc", "[A": "up", "OA": "up", "[B": "down", "OB": "down",
				"[C": "right", "OC": "right", "[D": "left", "OD": "left", "[H": "home", "OH": "home", "[1~": "home", "[7~": "home",
				"[F": "end", "OF": "end", "[4~": "end", "[8~": "end", "[3~": "del"}[seq]
			if k != "" {
				t.mu.Lock()
				if used, _ := t.dockKey(k); !used {
					t.move(k)
				}
				t.mu.Unlock()
			}
			continue
		}
		if r == 22 { // Ctrl+V: a clipboard image as a chip, else its text (w3_paste.go)
			t.clipPaste(false)
			continue
		}
		if line, ok := t.key(r); ok {
			t.send(line)
		}
	}
}

// send hands a line Enter sent to OnSend (Blaze answers now), or queues it.
func (t *typeahead) send(line string) {
	RecordRecent(line)
	if OnSend != nil && OnSend(line) {
		return
	}
	t.mu.Lock()
	t.queue = append(t.queue, line)
	t.mu.Unlock()
	if spinners.Load() > 0 {
		Above(UserLines(line, "runs when this step is done")...)
	}
}

// typeKey is a key as the reader handles it (tests).
func (t *typeahead) typeKey(r rune) {
	if line, ok := t.key(r); ok {
		t.send(line)
	}
}

// move handles the caret keys. Called with t.mu held.
func (t *typeahead) move(k string) {
	if t.cur > len(t.buf) || t.cur < 0 {
		t.cur = len(t.buf)
	}
	t.fixSpans()
	if t.chip(k) {
		return
	}
	switch k {
	case "left":
		if t.cur > 0 {
			t.cur--
		}
	case "right":
		if t.cur < len(t.buf) {
			t.cur++
		}
	case "home":
		t.cur = 0
	case "end":
		t.cur = len(t.buf)
	case "del":
		if t.cur < len(t.buf) {
			t.buf = append(t.buf[:t.cur], t.buf[t.cur+1:]...)
		}
	}
}

// key edits the typed line; it reports a line that Enter sent.
func (t *typeahead) key(r rune) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fixSpans()
	if t.cur > len(t.buf) || t.cur < 0 {
		t.cur = len(t.buf)
	}
	switch r {
	case '\t':
		t.dockKey("tab")
		return "", false
	case '\r', '\n':
		if used, submit := t.dockKey("enter"); used && !submit {
			return "", false // filled in from the menu; Enter again sends it
		}
		line := strings.TrimSpace(string(t.buf))
		t.buf, t.cur = nil, 0
		if line == "" {
			return "", false
		}
		return line, true
	case 127, 8:
		if t.chip("backspace") {
			break
		}
		if t.cur > 0 {
			t.buf = append(t.buf[:t.cur-1], t.buf[t.cur:]...)
			t.cur--
		}
	case 1: // Ctrl-A
		t.cur = 0
	case 5: // Ctrl-E
		t.cur = len(t.buf)
	case 2: // Ctrl-B
		t.move("left")
	case 6: // Ctrl-F
		t.move("right")
	case 21: // Ctrl-U: everything before the caret
		t.buf = append([]rune(nil), t.buf[t.cur:]...)
		t.cur = 0
	case 23: // Ctrl-W: the word before the caret
		i := t.cur
		for i > 0 && t.buf[i-1] == ' ' {
			i--
		}
		for i > 0 && t.buf[i-1] != ' ' {
			i--
		}
		t.buf = append(t.buf[:i], t.buf[t.cur:]...)
		t.cur = i
	default:
		if r >= 32 && r != utf8.RuneError {
			t.buf = append(t.buf[:t.cur], append([]rune{r}, t.buf[t.cur:]...)...)
			t.cur++
		}
	}
	return "", false
}

// typeaheadTail is the composer on the working line.
func typeaheadTail() string {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	if !ta.on || dockUsable() {
		return "" // the composer box under the working line shows it (dock.go)
	}
	q := ""
	if n := len(ta.queue); n > 0 {
		q = Weak("  ·  ") + Accent(strconv.Itoa(n)+" waiting")
	}
	if len(ta.buf) == 0 {
		return Weak("  ·  › message Blaze, it answers while it works") + q
	}
	s := ta.buf
	if len(s) > 32 {
		s = append([]rune("…"), s[len(s)-31:]...)
	}
	return Weak("  ·  ") + Accent("› ") + Ink(string(s)) + Accent("▏") + q
}
