package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

// What the user says mid-turn reaches the next step, and a turn about to finish
// takes one more step for a note it hasn't read.
func TestInboxSteersTheTurn(t *testing.T) {
	a := newTestAgent(t)
	var seen []string
	n := 0
	old := complete
	t.Cleanup(func() { complete = old })
	rs := []*llm.Result{
		{ToolCalls: calls("file_list", `{"path":"."}`)},
		{Text: "Done with the list."},
		{Text: "Switched to the docs."},
	}
	complete = func(ctx context.Context, cfg *config.Config, msgs []llm.Message, tools []llm.Tool, onText func(string)) (*llm.Result, error) {
		seen = append(seen, msgs[len(msgs)-1].Content)
		n++
		switch n {
		case 1:
			a.Tell(Note{User: "use the docs folder", Reply: "Got it, passing that on.", Act: true})
		case 2:
			a.Tell(Note{User: "what model is this?", Reply: "Mistral."})
		}
		return rs[n-1], nil
	}
	out, err := a.Ask(context.Background(), "list the files")
	if err != nil || out != "Switched to the docs." || n != 3 {
		t.Fatalf("Ask = %q, %v after %d calls", out, err, n)
	}
	if !strings.Contains(seen[1], "use the docs folder") || !strings.Contains(seen[1], "Answer or act on this now") {
		t.Errorf("step 2 didn't read the correction: %q", seen[1])
	}
	if !strings.Contains(seen[2], "what model is this?") || !strings.Contains(seen[2], "no need to reply") {
		t.Errorf("the turn finished without reading the last note: %q", seen[2])
	}
	if len(a.TakeNotes()) != 0 {
		t.Error("notes left over")
	}
	if a.Now() != "" {
		t.Errorf("Now() after the turn = %q", a.Now())
	}
}
