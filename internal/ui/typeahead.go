package ui

// The composer stays open while Blaze works (v0.7.0). From the moment a turn starts
// until it ends, a small reader owns the keyboard: what you type shows on the working
// line ("› your words▏"), Enter queues it as your next message (it runs right after
// the turn, in order, like Blaze Desktop's composer), and whatever is half-typed when
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
	queue   []string
}

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
	ta.buf = nil
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
		if r == 27 { // an escape sequence (arrows etc.): swallow it whole
			for In.Buffered() > 0 {
				c, _, err := In.ReadRune()
				if err != nil || (c >= 0x40 && c <= 0x7e && c != '[' && c != 'O') {
					break
				}
			}
			continue
		}
		if line, ok := t.key(r); ok {
			if spinners.Load() > 0 {
				Above("  " + Accent("› ") + Ink(clip(line, termWidth()-20)) + Weak("  · queued, Blaze reads it next"))
			}
		}
	}
}

// key edits the typed line; it reports a line that Enter queued.
func (t *typeahead) key(r rune) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch r {
	case '\r', '\n':
		line := strings.TrimSpace(string(t.buf))
		t.buf = nil
		if line == "" {
			return "", false
		}
		t.queue = append(t.queue, line)
		return line, true
	case 127, 8:
		if n := len(t.buf); n > 0 {
			t.buf = t.buf[:n-1]
		}
	case 21: // Ctrl-U
		t.buf = nil
	case 23: // Ctrl-W
		i := len(t.buf)
		for i > 0 && t.buf[i-1] == ' ' {
			i--
		}
		for i > 0 && t.buf[i-1] != ' ' {
			i--
		}
		t.buf = t.buf[:i]
	default:
		if r >= 32 && r != utf8.RuneError {
			t.buf = append(t.buf, r)
		}
	}
	return "", false
}

// typeaheadTail is the composer on the working line.
func typeaheadTail() string {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	if !ta.on {
		return ""
	}
	q := ""
	if n := len(ta.queue); n > 0 {
		q = Weak("  ·  ") + Accent(strconv.Itoa(n)+" queued")
	}
	if len(ta.buf) == 0 {
		return Weak("  ·  › type to queue a message") + q
	}
	s := ta.buf
	if len(s) > 32 {
		s = append([]rune("…"), s[len(s)-31:]...)
	}
	return Weak("  ·  ") + Accent("› ") + Ink(string(s)) + Accent("▏") + q
}
