package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Normalized is a model's tool calls after cleanup (ported from Blaze Desktop's
// internal/llm/calls.go, trimmed to what the CLI needs).
type Normalized struct {
	Calls   []ToolCall
	Dropped []string // calls that couldn't be run, in words
}

// ResolveName maps a model's tool name onto a known one: exact, then
// case-insensitive, then with a "functions." / "default_api:" style prefix removed.
func ResolveName(raw string, known []string) string {
	raw = strings.TrimSpace(raw)
	for _, k := range known {
		if k == raw {
			return k
		}
	}
	base := raw
	for _, sep := range []string{".", ":", "/"} {
		if i := strings.LastIndex(base, sep); i >= 0 {
			base = base[i+1:]
		}
	}
	for _, k := range known {
		if strings.EqualFold(k, raw) || strings.EqualFold(k, base) {
			return k
		}
	}
	return ""
}

// NormalizeCalls gives every call an ID, resolves its name against known and
// checks its arguments are a JSON object. Calls that can't run are listed in Dropped.
func NormalizeCalls(calls []ToolCall, known []string) Normalized {
	var n Normalized
	ids := map[string]bool{}
	for i, c := range calls {
		raw := strings.TrimSpace(c.Function.Name)
		if raw == "" {
			n.Dropped = append(n.Dropped, "a call with no tool name")
			continue
		}
		name := ResolveName(raw, known)
		if name == "" {
			n.Dropped = append(n.Dropped, raw+" (there's no tool by that name)")
			continue
		}
		args := strings.TrimSpace(c.Function.Arguments)
		if args == "" || args == "null" {
			args = "{}"
		}
		var obj map[string]any
		if json.Unmarshal([]byte(args), &obj) != nil {
			n.Dropped = append(n.Dropped, name+" (its arguments weren't valid JSON)")
			continue
		}
		id := strings.TrimSpace(c.ID)
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		for ids[id] {
			id += "x"
		}
		ids[id] = true
		c.ID, c.Type = id, "function"
		c.Function.Name, c.Function.Arguments = name, args
		n.Calls = append(n.Calls, c)
	}
	return n
}

// Plain is an error as one sentence for the user.
func Plain(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	if s == "" {
		return "Something went wrong. Try again."
	}
	return s
}
