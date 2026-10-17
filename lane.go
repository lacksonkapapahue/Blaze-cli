package main

// Blaze is always available (v0.9.1), like Blaze Desktop and Hark: a message sent
// while a turn runs is answered right away by a side lane instead of waiting in a
// queue. The lane sees the conversation (without tool output), what the turn is
// doing and the background agents, and replies in a few sentences. It can pass a
// correction or an addition to the running turn (tell_main: the turn reads it at its
// next step), stop the turn (stop_main), or hand separate work to background agents
// and specialists, so the main chat stays free while they do the heavy lifting.
// Either way the exchange reaches the main conversation, so nothing is lost.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/agents"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

// laneComplete is the lane's model call (a var so tests can fake it).
var laneComplete = func(ctx context.Context, a *agent.Agent, msgs []llm.Message, ts []llm.Tool) (*llm.Result, error) {
	return llm.Complete(ctx, a.Cfg, msgs, ts, nil)
}

// lane answers messages sent while a turn runs.
type lane struct {
	a    *agent.Agent
	bg   *bgAgents
	stop func() // cancels the running turn
	// running reports whether a turn is running (nil: assume it is). A reply that
	// lands after the turn ended goes straight into the conversation.
	running func() bool
	mu      sync.Mutex
	busy    int // replies in flight
}

const laneSystem = `

YOU ARE ANSWERING MID-TURN (Blaze is always available)
Your main turn is busy right now. The user just sent a message while it works, and you answer it now, in parallel, like a colleague who looks up from their work. Reply in one to four short sentences, plain words, no headings.
- A question you can answer from the conversation, the status below or what you know: answer it.
- A correction, an addition or a change of plan for the work in progress: call tell_main with a clear instruction, and tell the user in a few words that it's on its way to the work.
- "Stop", "cancel", "wait": call stop_main, and say so.
- Separate work that needs tools (another fix, research, a build): hand it to a background agent (dispatch_agent) or specialists, and say so in one line. Never do tool work yourself.
- "How's it going?": answer from the status below (and list_agents for agents).
- Never claim something is done that isn't. Don't repeat what the main turn will say anyway.
`

// laneTools are the lane's tools: steer or stop the turn, and the lead's agent tools.
func laneTools() []llm.Tool {
	mk := func(name, desc string, props map[string]any, req ...string) llm.Tool {
		var t llm.Tool
		t.Type = "function"
		t.Function.Name = name
		t.Function.Description = desc
		t.Function.Parameters = map[string]any{"type": "object", "properties": props, "required": req}
		return t
	}
	ts := []llm.Tool{
		mk("tell_main", "Pass a correction, addition or change of plan to the work in progress. It reads it at its next step and changes course.",
			map[string]any{"instruction": map[string]any{"type": "string", "description": "What the work should do now, in the user's terms, self-contained."}}, "instruction"),
		mk("stop_main", "Stop the work in progress (the user asked to stop, cancel or wait).", map[string]any{}),
	}
	return append(ts, agents.MainTools()...)
}

// plainHistory is the conversation as text messages only: tool calls and their
// output are left out, so a turn caught mid-step still reads as a valid chat.
func plainHistory(h []llm.Message, max int) []llm.Message {
	var out []llm.Message
	for _, m := range h {
		if (m.Role != "user" && m.Role != "assistant") || strings.TrimSpace(m.Content) == "" {
			continue
		}
		c := m.Content
		if len(c) > 4000 {
			c = c[:4000] + "\n…(cut)"
		}
		out = append(out, llm.Message{Role: m.Role, Content: c})
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	for len(out) > 0 && out[0].Role != "user" {
		out = out[1:]
	}
	return out
}

// status says what the turn and the agents are doing, for the lane's prompt.
func (l *lane) status() string {
	s := "\nRIGHT NOW\n- Main turn: "
	if now := l.a.Now(); now != "" {
		s += now + "."
	} else {
		s += "finishing up."
	}
	if l.bg != nil {
		s += "\n- Background agents: " + strings.TrimSpace(l.bg.m.Summary(l.bg.conv))
	}
	return s + "\n"
}

// send is ui.OnSend: a plain message gets an answer now; commands, shell lines and
// notes to remember wait for the turn to end (they change Blaze's state).
func (l *lane) send(line string) bool {

	if strings.HasPrefix(line, "/") || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "#") {
		return false
	}
	ui.Above(append([]string{""}, ui.UserLines(line, "")...)...)
	l.mu.Lock()
	l.busy++
	l.mu.Unlock()
	go l.reply(line)
	return true
}

