package ui

// Turn rhythm (v0.12.0): what the working line knows about the turn around it (its
// step count, elapsed time and the checklist's progress), the chips a finished turn
// earns, the session's running totals for the goodbye recap, and the welcome
// card's greeting and "pick up where you left off" row. Hosts feed it through the
// small exported hooks below; nothing here draws unless a turn has begun.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/steps"
)

// ---------- the running turn ----------

var (
	w1TurnMu        sync.Mutex
	w1TurnActive    bool
	w1TurnStarted   time.Time
	w1TurnStep      int  // the step being worked on (1-based), 0 = none yet
	w1TodoDone      int  // checklist progress for the goal-gradient rail
	w1TodoTotal     int  // 0 = no checklist known
	w1StepContinues bool // the last thing printed was a step mark (for the │ gutter)
)

// BeginTurn marks the start of a user turn: the working line counts its steps and
// shows the turn's elapsed time from here (not the current spinner's).
func BeginTurn() {
	w1TurnMu.Lock()
	w1TurnActive, w1TurnStarted, w1TurnStep, w1TodoDone, w1TodoTotal, w1StepContinues = true, time.Now(), 0, 0, 0, false
	w1TurnMu.Unlock()
}

// EndTurn clears the turn state (safe to call twice).
func EndTurn() {
	w1TurnMu.Lock()
	w1TurnActive, w1TurnStep, w1TodoDone, w1TodoTotal, w1StepContinues = false, 0, 0, 0, false
	w1TurnMu.Unlock()
}

// SetTurnStep sets the live step count ("step 3") on the working line.
func SetTurnStep(n int) {
	w1TurnMu.Lock()
	w1TurnStep = n
	w1TurnMu.Unlock()
}

// SetTurnProgress feeds the checklist's progress to the rail ("■■■□□ 3/5").
// total 0 hides it. The checklist card breaks the step gutter.
func SetTurnProgress(done, total int) {
	w1TurnMu.Lock()
	w1TodoDone, w1TodoTotal, w1StepContinues = done, total, false
	w1TurnMu.Unlock()
}

type w1TurnView struct {
	active      bool
	el          time.Duration
	step        int
	done, total int
}

func w1TurnSnapshot() w1TurnView {
	w1TurnMu.Lock()
	defer w1TurnMu.Unlock()
	v := w1TurnView{active: w1TurnActive, step: w1TurnStep, done: w1TodoDone, total: w1TodoTotal}
	if w1TurnActive {
		v.el = time.Since(w1TurnStarted)
	}
	return v
}

// w1BreakSteps notes output that isn't a step mark: the next mark starts a new run.
func w1BreakSteps() {
	w1TurnMu.Lock()
	w1StepContinues = false
	w1TurnMu.Unlock()
}

// w1StepGutter reports whether a step mark follows another directly (draw │ first),
// and records that this one was printed.
func w1StepGutter() bool {
	w1TurnMu.Lock()
	defer w1TurnMu.Unlock()
	if !w1TurnActive {
		return false
	}
	c := w1StepContinues
	w1StepContinues = true
	return c
}

// ---------- goal-gradient rail ----------

const w1RailMax = 10

// w1RailCells is the rail as plain marks, scaled to at most w1RailMax cells: "■■■□□".
func w1RailCells(done, total int) (filled, cells int) {
	if total <= 0 {
		return 0, 0
	}
	done = max(0, min(done, total))
	cells = min(total, w1RailMax)
	filled = done * cells / total
	if done > 0 && filled == 0 {
		filled = 1 // any progress shows
	}
	if done < total && filled == cells {
		filled = cells - 1 // full only when done
	}
	return filled, cells
}

// RailText is the plain rail with its count: "■■■□□ 3/5" ("" with no checklist).
func RailText(done, total int) string {
	f, n := w1RailCells(done, total)
	if n == 0 {
		return ""
	}
	return strings.Repeat("■", f) + strings.Repeat("□", n-f) + fmt.Sprintf(" %d/%d", max(0, min(done, total)), total)
}

