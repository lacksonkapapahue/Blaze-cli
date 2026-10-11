package agents

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

// Input is one thing that starts a main-agent turn: a user message, a background
// agent's result, or a retry.
type Input struct {
	Kind   string       // user, agent, retry
	Text   string       // the message (user) or the result (agent)
	Handle string       // agent results: which agent
	Conv   string       // agent results: the chat the agent belongs to
	Attach []llm.Attach // user messages: what the composer attached (images, files, folders)
}

// Mainline runs main-agent turns one at a time. A turn that arrives while another
// runs waits its turn instead of being refused, so each finished agent wakes the
// main agent on its own, in the order results came in, never bundled behind the
// slowest one.
type Mainline struct {
	Run func(Input) // runs one whole turn (blocking)

	mu   sync.Mutex
	busy bool
	q    []Input
}

// Submit runs in now, or queues it behind the turn in progress. It reports whether
// the input was queued.
func (m *Mainline) Submit(in Input) (queued bool) {
	m.mu.Lock()
	if m.busy {
		m.q = append(m.q, in)
		m.mu.Unlock()
		return true
	}
	m.busy = true
	m.mu.Unlock()
	go m.loop(in)
	return false
}

func (m *Mainline) loop(in Input) {
	for {
		func() {
			defer func() { _ = recover() }()
			m.Run(in)
		}()
		m.mu.Lock()
		if len(m.q) == 0 {
			m.busy = false
			m.mu.Unlock()
			return
		}
		in = m.q[0]
		m.q = m.q[1:]
		m.mu.Unlock()
	}
}

// Busy reports whether a turn is running.
func (m *Mainline) Busy() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.busy }

// Pending is how many inputs wait behind the current turn.
func (m *Mainline) Pending() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.q) }

// Drop removes queued inputs that match keep()==false (e.g. a chat was closed).
func (m *Mainline) Drop(keep func(Input) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Input
	for _, in := range m.q {
		if keep(in) {
			out = append(out, in)
		}
	}
	m.q = out
}

// ResultPrefix starts every agent-result message in the main conversation. The UI
// shows such messages as a small marker, not as a user bubble.
const ResultPrefix = "[Background agent result"

// ResultMessage is what the main agent reads when an agent finishes.
func ResultMessage(v View) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s · %s · %s · %d step(s) · %.0fs]\n", ResultPrefix, v.Handle, v.Status, v.N, v.Seconds)
	b.WriteString("This is a platform message, not the user. Your background agent finished. Tell the user what it found or did, in your own words and briefly; don't paste it whole.\n")
	fmt.Fprintf(&b, "<agent_result handle=%q status=%q>\n", v.Handle, v.Status)
	if v.Status == Done {
		b.WriteString(v.Result)
	} else {
		b.WriteString("The agent could not finish: " + v.Error)
		if v.Result != "" {
			b.WriteString("\n" + v.Result)
		}
	}
	b.WriteString("\n</agent_result>")
	if v.Kind == Specialist {
		b.WriteString("\nThis was your specialist " + strconv.Quote(v.Handle) + " (" + v.Mode + ").")
		switch {
		case v.Review == "pending":
			fmt.Fprintf(&b, " It changed %d file(s) in its isolated copy: %s. Review them (check_specialist with diff: true), then apply_specialist apply or discard. Nothing has reached the user's folder yet.", len(v.Changes), changeList(v.Changes, 15))
		case v.Review == "none":
			b.WriteString(" It changed no files.")
		}
		b.WriteString(" If other specialists are still running, end your turn with at most one short progress line; summarise for the user when the last one is in.")
	}
	return b.String()
}
