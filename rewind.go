package main

// Checkpoints and rewind (v0.8.0), like Claude Code's: every message is a
// checkpoint; /rewind (or Esc twice on an empty prompt) picks one and puts the
// code, the chat, or both back to how they were before it. /undo rewinds the last
// message's code and chat.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/agents"
	"github.com/lacksonkapapahue/blaze-cli/internal/checkpoint"
	"github.com/lacksonkapapahue/blaze-cli/internal/steps"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

// rewindNote is said when the folder isn't a git repo.
const rewindNote = "This folder isn't a git repo, so only Blaze's own file edits can be rewound; changes made by commands it ran aren't tracked."

// checkpointsOn reports whether checkpoints are on (BLAZE_CHECKPOINTS=off turns them off).
func checkpointsOn() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("BLAZE_CHECKPOINTS")))
	return v != "off" && v != "0" && v != "false"
}

// openCheckpoints starts this chat's checkpoints: the folder's last session when the
// chat was resumed, otherwise a new one.
func openCheckpoints(a *agent.Agent, resumed bool) {
	if !checkpointsOn() {
		a.Checkpoints = nil
		return
	}
	if resumed {
		if s, err := checkpoint.Latest(a.Env.Root); err == nil {
			a.Checkpoints = s
			return
		}
	}
	a.Checkpoints = checkpoint.Open(a.Env.Root)
}

// beginTurn opens a checkpoint for a turn that is about to run.
func beginTurn(a *agent.Agent, typed, prompt string) *checkpoint.Turn {
	if a.Checkpoints == nil {
		return nil
	}
	kind := "user"
	switch {
	case strings.HasPrefix(prompt, agents.ResultPrefix):
		kind, typed = "agent", ""
	case typed == "":
		kind, typed = "auto", prompt
	}
	return a.Checkpoints.Begin(kind, typed, prompt, a.HistIndex())
}

// endTurn closes it; a turn whose message never reached the chat is dropped.
func endTurn(a *agent.Agent, t *checkpoint.Turn) {
	if a.Checkpoints == nil || t == nil {
		return
	}
	a.Checkpoints.End()
	if a.HistIndex() <= t.HistIndex {
		a.Checkpoints.Discard(t)
	}
}

func turnLabel(t *checkpoint.Turn) string {
	switch t.Kind {
	case "agent":
		return "(an agent's result)"
	}
	s := t.Prompt
	if i := strings.Index(s, "\n\n<file path="); i >= 0 {
		s = s[:i]
	}
	return strings.Join(strings.Fields(s), " ")
}

func filesWord(n int) string {
	switch n {
	case 0:
		return "no file changes"
	case 1:
		return "1 file changed"
	}
	return fmt.Sprintf("%d files changed", n)
}

func when(t time.Time) string {
	if time.Since(t) > 20*time.Hour {
		return t.Format("Jan 2 15:04")
	}
	return t.Format("15:04")
}

