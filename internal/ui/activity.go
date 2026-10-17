package ui

// The working line and the collapsed turn (v0.7.0, Blaze Desktop's "Worked · N
// steps"). While a turn runs, only a plain-words status shows ("Reading main.go");
// the exact commands, tool calls, outputs and diffs are kept in an Activity. When the
// turn ends one dim line sums it up, and Ctrl+O at the prompt expands the last turn's
// details above the prompt (and collapses them again) without touching what you are
// typing.

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/lacksonkapapahue/blaze-cli/internal/steps"
)

// ActStep is one tool call of a turn.
type ActStep struct {
	Plain  string // "Reading main.go"
	Detail string // the exact call: "$ cat main.go", or the tool and its arguments
	Result string // what it returned
	OK     bool
	Took   time.Duration
	// A file change it made, drawn as a diff when expanded.
	DiffPath, Before, After string
}

// Activity is what a turn did, for Ctrl+O.
type Activity struct {
	Steps   []ActStep
	Seconds float64
}

var (
	actMu       sync.Mutex
	lastAct     *Activity
	actShown    int  // lines the expanded view drew (0 = collapsed)
	actTouched  bool // something printed above the prompt since it was expanded
	actHeightFn = func() int {
		if _, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && h > 4 {
			return h
		}
		return 24
	}
)

// SetActivity records the turn that just finished (collapsed).
func SetActivity(a *Activity) {
	actMu.Lock()
	lastAct, actShown, actTouched = a, 0, false
	actMu.Unlock()
}

// LastActivity is the last finished turn's activity (nil when it ran no tools).
func LastActivity() *Activity {
	actMu.Lock()
	defer actMu.Unlock()
	return lastAct
}

// touchActivity notes output above the prompt (an expanded view can't be erased then).
func touchActivity() {
	w1BreakSteps() // something printed between step marks: the gutter starts over
	actMu.Lock()
	if actShown > 0 {
		actTouched = true
	}
	actMu.Unlock()
}

// Worked is the dim line a turn collapses to: "── Worked · 5 steps · 12s · ctrl+o to
// expand · model". With no steps it is the plain footer.
func Worked(route string, n int, took time.Duration) {
	workedChips(route, n, took, TurnChips{})
}

// WorkedTurn is Worked for a finished turn's activity: the line earns its chips
// ("+12 −3 · 4 files · tests ✓"), plays the finish sweep, and the turn is added to
// the session recap. It also ends the working line's turn state.
func WorkedTurn(route string, a *Activity, took time.Duration) {
	c := ChipsOf(a)
	n := 0
	if a != nil {
		n = len(a.Steps)
	}
	RecordSessionTurn(c.Paths)
	EndTurn()
	workedChips(route, n, took, c)
}

func workedChips(route string, n int, took time.Duration, c TurnChips) {
	w1BreakSteps()
	if n == 0 {
		Footer(route, took)
		return
	}
	W := termWidth()
	base := strings.TrimSuffix(workedText(n, took.Seconds()), " · ctrl+o to expand")
	hint, rt := " · ctrl+o to expand", ""
	if route != "" {
		rt = " · " + route
	}
	chips := w1ChipParts(c, true)
	plainChips := w1ChipParts(c, false)
	// narrow terminal: drop the model, then the hint, then the chips from the end
	fits := func() bool {
		w := 6 + len([]rune(base+hint+rt))
		for _, p := range plainChips {
			w += 3 + width(p)
		}
		return w <= W-1
	}
shrink:
	for !fits() {
		switch {
		case rt != "":
			rt = ""
		case hint != "":
			hint = ""
		case len(plainChips) > 0:
			plainChips, chips = plainChips[:len(plainChips)-1], chips[:len(chips)-1]
		default:
			base = clip(base, max(8, W-8))
			break shrink
		}
	}
	tail := ""
	for _, ch := range chips {
		tail += Weak(" · ") + ch
	}
	tail += Weak(hint + rt)
	Println("")
	if Animate() {
		w1Sweep(base)
	}
	Println("  " + hair("──") + " " + Weak(base) + tail)
}

