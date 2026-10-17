package main

// `blaze theme [name]` and `/theme [name]`: pick the accent preset (saved for next
// time), and `blaze stats` / `/stats`: the habit card.

import (
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/checkpoint"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

// runTheme shows the presets, or switches to one and saves it.
func runTheme(args []string) {
	if len(args) == 0 || args[0] == "list" {
		ui.ThemeCard()
		return
	}
	name := strings.ToLower(args[0])
	if err := ui.SaveAccent(name); err != nil {
		ui.Println("  " + ui.Ink("■ ") + ui.Sub(err.Error()))
		return
	}
	ui.ThemeCard()
	if ui.AccentFromEnv() {
		ui.Println("  " + ui.Weak("· BLAZE_ACCENT is set, so it wins over the saved choice in new sessions"))
	}
}

// themeMenu is the menu after "/theme ".
func themeMenu(q string) *ui.MenuSet {
	var opts []opt
	for _, p := range ui.Presets {
		d := p.Desc
		if p.Name == ui.AccentName() {
			d = "■ in use · " + d
		}
		opts = append(opts, opt{p.Name, d, false, ""})
	}
	opts = append(opts, opt{"list", "Every preset with a swatch", false, "/theme"})
	return optMenu("/theme ", "theme", q, opts)
}

// runStats prints the stats card.
func runStats() { ui.StatsCard() }

// turnMark is what turnBegin noted: when the turn started and the activity
// shown before it (a turn with no steps leaves the previous one in place).
type turnMark struct {
	start time.Time
	prev  *ui.Activity
}

// turnBegin marks the tab busy (title + taskbar progress).
func turnBegin() turnMark {
	ui.TurnStart()
	return turnMark{start: time.Now(), prev: ui.LastActivity()}
}

// turnFinish records a finished turn in the stats and ends the tab's busy state,
// pinging (bell + notification) when it took 20s+ and the terminal is unfocused.
// ok is false for a stopped or failed turn: those aren't counted.
func turnFinish(a *agent.Agent, t *checkpoint.Turn, m turnMark, ok bool) {
	took := time.Since(m.start)
	if ok {
		steps := 0
		if act := ui.LastActivity(); act != nil && act != m.prev {
			steps = len(act.Steps)
		}
		files := 0
		if t != nil && a != nil && a.Checkpoints != nil {
			files = len(t.Changed(a.Checkpoints.RepoTop))
		}
		ui.RecordTurn(took, steps, files)
	}
	ui.TurnDone(took)
}