// w1Rail is RailText in the palette: filled marks in the accent, the rest hairline.
func w1Rail(done, total int) string {
	f, n := w1RailCells(done, total)
	if n == 0 {
		return ""
	}
	return Accent(strings.Repeat("■", f)) + hair(strings.Repeat("□", n-f)) + Weak(fmt.Sprintf(" %d/%d", max(0, min(done, total)), total))
}

// ---------- phases ----------

// Phase kinds for Spinner.StartPhase (the same words steps.PhaseOf returns).
const (
	PhaseThinking  = steps.PhaseThinking
	PhaseReading   = steps.PhaseReading
	PhaseSearching = steps.PhaseSearching
	PhaseEditing   = steps.PhaseEditing
	PhaseRunning   = steps.PhaseRunning
)

// w1Shimmer colours one rune of the working line for its phase at tick i. Thinking
// breathes the whole line softly; doing glides a glint through it (reading at a
// walk, running twice as fast, searching back and forth, editing tinted toward the
// accent).
func w1Shimmer(phase string, i, k, n int) string {
	if phase == PhaseThinking {
		// slow breath, ~2s a cycle, with a gentle ripple so it reads as alive
		t := float64((i+k/3)%24) / 12
		if t > 1 {
			t = 2 - t
		}
		return mix(pal.weak, pal.sub, 0.35+0.65*t)
	}
	g := w1GlintPos(phase, i, n)
	d := k - g + 2
	if d < 0 {
		d = -d
	}
	if d >= 3 {
		return pal.sub
	}
	hi := pal.ink
	if phase == PhaseEditing {
		hi = mix(pal.accent, pal.ink, 0.45)
	}
	return mix(hi, pal.sub, float64(d)/3)
}

func w1GlintPos(phase string, i, n int) int {
	span := n + 8
	switch phase {
	case PhaseRunning:
		return (i * 2) % span
	case PhaseSearching:
		p := i % (2 * span)
		if p >= span {
			p = 2*span - 1 - p
		}
		return p
	}
	return i % span
}

// ---------- finish chips ----------

// TurnChips is what a finished turn changed: lines added and removed, files
// touched, and the last test run (Tests: "pass", "fail" or "" for none).
type TurnChips struct {
	Added, Removed, Files int
	Tests                 string
	Paths                 []string
}

// ChipsOf works the chips out from a turn's activity (its diffs and test runs).
func ChipsOf(a *Activity) TurnChips {
	var c TurnChips
	if a == nil {
		return c
	}
	seen := map[string]bool{}
	for _, s := range a.Steps {
		if s.DiffPath != "" {
			ad, rm := w1CountChanges(s.Before, s.After)
			c.Added += ad
			c.Removed += rm
			if !seen[s.DiffPath] {
				seen[s.DiffPath] = true
				c.Paths = append(c.Paths, s.DiffPath)
			}
		}
		if strings.HasPrefix(s.Plain, "Running the tests") {
			c.Tests = map[bool]string{true: "pass", false: "fail"}[s.OK]
		}
	}
	c.Files = len(c.Paths)
	return c
}

// w1CountChanges counts lines added and removed between two texts (as a multiset:
// moved lines don't count, which is close enough for a chip).
func w1CountChanges(before, after string) (added, removed int) {
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimRight(s, "\n"), "\n")
	}
	have := map[string]int{}
	for _, l := range split(before) {
		have[l]++
	}
	for _, l := range split(after) {
		if have[l] > 0 {
			have[l]--
		} else {
			added++
		}
	}
	for _, n := range have {
		removed += n
	}
	return added, removed
}

// ChipsText is the chips in plain words: "+12 −3 · 4 files · tests ✓".
func ChipsText(c TurnChips) string {
	return strings.Join(w1ChipParts(c, false), " · ")
}

func w1ChipParts(c TurnChips, styled bool) []string {
	st := func(f func(string) string, s string) string {
		if styled {
			return f(s)
		}
		return s
	}
	var parts []string
	switch {
	case c.Added > 0 && c.Removed > 0:
		parts = append(parts, st(Ink, fmt.Sprintf("+%d", c.Added))+" "+st(Sub, fmt.Sprintf("−%d", c.Removed)))
	case c.Added > 0:
		parts = append(parts, st(Ink, fmt.Sprintf("+%d", c.Added)))
	case c.Removed > 0:
		parts = append(parts, st(Sub, fmt.Sprintf("−%d", c.Removed)))
	}
	if c.Files > 0 {
		parts = append(parts, st(Weak, w1Plural(c.Files, "file")))
	}
	switch c.Tests {
	case "pass":
		parts = append(parts, st(Weak, "tests ")+st(Accent, "✓"))
	case "fail":
		parts = append(parts, st(Weak, "tests ")+st(Ink, "✗"))
	}
	return parts
}

