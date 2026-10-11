package ui

// Live mode: the prompt never goes away while Blaze works.
//
// In the chat (SetLive), the input box stays open and active for the whole session.
// Everything a turn prints (streamed answers, steps, diffs, agent notices) goes
// ABOVE the open box: the box is wiped, the finished lines are written into the
// scrollback, and the box is drawn again under them, with a few live lines on top
// of it: the line being streamed, running background agents (handle, plain step,
// elapsed) and the working status (sparks, plain step, elapsed, "esc to stop").
//
// Steps a turn takes are grouped: while Blaze works you see the current step in the
// status line; when text follows (or the turn ends) the group lands in the
// scrollback as one line, "Worked · N steps · 12s", and Ctrl+O (or /steps) prints the
// exact commands of the last turn.
//
// Something that must take the keyboard (an approval, a picker, a question) asks for
// it with withModal: the open box steps aside until it is done.

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	live      bool       // SetLive(true): the chat's always-open prompt
	liveTail  string     // the output line being written (no newline yet)
	liveSpin  *Spinner   // the working status shown above the box
	modalN    int        // >0: something draws straight to the terminal
	modalReq  *modalWait // a modal waiting for the open box to step aside
	modalMu   sync.Mutex // one modal at a time
	rawActive bool       // the editor has the terminal in raw mode
	queuedN   int        // messages waiting for the current turn (status line)
	agentRows []string   // cached live agent lines (AgentsHook, refreshed by the ticker)
	refreshCh = make(chan struct{}, 1)
	work      workGroup
	turnWork  [][]StepInfo // groups the current / last turn committed (Ctrl+O)
	turnStart time.Time    // when the current turn began (status elapsed)
	tickOnce  sync.Once
	termW     int // test override for the terminal width (0 = ask the terminal)
)

// AgentsHook returns the live agent lines shown above the input box. It is called
// outside the screen lock (it asks the agents manager).
var AgentsHook func() []string

type modalWait struct{ ready, done chan struct{} }

// SetLive turns the always-open prompt on for an interactive chat (no-op when
// stdin or stdout isn't a terminal).
func SetLive(on bool) {
	if on && (!StdinTTY() || !IsTTY()) {
		return
	}
	edMu.Lock()
	live = on
	tail := liveTail
	if !on {
		liveTail = ""
	}
	edMu.Unlock()
	if !on && tail != "" {
		emit(tail + reset() + "\r\n")
	}
	if on {
		tickOnce.Do(func() { go ticker() })
	}
}

// Live reports whether the always-open prompt is on.
func Live() bool { edMu.Lock(); defer edMu.Unlock(); return live }

// SetQueued sets how many messages wait for the current turn (shown in the status).
func SetQueued(n int) {
	edMu.Lock()
	queuedN = n
	edMu.Unlock()
	Refresh()
}

// emit writes straight to the terminal.
func emit(s string) {
	outMu.Lock()
	fmt.Fprint(Out, s)
	Out.Flush()
	outMu.Unlock()
}

// Printf prints, going above the open input box in live mode.
func Printf(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	edMu.Lock()
	if live && modalN == 0 && (activeEd != nil || liveTail != "" || len(work.steps) > 0) {
		liveWrite(s)
		edMu.Unlock()
		return
	}
	edMu.Unlock()
	emit(s)
}

func Println(s string) { Printf("%s\n", s) }

// ---------- output above the box ----------

