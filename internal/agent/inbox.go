package agent

// The inbox (v0.9.1): Blaze is always available. While a turn runs, a message the
// user sends gets a quick reply from a side lane (the host's), and what the turn
// should know reaches it here: the next step starts with it, so the work changes
// course without waiting for the turn to end. A turn that was about to finish takes
// one more step when a note is waiting, so nothing the user said is dropped.

import (
	"strings"

	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

// Note is something the user said while a turn ran, and the quick reply they got.
type Note struct {
	User  string // what they typed
	Reply string // what the side lane already told them ("" when it said nothing)
	Act   bool   // the turn should act on it (a correction, an addition), not just know it
}

// Tell gives the running turn a note; it reads it at its next step.
func (a *Agent) Tell(n Note) {
	a.inMu.Lock()
	a.inbox = append(a.inbox, n)
	a.inMu.Unlock()
}

// TakeNotes returns and clears the notes no turn read (the turn ended first).
func (a *Agent) TakeNotes() []Note {
	a.inMu.Lock()
	defer a.inMu.Unlock()
	n := a.inbox
	a.inbox = nil
	return n
}

// NoteMessage is how notes read to the model mid-turn.
func NoteMessage(ns []Note) string {
	var b strings.Builder
	act := false
	for _, n := range ns {
		b.WriteString("[While you worked, the user said] " + strings.TrimSpace(n.User) + "\n")
		if r := strings.TrimSpace(n.Reply); r != "" {
			b.WriteString("[Blaze's quick reply, already shown to them] " + r + "\n")
		}
		act = act || n.Act
	}
	if act {
		b.WriteString("Answer or act on this now: change course if it asks you to, and answer it if it's a question nobody answered yet. Don't repeat a quick reply they already got.")
	} else {
		b.WriteString("They already have an answer; no need to reply to it again. Carry on, using it if it matters.")
	}
	return b.String()
}

// readNotes pushes waiting notes into the history; it reports whether there were any.
func (a *Agent) readNotes() bool {
	ns := a.TakeNotes()
	if len(ns) == 0 {
		return false
	}
	a.push(llm.Message{Role: "user", Content: NoteMessage(ns)})
	return true
}

// Now is what the running turn is doing, in plain words ("" between turns).
func (a *Agent) Now() string {
	if s, ok := a.now.Load().(string); ok {
		return s
	}
	return ""
}

func (a *Agent) setNow(s string) { a.now.Store(s) }

// HistoryCopy is a copy of the conversation, safe to read while a turn runs.
func (a *Agent) HistoryCopy() []llm.Message { return a.historyCopy() }
