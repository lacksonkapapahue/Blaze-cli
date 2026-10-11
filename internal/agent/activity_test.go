package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

// scripted fakes the model: each call returns the next result.
func scripted(t *testing.T, rs ...*llm.Result) *int {
	t.Helper()
	n := 0
	old := complete
	t.Cleanup(func() { complete = old })
	complete = func(ctx context.Context, cfg *config.Config, msgs []llm.Message, tools []llm.Tool, onText func(string)) (*llm.Result, error) {
		if n >= len(rs) {
			t.Fatalf("unexpected model call %d", n+1)
		}
		n++
		return rs[n-1], nil
	}
	return &n
}

func calls(names ...string) []llm.ToolCall {
	var out []llm.ToolCall
	for i := 0; i+1 < len(names); i += 2 {
		var c llm.ToolCall
		c.ID, c.Type = names[i], "function"
		c.Function.Name, c.Function.Arguments = names[i], names[i+1]
		out = append(out, c)
	}
	return out
}

// A turn keeps each step's plain phrase and its exact call for Ctrl+O.
func TestAskRecordsActivity(t *testing.T) {
	a := newTestAgent(t)
	a.Quiet = false // the activity is published for interactive turns
	scripted(t,
		&llm.Result{ToolCalls: calls("file_list", `{"path":"."}`)},
		&llm.Result{ToolCalls: calls("run_shell", `{"command":"echo hi && ls -la"}`)},
		&llm.Result{Text: "Done."},
	)
	out, err := a.Ask(context.Background(), "look around")
	if err != nil || out != "Done." {
		t.Fatalf("Ask = %q, %v", out, err)
	}
	act := ui.LastActivity()
	if act == nil || len(act.Steps) != 2 {
		t.Fatalf("activity = %+v", act)
	}
	if act.Steps[0].Plain != "Looking through the project folder" || act.Steps[1].Plain != "Looking through the project folder" {
		t.Errorf("plain phrases = %q, %q", act.Steps[0].Plain, act.Steps[1].Plain)
	}
	if act.Steps[1].Detail != "$ echo hi && ls -la" || !strings.Contains(act.Steps[1].Result, "hi") {
		t.Errorf("details kept for Ctrl+O = %+v", act.Steps[1])
	}
}

// Handing work off ends the turn after that step: no second model call.
func TestAskHandoffEndsTurn(t *testing.T) {
	a := newTestAgent(t)
	a.Handle = func(ctx context.Context, name, args string) (string, bool) {
		if name == "dispatch_specialist" {
			return "started: specialist \"x\"", true
		}
		return "", false
	}
	a.Handoff = func(cs []llm.ToolCall) bool { return len(cs) == 1 && cs[0].Function.Name == "dispatch_specialist" }
	n := scripted(t, &llm.Result{ToolCalls: calls("dispatch_specialist", `{"name":"x","brief":"y"}`)})
	out, err := a.Ask(context.Background(), "build it")
	if err != nil || out != HandoffLine || *n != 1 {
		t.Fatalf("Ask = %q, %v after %d calls", out, err, *n)
	}
	last := a.History[len(a.History)-1]
	if last.Role != "assistant" || last.Content != HandoffLine {
		t.Fatalf("history doesn't end with the handoff line: %+v", last)
	}
	// The model's own line is kept as the reply.
	n = scripted(t, &llm.Result{Text: "Briefed x.", ToolCalls: calls("dispatch_specialist", `{}`)})
	if out, _ := a.Ask(context.Background(), "again"); out != "Briefed x." || *n != 1 {
		t.Fatalf("Ask = %q after %d calls", out, *n)
	}
}