// splitOutput feeds s into the line being written: it returns the lines s finished
// and the new unfinished line. Cursor and erase sequences are dropped (a carriage
// return starts the line over), colours are kept, and a line wider than w is
// wrapped into finished rows.
func splitOutput(tail, s string, w int) (lines []string, rest string) {
	var cur strings.Builder
	cur.WriteString(tail)
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch r {
		case 0x1b:
			if i+1 < len(rs) && rs[i+1] == '[' {
				j := i + 2
				for j < len(rs) && !(rs[j] >= 0x40 && rs[j] <= 0x7e) {
					j++
				}
				if j < len(rs) && rs[j] == 'm' {
					cur.WriteString(string(rs[i : j+1]))
				}
				i = j
				continue
			}
			i++ // ESC x: drop both
		case '\r':
			if i+1 < len(rs) && rs[i+1] == '\n' {
				continue
			}
			cur.Reset()
		case '\n':
			lines = append(lines, cur.String())
			cur.Reset()
		case '\t':
			cur.WriteString("    ")
		default:
			if r >= 32 && r != 0x7f {
				cur.WriteRune(r)
			}
		}
	}
	rest = cur.String()
	var out []string
	for _, l := range append(lines, rest) {
		for w > 0 && width(l) > w {
			head, tl := cutStyled(l, w)
			out = append(out, head)
			l = tl
		}
		out = append(out, l)
	}
	return out[:len(out)-1], out[len(out)-1]
}

// cutStyled splits a styled string after n visible columns; the second half starts
// with the colour that was active at the cut.
func cutStyled(s string, n int) (string, string) {
	var head strings.Builder
	col, last := 0, ""
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b && i+1 < len(rs) && rs[i+1] == '[' {
			j := i + 2
			for j < len(rs) && !(rs[j] >= 0x40 && rs[j] <= 0x7e) {
				j++
			}
			if j >= len(rs) {
				break
			}
			seq := string(rs[i : j+1])
			head.WriteString(seq)
			last = seq
			i = j
			continue
		}
		w := runeW(rs[i])
		if col+w > n {
			return head.String() + reset(), last + string(rs[i:])
		}
		head.WriteRune(rs[i])
		col += w
	}
	return head.String(), ""
}

// liveWrite takes output in live mode (edMu held).
func liveWrite(s string) {
	lines, tail := splitOutput(liveTail, s, liveWidth()-1)
	if len(work.steps) > 0 && (len(lines) > 0 || (tail != "" && liveTail == "")) {
		lines = append([]string{commitWork()}, lines...)
	}
	changed := tail != liveTail
	liveTail = tail
	if activeEd == nil {
		var b strings.Builder
		for _, l := range lines {
			b.WriteString(l + reset() + "\r\n")
		}
		if b.Len() > 0 {
			emit(b.String())
		}
		return
	}
	if len(lines) == 0 && !changed {
		return
	}
	writeAbove(lines)
}

// writeAbove wipes the open box, writes finished lines and draws the box again (edMu held).
func writeAbove(lines []string) {
	e := activeEd
	var b strings.Builder
	b.WriteString(syncOn())
	if e != nil {
		b.WriteString(e.clearSeq())
	}
	for _, l := range lines {
		b.WriteString(l + reset() + "\x1b[K\r\n")
	}
	if e != nil {
		b.WriteString(e.frame(false))
	}
	b.WriteString(syncOff())
	emit(b.String())
}

// synchronized output (DEC 2026): the terminal shows the whole redraw at once.
func syncOn() string {
	if !color {
		return ""
	}
	return "\x1b[?2026h"
}

func syncOff() string {
	if !color {
		return ""
	}
	return "\x1b[?2026l"
}

func liveWidth() int { return termWidth() }

// Refresh redraws the open prompt (live lines, footer). Safe from any goroutine.
func Refresh() {
	edMu.Lock()
	l := live
	edMu.Unlock()
	if l {
		select {
		case refreshCh <- struct{}{}:
		default:
		}
		return
	}
	edMu.Lock()
	defer edMu.Unlock()
	if activeEd != nil {
		activeEd.render(false)
	}
}

// ticker animates the status line and keeps the agents line current.
func ticker() {
	for {
		edMu.Lock()
		fast := liveSpin != nil
		edMu.Unlock()
		d := time.Second
		if fast {
			d = 100 * time.Millisecond
		}
		select {
		case <-refreshCh:
		case <-time.After(d):
		}
		var rows []string
		if AgentsHook != nil {
			rows = AgentsHook() // outside the lock
		}
		edMu.Lock()
		agentRows = rows
		if activeEd != nil && modalN == 0 {
			emit(syncOn() + activeEd.clearSeq() + activeEd.frame(false) + syncOff())
		}
		edMu.Unlock()
	}
}

// ---------- the keyboard for someone else ----------