// w1Sweep is the finish moment: an accent band runs once along "Worked · …" and
// settles into the dim line (about a quarter second). Held under dockMu so a line
// printed over a live composer can't land in the middle of it.
func w1Sweep(text string) {
	r := []rune(text)
	dockMu.Lock()
	defer dockMu.Unlock()
	hideCursor()
	defer ShowCursor()
	for p := 0; p <= len(r)+4; p += 2 {
		var b strings.Builder
		for k, c := range r {
			col := pal.weak
			if d := p - k; d >= 0 && d < 5 {
				col = mix(pal.accent, pal.weak, float64(d)/5)
			}
			b.WriteString(fg(col) + string(c))
		}
		Printf("\r  %s %s%s", fg(mix(pal.accent, pal.hair, float64(p)/float64(len(r)+4)))+"──", b.String(), reset())
		sleep(14)
	}
	Printf("\r\x1b[2K")
}

func workedText(n int, secs float64) string {
	return steps.WorkedLine(n, secs) + " · ctrl+o to expand"
}

// activityLines draws a turn's details: each step's plain phrase, its exact call,
// a few lines of what it returned, and its diff.
func activityLines(a *Activity, w int) []string {
	if w < 20 {
		w = 20
	}
	sum := strings.TrimPrefix(steps.WorkedLine(len(a.Steps), a.Seconds), "Worked · ")
	out := []string{AccentLabel("last steps") + Weak("  "+sum+" · ctrl+o to collapse")}
	for si, s := range a.Steps {
		// the hairline gutter joins each mark to the next (the last step's details hang free)
		ind := "    "
		if si < len(a.Steps)-1 {
			ind = hair("│") + "   "
		}
		mark := Accent("■")
		if !s.OK {
			mark = Ink("□")
		}
		d := ""
		if s.Took >= 100*time.Millisecond {
			d = Weak(fmt.Sprintf(" · %.1fs", s.Took.Seconds()))
		}
		out = append(out, mark+" "+Ink(s.Plain)+d)
		for i, l := range strings.Split(strings.TrimRight(s.Detail, "\n"), "\n") {
			if i == 8 {
				out = append(out, ind+Weak("…"))
				break
			}
			out = append(out, ind+Accent(strings.ReplaceAll(l, "\t", "  ")))
		}
		res := strings.Split(strings.TrimRight(s.Result, "\n"), "\n")
		for i, l := range res {
			if i == 6 {
				out = append(out, ind+Weak(fmt.Sprintf("… %d more lines", len(res)-6)))
				break
			}
			if strings.TrimSpace(l) != "" || len(res) > 1 {
				out = append(out, ind+Sub(strings.ReplaceAll(l, "\t", "  ")))
			}
		}
		if s.DiffPath != "" {
			for _, l := range capture(func() { Diff(s.DiffPath, s.Before, s.After) }) {
				if si < len(a.Steps)-1 && strings.HasPrefix(l, "  ") {
					l = hair("│") + " " + l[2:] // keep the gutter running past the diff
				}
				out = append(out, l)
			}
		}
	}
	for i, l := range out {
		out[i] = clipStyled("  "+l, w-1)
	}
	return out
}

// capture runs fn with output going to a buffer and returns its lines.
func capture(fn func()) []string {
	var buf bytes.Buffer
	outMu.Lock()
	old := Out
	Out = bufio.NewWriter(&buf)
	outMu.Unlock()
	fn()
	outMu.Lock()
	Out.Flush()
	Out = old
	outMu.Unlock()
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}

// toggleActivity is Ctrl+O at the prompt. The prompt has been cleared (cursor where
// it was drawn) and is redrawn after; called with edMu held.
func toggleActivity() {
	actMu.Lock()
	defer actMu.Unlock()
	if actShown > 0 {
		n := actShown
		actShown = 0
		if !actTouched && n < actHeightFn()-1 {
			Printf("\x1b[%dA\r\x1b[J", n) // erase the expanded view
		} else {
			Printf("  %s\r\n", Weak("details collapsed · ctrl+o to expand again"))
		}
		return
	}
	if lastAct == nil || len(lastAct.Steps) == 0 {
		Printf("  %s\r\n", Weak("No steps to show yet: Blaze hasn't used any tools in this session."))
		return
	}
	lines := activityLines(lastAct, termWidth())
	for _, l := range lines {
		Printf("%s\r\n", l)
	}
	actShown, actTouched = len(lines), false
}
