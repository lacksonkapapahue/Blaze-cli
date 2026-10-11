package agent

import (
	"encoding/json"

	"github.com/lacksonkapapahue/blaze-cli/internal/checkpoint"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
)

// fileTools change one path named in their "path" argument.
var fileTools = map[string]bool{"file_write": true, "file_edit": true, "file_delete": true}

// changePaths names the files a tool call is about to change (nil when it changes
// none Blaze can tell in advance; shell commands are covered by the git snapshot).
func (a *Agent) changePaths(name, args string) []string {
	if fileTools[name] {
		var v struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(args), &v) != nil || v.Path == "" {
			return nil
		}
		return []string{tools.ExpandPath(a.Env, v.Path)}
	}
	if a.ChangePaths != nil {
		return a.ChangePaths(name, args)
	}
	return nil
}

// HistIndex is where the next message lands in the conversation, counting messages
// trim has cut (a checkpoint's position).
func (a *Agent) HistIndex() int { return a.Dropped + len(a.History) }

// chatIndex finds a checkpoint's message in the current history (-1 when it's gone):
// first where it should be, then the nearest user message with the same text.
func (a *Agent) chatIndex(t *checkpoint.Turn) int {
	if t == nil || t.ChatGone {
		return -1
	}
	match := func(i int) bool {
		return i >= 0 && i < len(a.History) && a.History[i].Role == "user" && checkpoint.HashText(a.History[i].Content) == t.SentHash
	}
	want := t.HistIndex - a.Dropped
	if match(want) {
		return want
	}
	best := -1
	for i := range a.History {
		if match(i) && (best < 0 || abs(i-want) < abs(best-want)) {
			best = i
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// CanRewindChat reports whether the conversation still holds the checkpoint's message.
func (a *Agent) CanRewindChat(t *checkpoint.Turn) bool { return a.chatIndex(t) >= 0 }

// RewindChat cuts the conversation back to just before the checkpoint's message.
func (a *Agent) RewindChat(t *checkpoint.Turn) bool {
	i := a.chatIndex(t)
	if i < 0 {
		return false
	}
	a.History = a.History[:i]
	tools.ResetTodos()
	return true
}