// reply answers one message and hands the exchange to the main conversation.
func (l *lane) reply(line string) {
	defer func() {
		l.mu.Lock()
		l.busy--
		l.mu.Unlock()
	}()
	// Some keyless providers answer one request at a time, so the lane may wait
	// behind the turn; past this, the turn answers the message instead.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	msgs := append([]llm.Message{{Role: "system", Content: l.a.SystemPrompt() + laneSystem + l.status()}}, plainHistory(l.a.HistoryCopy(), 30)...)
	msgs = append(msgs, llm.Message{Role: "user", Content: attachFiles(l.a, line)})
	note := agent.Note{User: line}
	var said []string
	for step := 0; step < 4; step++ {
		r, err := laneComplete(ctx, l.a, msgs, laneTools())
		if err != nil {
			// No quick answer: the turn reads the message at its next step instead.
			note.Act = true
			ui.Above("  " + ui.Weak("· Blaze answers this in the work in progress"))
			l.deliver(note)
			return
		}
		if t := strings.TrimSpace(r.Text); t != "" {
			said = append(said, t)
			ui.Above(ui.RenderMD(t)...)
		}
		if len(r.ToolCalls) == 0 {
			break
		}
		msgs = append(msgs, llm.Message{Role: "assistant", Content: r.Text, ToolCalls: r.ToolCalls})
		for _, tc := range r.ToolCalls {
			msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: l.run(tc, &note)})
		}
	}
	note.Reply = strings.Join(said, "\n")
	if note.Reply == "" && !note.Act {
		note.Act = true // it said nothing: let the work answer it
	}
	l.deliver(note)
}

// deliver hands a note to the running turn, or, when the turn already ended, puts
// the exchange into the conversation (and wakes the prompt for one to act on).
func (l *lane) deliver(n agent.Note) {
	if l.running == nil || l.running() {
		l.a.Tell(n)
		return
	}
	if !n.Act {
		l.a.Push(llm.Message{Role: "user", Content: n.User}, llm.Message{Role: "assistant", Content: n.Reply})
		return
	}
	l.a.Tell(n)
	ui.Wake() // the main loop runs it (settle) as the next turn
}

// run carries out one of the lane's tool calls.
func (l *lane) run(tc llm.ToolCall, note *agent.Note) string {
	switch tc.Function.Name {
	case "tell_main":
		var in struct{ Instruction string }
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &in)
		if strings.TrimSpace(in.Instruction) != "" {
			note.User += "\n(Instruction for you: " + strings.TrimSpace(in.Instruction) + ")"
		}
		note.Act = true
		return "Passed on: the work reads it at its next step."
	case "stop_main":
		if l.stop != nil {
			l.stop()
		}
		return "Stopped the work in progress."
	}
	if l.bg != nil && agents.IsMainTool(tc.Function.Name) {
		return l.bg.leadFrom(tc.Function.Name, tc.Function.Arguments, false)
	}
	return "Error: " + tc.Function.Name + " isn't available here. Use tell_main or dispatch_agent."
}

// settle runs after a turn: notes the turn never read go into the conversation, and
// those that asked for action become the next turn's prompt.
// wait is how long a reply still being written may take to land first; one that
// lands later goes straight into the conversation (deliver).
func (l *lane) settle(wait time.Duration) (next string) {
	for end := time.Now().Add(wait); time.Now().Before(end); {
		l.mu.Lock()
		b := l.busy
		l.mu.Unlock()
		if b <= 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var act []agent.Note
	for _, n := range l.a.TakeNotes() {
		if n.Act {
			act = append(act, n)
			continue
		}
		l.a.Push(llm.Message{Role: "user", Content: n.User}, llm.Message{Role: "assistant", Content: n.Reply})
	}
	if len(act) == 0 {
		return ""
	}
	return agent.NoteMessage(act)
}
