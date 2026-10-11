package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/checkpoint"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

// ask runs one turn the way the REPL does: a checkpoint around Ask.
func ask(t *testing.T, a *Agent, prompt string) *checkpoint.Turn {
	t.Helper()
	turn := a.Checkpoints.Begin("user", prompt, prompt, a.HistIndex())
	if _, err := a.Ask(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	a.Checkpoints.End()
	return turn
}

// Blaze's file tools are checkpointed per turn; rewinding puts code and chat back.
func TestAgentCheckpointsFileToolsAndRewinds(t *testing.T) {
	a := newTestAgent(t)
	a.Env.Confirm = func(string, string) bool { return true }
	root := a.Env.Root
	a.Checkpoints = checkpoint.Open(root)
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	scripted(t,
		&llm.Result{ToolCalls: calls("file_edit", `{"path":"keep.txt","old_text":"one","new_text":"two"}`)},
		&llm.Result{ToolCalls: calls("file_write", `{"path":"sub/new.txt","content":"fresh"}`)},
		&llm.Result{Text: "Done."},
		&llm.Result{ToolCalls: calls("file_delete", `{"path":"keep.txt"}`)},
		&llm.Result{Text: "Deleted."},
	)
	t1 := ask(t, a, "edit and create")
	t2 := ask(t, a, "now delete it")
	if _, err := os.Stat(keep); !os.IsNotExist(err) {
		t.Fatalf("keep.txt should be deleted by turn 2")
	}
	turns := a.Checkpoints.List()
	if len(turns) != 2 || len(t1.Order) != 2 || len(t2.Order) != 1 {
		t.Fatalf("turns=%d t1=%v t2=%v", len(turns), t1.Order, t2.Order)
	}

	// Rewind turn 2 (code and chat): keep.txt comes back as "two", chat ends after turn 1.
	p, err := a.Checkpoints.PlanRestore(1)
	if err != nil || len(p.Conflicts()) != 0 {
		t.Fatalf("plan: %v %v", err, p.Conflicts())
	}
	a.Checkpoints.Restore(p, false)
	if b, _ := os.ReadFile(keep); string(b) != "two\n" {
		t.Fatalf("keep.txt = %q", b)
	}
	before := len(a.History)
	if !a.RewindChat(t2) {
		t.Fatal("chat rewind failed")
	}
	if len(a.History) >= before || a.History[len(a.History)-1].Content != "Done." {
		t.Fatalf("history not cut before turn 2: %+v", a.History[len(a.History)-1])
	}
	a.Checkpoints.DropFrom(1)

	// Rewind turn 1: the edit is undone and the created file (and its folder) removed.
	p, _ = a.Checkpoints.PlanRestore(0)
	a.Checkpoints.Restore(p, false)
	if b, _ := os.ReadFile(keep); string(b) != "one\n" {
		t.Fatalf("keep.txt = %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "sub")); !os.IsNotExist(err) {
		t.Fatalf("the folder Blaze created should be gone")
	}
	if !a.RewindChat(t1) || len(a.History) != 0 {
		t.Fatalf("history should be empty, got %d", len(a.History))
	}
}

// The checkpoint's message is found even after trim cut the front of the chat.
func TestRewindChatAfterTrim(t *testing.T) {
	a := newTestAgent(t)
	a.Checkpoints = checkpoint.Open(a.Env.Root)
	var target *checkpoint.Turn
	for i := 0; i < 40; i++ {
		p := "message " + strings.Repeat("x", i)
		turn := a.Checkpoints.Begin("user", p, p, a.HistIndex())
		a.History = append(a.History, llm.Message{Role: "user", Content: p}, llm.Message{Role: "assistant", Content: "ok"})
		a.trim()
		a.Checkpoints.End()
		if i == 35 {
			target = turn
		}
	}
	if a.Dropped == 0 {
		t.Fatal("trim should have cut messages")
	}
	if !a.RewindChat(target) {
		t.Fatal("couldn't find the message after trim")
	}
	if last := a.History[len(a.History)-1]; last.Content != "ok" || a.History[len(a.History)-2].Content != "message "+strings.Repeat("x", 34) {
		t.Fatalf("cut at the wrong place: %+v", a.History[len(a.History)-2:])
	}
	// A compacted chat can't be rewound, only its code.
	gone := &checkpoint.Turn{SentHash: checkpoint.HashText("message"), ChatGone: true}
	if a.CanRewindChat(gone) {
		t.Fatal("ChatGone turn must not rewind the chat")
	}
}

// Host tools that change files (applying a specialist) are checkpointed too.
func TestChangePathsHookIsCheckpointed(t *testing.T) {
	a := newTestAgent(t)
	root := a.Env.Root
	a.Checkpoints = checkpoint.Open(root)
	f := filepath.Join(root, "app.go")
	_ = os.WriteFile(f, []byte("old"), 0o644)
	a.ChangePaths = func(name, args string) []string {
		if name == "apply_specialist" {
			return []string{f}
		}
		return nil
	}
	a.Handle = func(ctx context.Context, name, args string) (string, bool) {
		if name == "apply_specialist" {
			_ = os.WriteFile(f, []byte("from specialist"), 0o644)
			return "Applied 1 file change(s).", true
		}
		return "", false
	}
	a.Checkpoints.Begin("agent", "", "result", 0)
	a.exec(context.Background(), "apply_specialist", `{"name":"x","action":"apply"}`)
	a.Checkpoints.End()
	p, _ := a.Checkpoints.PlanRestore(0)
	if len(p.Items) != 1 {
		t.Fatalf("items = %+v", p.Items)
	}
	a.Checkpoints.Restore(p, false)
	if b, _ := os.ReadFile(f); string(b) != "old" {
		t.Fatalf("app.go = %q", b)
	}
}
