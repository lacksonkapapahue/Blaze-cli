package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

// Images ride on the sent copy of the user's message for its turn and the next,
// and never enter the kept (saved) history.
func TestAskImages(t *testing.T) {
	a := newTestAgent(t)
	old := complete
	t.Cleanup(func() { complete = old })
	var sent [][]string // images on the first user message, per call
	complete = func(ctx context.Context, cfg *config.Config, msgs []llm.Message, tools []llm.Tool, onText func(string)) (*llm.Result, error) {
		var imgs []string
		for _, m := range msgs {
			if m.Role == "user" && strings.HasPrefix(m.Content, "what is [image 1]") {
				imgs = m.Images
			}
		}
		sent = append(sent, imgs)
		return &llm.Result{Text: "ok"}, nil
	}
	url := "data:image/png;base64,QUJD"
	if _, err := a.AskImages(context.Background(), "what is [image 1]?", []string{url}); err != nil {
		t.Fatal(err)
	}
	for _, m := range a.History {
		if len(m.Images) != 0 {
			t.Fatal("images kept in history")
		}
	}
	b, _ := json.Marshal(a.History)
	if strings.Contains(string(b), "QUJD") {
		t.Fatal("image data in the saved history")
	}
	_, _ = a.Ask(context.Background(), "and now?")
	_, _ = a.Ask(context.Background(), "later")
	if len(sent) != 3 || len(sent[0]) != 1 || sent[0][0] != url || len(sent[1]) != 1 || len(sent[2]) != 0 {
		t.Fatalf("images per call: %v", sent)
	}
}