// rewindCmd is /rewind and /undo.
func rewindCmd(a *agent.Agent, ed *ui.Editor, undo bool) {
	s := a.Checkpoints
	if s == nil {
		ui.Step("Checkpoints are off (BLAZE_CHECKPOINTS=off), so there's nothing to rewind.")
		return
	}
	turns := s.List()
	if len(turns) == 0 {
		ui.Step("Nothing to rewind yet: checkpoints start with your next message.")
		return
	}
	i := len(turns) - 1
	code, chat := true, true
	if !undo {
		if !s.Git() {
			ui.Println("  " + ui.Weak(rewindNote))
		}
		rows := make([]string, 0, len(turns))
		for k := len(turns) - 1; k >= 0; k-- {
			t := turns[k]
			extra := filesWord(len(t.Changed(s.RepoTop)))
			if !a.CanRewindChat(t) {
				extra += " · code only"
			}
			label := turnLabel(t)
			if len([]rune(label)) > 60 {
				label = string([]rune(label)[:59]) + "…"
			}
			rows = append(rows, ui.Weak(when(t.Time))+"  "+label+ui.Weak("  · "+extra))
		}
		p := ui.PickList("Rewind to before which message?", rows, 0)
		if p < 0 {
			ui.Step("Rewind cancelled.")
			return
		}
		i = len(turns) - 1 - p
		t := turns[i]
		var opts []string
		canChat := a.CanRewindChat(t)
		if canChat {
			opts = append(opts, "Code and chat", "Chat only")
		}
		opts = append(opts, "Code only", "Cancel")
		c := ui.Choose("  "+ui.Ink("Restore what?")+"  ", opts, 0, len(opts)-1)
		switch opts[c] {
		case "Code and chat":
		case "Chat only":
			code = false
		case "Code only":
			chat = false
		default:
			ui.Step("Rewind cancelled.")
			return
		}
	}
	t := turns[i]
	if chat && !a.CanRewindChat(t) {
		if undo {
			chat = false // the chat was compacted: put the code back only
		}
	}
	if code {
		if !restoreCode(a, i) {
			return
		}
		if !chat {
			s.CodeRewound(i)
		}
	}
	if chat {
		if !a.RewindChat(t) {
			ui.Step("That message is no longer in the chat (it was compacted or cleared), so only code can be rewound.")
			return
		}
		s.DropFrom(i)
		a.Save()
		if t.Kind == "user" && strings.TrimSpace(t.Prompt) != "" {
			ed.Keep(t.Prompt)
			ui.StepDone(true, "Chat rewound", "your message is back in the prompt to edit", 0)
		} else {
			ui.StepDone(true, "Chat rewound", "", 0)
		}
	}
}

// restoreCode puts the files back to how they were before turn i. It asks before
// overwriting files changed outside Blaze since then. false = cancelled.
func restoreCode(a *agent.Agent, i int) bool {
	s := a.Checkpoints
	plan, err := s.PlanRestore(i)
	if err != nil {
		ui.Step("Couldn't rewind: " + err.Error())
		return false
	}
	for _, w := range plan.Warnings {
		if w != "" && !strings.Contains(w, "isn't a git repo") {
			ui.Println("  " + ui.Weak("Note: "+w+"."))
		}
	}
	if len(plan.Items) == 0 {
		ui.Step("No file changes to undo since then.")
		return true
	}
	overwrite := false
	if cs := plan.Conflicts(); len(cs) > 0 {
		ui.Println("  " + ui.Ink(fmt.Sprintf("%d file%s changed outside Blaze since then:", len(cs), plural(len(cs)))))
		for k, p := range cs {
			if k == 8 {
				ui.Println("    " + ui.Weak(fmt.Sprintf("… and %d more", len(cs)-8)))
				break
			}
			ui.Println("    " + ui.Body(relPath(a, p)))
		}
		switch ui.Choose("  "+ui.Ink("Put them back anyway?")+"  ", []string{"Keep my changes", "Overwrite them", "Cancel"}, 0, 2) {
		case 0:
		case 1:
			overwrite = true
		default:
			ui.Step("Rewind cancelled; nothing changed.")
			return false
		}
	}
	n := plan.Restorable(overwrite)
	var sp ui.Spinner
	sp.Start(steps.Restoring(n))
	res := s.Restore(plan, overwrite)
	sp.Stop()
	var parts []string
	if k := len(res.Restored); k > 0 {
		parts = append(parts, fmt.Sprintf("%d put back", k))
	}
	if k := len(res.Deleted); k > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", k))
	}
	if k := len(res.Skipped); k > 0 {
		parts = append(parts, fmt.Sprintf("%d left as is", k))
	}
	ui.StepDone(len(res.Failed) == 0, steps.Restored(res.N()), strings.Join(parts, " · "), 0)
	for _, f := range res.Failed {
		ui.Println("    " + ui.Ink("■ ") + ui.Sub(shortPath(f)))
	}
	return true
}

// specialistPaths names the files applying a specialist would change, for the
// checkpoint of the turn that applies it.
func specialistPaths(bg *bgAgents) func(name, args string) []string {
	return func(name, args string) []string {
		if bg == nil || name != "apply_specialist" {
			return nil
		}
		var v struct {
			Name   string `json:"name"`
			Action string `json:"action"`
		}
		if json.Unmarshal([]byte(args), &v) != nil || strings.ToLower(strings.TrimSpace(v.Action)) != "apply" {
			return nil
		}
		return bg.m.SpecialistFiles(v.Name)
	}
}
