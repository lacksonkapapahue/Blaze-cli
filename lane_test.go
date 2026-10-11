package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
)

func laneScript(t *testing.T, rs ...*llm.Result) *[][]llm.Message {
	t.Helper()
	var got [][]llm.Message
	old := laneComplete
	t.Cleanup(func() { laneComplete = old })
	n := 0
	laneComplete = func(ctx context.Context, a *agent.Agent, msgs []llm.Message, ts []llm.Tool) (*llm.Result, error) {
		got = append(got, msgs)
		if n >= len(rs) {
			t.Fatalf("unexpected lane call %d", n+1)
		}
		n++
		return rs[n-1], nil
	}
	return &got
}

func laneCall(name, args string) []llm.ToolCall {
	var c llm.ToolCall
	c.ID, c.Type = "c1", "function"
	c.Function.Name, c.Function.Arguments = name, args
	return []llm.ToolCall{c}
}

func TestLaneAnswersAndSteers(t *testing.T) {
	a := testAgent(t)
	a.Push(llm.Message{Role: "user", Content: "fix the tests"},
		llm.Message{Role: "assistant", ToolCalls: laneCall("run_shell", `{}`)},
		llm.Message{Role: "tool", ToolCallID: "c1", Content: "FAIL"})
	l := &lane{a: a}

	// A question: answered, and the turn only needs to know.
	msgs := laneScript(t, &llm.Result{Text: "It's running the tests now."})
	l.reply("how's it going?")
	ns := a.TakeNotes()
	if len(ns) != 1 || ns[0].Act || ns[0].Reply != "It's running the tests now." {
		t.Fatalf("note = %+v", ns)
	}
	for _, m := range (*msgs)[0] {
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			t.Fatal("the lane saw tool traffic from the running turn")
		}
	}
	if sys := (*msgs)[0][0].Content; !strings.Contains(sys, "ANSWERING MID-TURN") || !strings.Contains(sys, "Main turn:") {
		t.Error("lane prompt is missing its rules or the status")
	}

	// A correction: passed to the turn.
	laneScript(t, &llm.Result{ToolCalls: laneCall("tell_main", `{"instruction":"Only fix the ui package tests."}`)}, &llm.Result{Text: "Passing that on."})
	l.reply("just the ui ones")
	ns = a.TakeNotes()
	if len(ns) != 1 || !ns[0].Act || !strings.Contains(ns[0].User, "Only fix the ui package tests.") {
		t.Fatalf("note = %+v", ns)
	}

	// Stop: the turn is cancelled.
	stopped := false
	l.stop = func() { stopped = true }
	laneScript(t, &llm.Result{ToolCalls: laneCall("stop_main", `{}`)}, &llm.Result{Text: "Stopped."})
	l.reply("stop")
	if !stopped {
		t.Fatal("stop_main didn't stop the turn")
	}
	a.TakeNotes()

	// After the turn: a known exchange joins the chat; one to act on is the next prompt.
	a.Tell(agent.Note{User: "what model?", Reply: "Mistral."})
	a.Tell(agent.Note{User: "also update the README", Act: true})
	before := len(a.HistoryCopy())
	next := l.settle(time.Second)
	h := a.HistoryCopy()
	if len(h) != before+2 || h[len(h)-1].Content != "Mistral." {
		t.Fatalf("history after settle: %+v", h[before:])
	}
	if !strings.Contains(next, "also update the README") {
		t.Fatalf("next = %q", next)
	}
}

func TestLaneSendLeavesCommandsQueued(t *testing.T) {
	l := &lane{a: testAgent(t)}
	for _, s := range []string{"/model auto", "!ls", "# I use tabs"} {
		if l.send(s) {
			t.Errorf("%q was answered mid-turn; it should wait for the turn", s)
		}
	}
}

func testAgent(t *testing.T) *agent.Agent {
	t.Helper()
	t.Setenv("BLAZE_HOME", t.TempDir())
	info := &sysinfo.Info{OS: "linux", Shell: "sh", ShellPath: "/bin/sh"}
	return agent.New(&config.Config{}, info, t.TempDir(), false)
}

// A reply that lands after the turn ended goes straight into the conversation.
func TestLaneLateReply(t *testing.T) {
	a := testAgent(t)
	l := &lane{a: a, running: func() bool { return false }}
	laneScript(t, &llm.Result{Text: "It's 4."})
	l.reply("2+2?")
	h := a.HistoryCopy()
	if len(h) != 2 || h[0].Content != "2+2?" || h[1].Content != "It's 4." || len(a.TakeNotes()) != 0 {
		t.Fatalf("history = %+v", h)
	}
}