// withModal runs fn with the terminal to itself: the open box steps aside (and comes
// back after), output goes straight to the screen meanwhile. One at a time.
func withModal(fn func()) {
	edMu.Lock()
	l := live
	edMu.Unlock()
	if !l {
		fn()
		return
	}
	modalMu.Lock()
	defer modalMu.Unlock()
	edMu.Lock()
	var m *modalWait
	if activeEd != nil {
		m = &modalWait{make(chan struct{}), make(chan struct{})}
		modalReq = m
	}
	modalN++
	edMu.Unlock()
	defer func() {
		edMu.Lock()
		modalN--
		edMu.Unlock()
		if m != nil {
			close(m.done)
		}
	}()
	if m != nil {
		select {
		case stdinR.wake <- struct{}{}:
		default:
		}
		<-m.ready
	}
	edMu.Lock()
	var pre []string
	if len(work.steps) > 0 {
		pre = append(pre, commitWork())
	}
	if liveTail != "" {
		pre = append(pre, liveTail)
		liveTail = ""
	}
	edMu.Unlock()
	for _, l := range pre {
		emit(l + reset() + "\r\n")
	}
	fn()
}

// takeModal is the open editor's side of withModal (edMu held): it steps aside,
// waits for the modal to finish and draws itself again.
func (e *Editor) takeModal() bool {
	m := modalReq
	if m == nil {
		return false
	}
	modalReq = nil
	emit(e.clearSeq())
	activeEd = nil
	edMu.Unlock()
	close(m.ready)
	<-m.done
	edMu.Lock()
	activeEd = e
	e.render(false)
	return true
}

// ---------- working steps ----------

// StepInfo is one finished tool step.
type StepInfo struct {
	OK     bool
	Plain  string        // what it did in plain words ("Running the tests")
	Exact  string        // the exact action ("$ go test ./...")
	Result string        // first line of the result
	Took   time.Duration // how long it ran
}

type workGroup struct {
	open  bool
	steps []StepInfo
	start time.Time
}

// BeginWork starts a turn's step grouping (live mode only).
func BeginWork() {
	edMu.Lock()
	work = workGroup{open: live}
	turnWork = nil
	turnStart = time.Now()
	edMu.Unlock()
}

// EndWork lands the turn's last group in the scrollback.
func EndWork() {
	edMu.Lock()
	defer edMu.Unlock()
	if len(work.steps) > 0 {
		l := commitWork()
		if activeEd != nil && modalN == 0 {
			writeAbove([]string{l})
		} else {
			emit(l + reset() + "\r\n")
		}
	}
	work.open = false
	turnStart = time.Time{}
}

// ToolStep records a finished step: grouped under "Worked · N steps" in live mode,
// printed as a step line otherwise.
func ToolStep(s StepInfo) {
	edMu.Lock()
	if work.open {
		if len(work.steps) == 0 {
			work.start = time.Now().Add(-s.Took)
		}
		work.steps = append(work.steps, s)
		edMu.Unlock()
		Refresh()
		return
	}
	edMu.Unlock()
	StepDone(s.OK, s.Exact, s.Result, s.Took)
}