func w1Plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// ---------- session totals ----------

// SessionStats is the session so far, for the goodbye recap.
type SessionStats struct {
	Tasks   int
	Files   int
	Started time.Time
}

var (
	w1StatsMu      sync.Mutex
	w1SessionStart = time.Now()
	w1SessionTasks int
	w1SessionFiles = map[string]bool{}
)

// RecordSessionTurn adds a finished turn to the session totals (WorkedTurn calls it).
func RecordSessionTurn(paths []string) {
	w1StatsMu.Lock()
	w1SessionTasks++
	for _, p := range paths {
		w1SessionFiles[p] = true
	}
	w1StatsMu.Unlock()
}

// SessionSoFar is the session so far.
func SessionSoFar() SessionStats {
	w1StatsMu.Lock()
	defer w1StatsMu.Unlock()
	return SessionStats{Tasks: w1SessionTasks, Files: len(w1SessionFiles), Started: w1SessionStart}
}

// RecapText is the goodbye recap: "3 tasks · 14 files · 6m 12s" ("" with no tasks).
func RecapText(tasks, files int, d time.Duration) string {
	if tasks <= 0 {
		return ""
	}
	parts := []string{w1Plural(tasks, "task")}
	if files > 0 {
		parts = append(parts, w1Plural(files, "file"))
	}
	parts = append(parts, w1Clock(d))
	return strings.Join(parts, " · ")
}

// w1Clock is a duration as "42s", "6m 12s" or "1h 05m".
func w1Clock(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %02dm", s/3600, (s%3600)/60)
}

// ---------- welcome ----------

var (
	w1WelcomeMu     sync.Mutex
	w1WelcomeName   string
	w1WelcomeResume string
)

// SetWelcomeName sets the name the greeting uses ("Good morning, Ana."); "" for none.
func SetWelcomeName(name string) {
	w1WelcomeMu.Lock()
	w1WelcomeName = strings.TrimSpace(name)
	w1WelcomeMu.Unlock()
}

// WelcomeResume sets the last unfinished prompt for the welcome card's "pick up
// where you left off" row ("" hides it). Call before Welcome / Redraw.
func WelcomeResume(lastPrompt string) {
	w1WelcomeMu.Lock()
	w1WelcomeResume = strings.Join(strings.Fields(lastPrompt), " ")
	w1WelcomeMu.Unlock()
}

func w1WelcomeExtras() (name, resume string) {
	w1WelcomeMu.Lock()
	defer w1WelcomeMu.Unlock()
	return w1WelcomeName, w1WelcomeResume
}

// TimeGreeting is the time-of-day headline: "Good morning." / "Good evening, Ana.".
func TimeGreeting(hour int, name string) string {
	g := "Good evening"
	switch {
	case hour >= 5 && hour < 12:
		g = "Good morning"
	case hour >= 12 && hour < 17:
		g = "Good afternoon"
	case hour >= 17 && hour < 23:
		g = "Good evening"
	default:
		g = "Working late"
	}
	if name = strings.TrimSpace(name); name != "" {
		if f := strings.Fields(name); len(f) > 0 {
			name = f[0]
		}
		return g + ", " + name + "."
	}
	return g + "."
}

// w1ResumeRow is the welcome card's pick-up row, fitted to w columns:
// "P I C K  U P   “fix the login bug…”   /resume".
func w1ResumeRow(prompt string, w int) string {
	if prompt == "" || w < 24 {
		return ""
	}
	label := AccentLabel("pick up") + "   "
	hint := "   " + Weak("/resume")
	room := w - width(label) - width(hint) - 2
	if room < 8 {
		label = ""
		room = w - width(hint) - 2
	}
	if room < 8 {
		return ""
	}
	return label + Body("“"+clip(prompt, room)+"”") + hint
}
