package ui

// The terminal tab: its title (OSC 0) and taskbar progress (OSC 9;4, Windows
// Terminal / ghostty / ConEmu), and the done-while-away ping.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/notify"
)

// Taskbar progress states (OSC 9;4).
const (
	ProgressOff           = 0
	ProgressNormal        = 1
	ProgressError         = 2
	ProgressIndeterminate = 3
	ProgressPaused        = 4
)

var (
	titleMu     sync.Mutex
	titlePushed bool
	progressOn  bool
)

// titleOK: a terminal on stdout, not dumb, and not switched off (BLAZE_NO_TITLE=1).
func titleOK() bool {
	if os.Getenv("BLAZE_NO_TITLE") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return IsTTY()
}

// progressOK: only terminals known to draw OSC 9;4 as progress (elsewhere OSC 9
// can mean "show a notification"). BLAZE_PROGRESS=1 forces it on, =0 off.
func progressOK(getenv func(string) string) bool {
	switch getenv("BLAZE_PROGRESS") {
	case "0", "off":
		return false
	case "1", "on":
		return true
	}
	if getenv("WT_SESSION") != "" || getenv("ConEmuPID") != "" || getenv("ConEmuANSI") == "ON" {
		return true
	}
	tp := strings.ToLower(getenv("TERM_PROGRAM"))
	return tp == "ghostty" || strings.Contains(strings.ToLower(getenv("TERM")), "ghostty")
}

// cleanTitle drops control characters so a title can't end the sequence early.
func cleanTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		s = string(r[:79]) + "…"
	}
	return s
}

// SetTitle sets the terminal tab's title. The first call saves the old title on
// the terminal's title stack; RestoreTitle puts it back.
func SetTitle(s string) {
	if !titleOK() {
		return
	}
	titleMu.Lock()
	defer titleMu.Unlock()
	pre := ""
	if !titlePushed {
		pre = "\x1b[22;0t" // push the current title (xterm; ignored elsewhere)
		titlePushed = true
	}
	Printf("%s\x1b]0;%s\x07", pre, cleanTitle(s))
}

// TaskbarProgress shows state (Progress*) with pct (0-100) on the tab/taskbar.
func TaskbarProgress(state, pct int) {
	if !titleOK() || !progressOK(os.Getenv) {
		return
	}
	pct = min(100, max(0, pct))
	if state < ProgressOff || state > ProgressPaused {
		state = ProgressOff
	}
	titleMu.Lock()
	progressOn = state != ProgressOff
	titleMu.Unlock()
	Printf("\x1b]9;4;%d;%d\x07", state, pct)
}

// RestoreTitle clears the progress and puts back the title Blaze found. Call it
// on exit (safe to call more than once, or when nothing was set).
func RestoreTitle() {
	titleMu.Lock()
	defer titleMu.Unlock()
	if progressOn {
		Printf("\x1b]9;4;0;0\x07")
		progressOn = false
	}
	if titlePushed {
		Printf("\x1b[23;0t") // pop the title saved by SetTitle
		titlePushed = false
	}
}

func here() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Base(wd)
}

// IdleTitle is the tab while the prompt waits: "Blaze · folder".
func IdleTitle() {
	t := "Blaze"
	if h := here(); h != "" && h != "/" && h != "." {
		t += " · " + h
	}
	SetTitle(t)
}

// TurnStart marks the tab busy: "■ Blaze · working · folder" and an indeterminate
// progress bar where the terminal has one.
func TurnStart() {
	t := "■ Blaze · working"
	if h := here(); h != "" && h != "/" && h != "." {
		t += " · " + h
	}
	SetTitle(t)
	TaskbarProgress(ProgressIndeterminate, 0)
}

// AwayAfter is how long a turn must run before Blaze pings you when it ends while
// the terminal is in the background.
const AwayAfter = 20 * time.Second

// shouldPing: long enough, and the terminal says it isn't focused.
func shouldPing(took time.Duration, focused bool) bool { return took >= AwayAfter && !focused }

// pingSend is notify.Send (swapped in tests).
var pingSend = func(n notify.Note) { _, _ = notify.Send(n) }

// TurnDone ends the tab's busy state. When the turn took 20s or more and the
// terminal isn't focused (it reported focus-out), it rings the bell and sends a
// desktop notification, so you come back when Blaze is done.
func TurnDone(took time.Duration) {
	TaskbarProgress(ProgressOff, 0)
	if shouldPing(took, Focused()) {
		if IsTTY() {
			Printf("\a")
		}
		body := "Finished in " + humanDur(took)
		if h := here(); h != "" {
			body += " · " + h
		}
		go pingSend(notify.Note{Title: "Blaze · done", Body: body, Kind: "task"})
		SetTitle("✓ Blaze · done")
		return
	}
	IdleTitle()
}

// humanDur is "42s", "3m 05s", "1h 04m".
func humanDur(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %02dm", s/3600, (s%3600)/60)
}
