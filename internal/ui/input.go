package ui

// The keyboard, shared by the prompt and everything that runs while it is open.
//
// One goroutine owns stdin. It strips the terminal's focus reports (ESC [ I and
// ESC [ O, switched on with FocusReporting) so Blaze knows whether you are looking
// at the terminal, and hands the rest to In. The prompt can be woken up while it
// waits for a key (Wake), which is how a finished background agent gets the main
// agent's attention while you are typing; whatever you had typed is kept and comes
// back when the prompt reopens. Lines printed with Above appear over the prompt
// without breaking it.

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ErrWake is returned by Editor.Read when Wake was called: something (a finished
// background agent) needs the main loop. The typed text is kept for the next Read.
var ErrWake = errors.New("wake")

type chunk struct {
	b   []byte
	err error
}

type stdinReader struct {
	once sync.Once
	data chan chunk
	wake chan struct{}

	mu   sync.Mutex
	left []byte
	eof  error
}

var stdinR = &stdinReader{data: make(chan chunk, 64), wake: make(chan struct{}, 1)}

// In is the keyboard. Read it through this reader only (never os.Stdin directly).
var In = bufio.NewReader(stdinR)

var (
	focused   atomic.Bool  // last focus report (true until the terminal says otherwise)
	focusSeen atomic.Bool  // the terminal sends focus reports at all
	focusAt   atomic.Int64 // unix ms of the last focus change
)

func init() { focused.Store(true) }

func (r *stdinReader) start() {
	r.once.Do(func() {
		go func() {
			for {
				buf := make([]byte, 4096)
				n, err := os.Stdin.Read(buf)
				b := stripFocus(buf[:n])
				if len(b) > 0 || err != nil {
					r.data <- chunk{b, err}
				}
				if err != nil {
					return
				}
			}
		}()
	})
}

// stripFocus removes focus reports from a chunk of input and records them.
func stripFocus(b []byte) []byte {
	if !bytes.Contains(b, []byte("\x1b[I")) && !bytes.Contains(b, []byte("\x1b[O")) {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == 0x1b && i+2 < len(b) && b[i+1] == '[' && (b[i+2] == 'I' || b[i+2] == 'O') {
			focused.Store(b[i+2] == 'I')
			focusSeen.Store(true)
			focusAt.Store(time.Now().UnixMilli())
			i += 2
			continue
		}
		out = append(out, b[i])
	}
	return out
}

func (r *stdinReader) Read(p []byte) (int, error) {
	r.start()
	r.mu.Lock()
	if len(r.left) > 0 {
		n := copy(p, r.left)
		r.left = r.left[n:]
		r.mu.Unlock()
		return n, nil
	}
	if r.eof != nil {
		err := r.eof
		r.mu.Unlock()
		return 0, err
	}
	r.mu.Unlock()
	var wake <-chan struct{}
	if wakeable() {
		wake = r.wake
	}
	select {
	case c := <-r.data:
		r.mu.Lock()
		defer r.mu.Unlock()
		if c.err != nil {
			r.eof = c.err
		}
		n := copy(p, c.b)
		r.left = append(r.left[:0], c.b[n:]...)
		if n == 0 && c.err != nil {
			return 0, c.err
		}
		return n, nil
	case <-wake:
		return 0, ErrWake
	}
}

// Focused reports whether the terminal window has focus, as far as Blaze can tell.
// Terminals that don't send focus reports always count as focused.
func Focused() bool { return focused.Load() }

// FocusKnown is true once the terminal has sent a focus report.
func FocusKnown() bool { return focusSeen.Load() }

// FocusReporting asks the terminal to report focus changes (xterm's mode 1004;
// supported by most modern terminals, and by tmux with focus-events on).
//
// While it is on, the terminal stays in Quiet mode (no echo, no line buffering;
// Ctrl-C still works) for the whole session, not only while a task runs. The
// terminal can send a focus report at any moment (while a session is being
// resumed, between prompts, during a slash command), and in the normal cooked
// mode the tty would echo it straight onto the screen as ^[[O before Blaze reads
// it. ReadLine echoes what you type itself meanwhile (readEchoed). Switching
// reporting off around a line instead would not work: terminals answer the
// switch back on with a report, which would land in cooked mode again.
func FocusReporting(on bool) {
	if !IsTTY() || !StdinTTY() || !color {
		return
	}
	focusMu.Lock()
	defer focusMu.Unlock()
	if on {
		if focusOn {
			return
		}
		focusRestore, focusQuiet = quiet() // echo off first, so no report is ever echoed
		focusOn = true
		Printf("\x1b[?1004h")
		return
	}
	if !focusOn {
		return
	}
	Printf("\x1b[?1004l")
	focusOn, focusQuiet = false, false
	if focusRestore != nil {
		focusRestore()
		focusRestore = nil
	}
}

var (
	focusMu      sync.Mutex
	focusOn      bool
	focusQuiet   bool // the session switched the terminal to Quiet mode (Unix)
	focusRestore func()
)

// quietSession is true while FocusReporting keeps the terminal in Quiet mode.
func quietSession() bool {
	focusMu.Lock()
	defer focusMu.Unlock()
	return focusQuiet
}

// ---------- the open prompt ----------

var (
	edMu       sync.Mutex
	activeEd   *Editor
	wakeWanted bool
	aboveQ     []string
	spinners   atomic.Int32
)

func wakeable() bool {
	edMu.Lock()
	defer edMu.Unlock()
	return activeEd != nil || taReading.Load() // the open prompt, or the composer under the working line (typeahead.go)
}

// Wake makes an open prompt return ErrWake (keeping what was typed). Called while no
// prompt is open, the next Read returns ErrWake at once.
func Wake() {
	edMu.Lock()
	wakeWanted = true
	open := activeEd != nil
	edMu.Unlock()
	if open {
		select {
		case stdinR.wake <- struct{}{}:
		default:
		}
	}
}

// Above prints lines over the open prompt (or over the spinner while a task runs).
// With neither on screen, the lines wait and show when the prompt next opens.
func Above(lines ...string) {
	touchActivity()
	edMu.Lock()
	defer edMu.Unlock()
	switch {
	case activeEd != nil:
		activeEd.clear()
		for _, l := range lines {
			Printf("%s\r\n", l)
		}
		activeEd.render(false)
	case spinners.Load() > 0:
		for _, l := range lines {
			Printf("\r\x1b[2K%s\n", l)
		}
	default:
		aboveQ = append(aboveQ, lines...)
	}
}

// Refresh redraws the open prompt (its footer shows live status).
func Refresh() {
	edMu.Lock()
	defer edMu.Unlock()
	if activeEd != nil {
		activeEd.render(false)
	}
}

// FlushAbove prints lines that waited for the prompt.
func FlushAbove() {
	edMu.Lock()
	q := aboveQ
	aboveQ = nil
	edMu.Unlock()
	for _, l := range q {
		Println(l)
	}
}

// StatusLine, when set, is a compact live line (background agents) added to the
// spinner while a task runs.
var StatusLine func() string