// WorkSummary is the collapsed line for a group of steps.
func WorkSummary(steps []StepInfo, took time.Duration) string {
	bad := 0
	for _, s := range steps {
		if !s.OK {
			bad++
		}
	}
	mark := Accent("■")
	extra := ""
	if bad > 0 {
		mark = Ink("□")
		extra = fmt.Sprintf(" · %d failed", bad)
	}
	n := fmt.Sprintf("%d step%s", len(steps), plural(len(steps)))
	t := ""
	if took >= time.Second {
		t = " · " + took.Round(time.Second).String()
	}
	return "  " + mark + " " + Ink("Worked") + Sub(" · "+n+extra+t) + Weak("  ctrl+o expands")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// commitWork closes the current group and returns its summary line (edMu held).
func commitWork() string {
	steps := work.steps
	l := WorkSummary(steps, time.Since(work.start))
	turnWork = append(turnWork, steps)
	work.steps = nil
	return l
}

// expandLines are the last turn's steps with their exact commands (edMu held).
func expandLines() []string {
	groups := turnWork
	if len(work.steps) > 0 {
		groups = append(append([][]StepInfo{}, groups...), work.steps)
	}
	if len(groups) == 0 {
		return []string{"  " + Weak("·") + " " + Sub("No steps to show yet.")}
	}
	n := 0
	for _, g := range groups {
		n += len(g)
	}
	out := []string{"  " + Accent("▾") + " " + Ink("Worked") + Sub(fmt.Sprintf(" · %d step%s, exactly", n, plural(n)))}
	W := liveWidth()
	for _, g := range groups {
		for _, s := range g {
			mark := Accent("■")
			if !s.OK {
				mark = Ink("□")
			}
			out = append(out, "    "+mark+" "+Ink(clip(s.Plain, W-8)))
			d := ""
			if s.Took >= 100*time.Millisecond {
				d = fmt.Sprintf(" · %.1fs", s.Took.Seconds())
			}
			ex := clip(s.Exact, max(10, W-12-len(d)))
			res := ""
			if room := W - 10 - width(ex) - len(d) - 2; room > 6 && s.Result != "" {
				res = "  " + clip(s.Result, room)
			}
			out = append(out, "      "+Sub(ex)+Weak(res+d))
		}
	}
	return out
}

// ExpandWork prints the last turn's steps with their exact commands (Ctrl+O, /steps).
func ExpandWork() {
	edMu.Lock()
	lines := expandLines()
	edMu.Unlock()
	for _, l := range lines {
		Println(l)
	}
}

// ---------- live lines above the box ----------

// preLines are the live rows drawn over the input box (edMu held).
func preLines(W int) []string {
	var out []string
	if liveTail != "" {
		out = append(out, liveTail+reset())
	}
	for i, r := range agentRows {
		if i == 3 {
			out = append(out, "  "+Weak(fmt.Sprintf("+%d more agents · /agents", len(agentRows)-3)))
			break
		}
		out = append(out, clipStyled(r, W-1))
	}
	if s := liveSpin; s != nil {
		s.mu.Lock()
		txt, since, i := s.text, s.since, s.frame
		s.frame++
		s.mu.Unlock()
		if !turnStart.IsZero() {
			since = turnStart
		}
		out = append(out, liveStatus(i, txt, time.Since(since), W))
	}
	return out
}

// liveStatus is the working line: sparks, the plain step, elapsed, steps, queued, how to stop.
func liveStatus(i int, txt string, el time.Duration, W int) string {
	tail := ""
	if el >= time.Second {
		tail += "  " + Weak(el.Round(time.Second).String())
	}
	if n := len(work.steps); n > 0 {
		tail += Weak(fmt.Sprintf(" · %d step%s", n, plural(n)))
	}
	if queuedN > 0 {
		tail += Weak(" · ") + Accent(fmt.Sprintf("%d queued", queuedN))
	}
	tail += Weak(" · esc to stop")
	room := W - 6 - width(tail)
	if room < 8 {
		tail, room = "", W-6
	}
	r := []rune(clip(txt, room))
	g := i % (len(r) + 8)
	var b strings.Builder
	for k, c := range r {
		d := k - g + 2
		if d < 0 {
			d = -d
		}
		col := pal.sub
		if d < 3 {
			col = mix(pal.ink, pal.sub, float64(d)/3)
		}
		b.WriteString(fg(col) + string(c))
	}
	b.WriteString(reset())
	pulse := mix(pal.accent, pal.hair, 0.35*float64((i/2)%2))
	if os.Getenv("BLAZE_NO_ANIM") != "" {
		pulse = pal.accent
	}
	return "  " + fg(pulse) + sparks(i) + reset() + " " + b.String() + tail
}

// AgentLine is one running agent for the live line: "▸ handle · step · 42s".
func AgentLine(handle, step string, secs float64, waiting bool) string {
	mark := Accent("▸")
	if waiting {
		mark = Accent("?")
	}
	return "  " + mark + " " + Ink(handle) + Weak(" · ") + Sub(step) + Weak(" · "+(time.Duration(secs)*time.Second).String())
}
