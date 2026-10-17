package agent

// A text sink for hosts that are not the terminal (Blaze OS, internal/osweb): a
// Quiet agent prints nothing, so a host that wants the reply as it streams puts a
// sink on the turn's context with WithTextSink. Everything else a web host needs
// is already a hook (Env.Confirm, Env.OnDiff, Env.OnTodo, Env.Status, Handle).

import (
	"context"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

type textSinkKey struct{}

// WithTextSink returns ctx carrying fn: every chunk of reply text the model
// streams during Ask(ctx, …) is passed to fn as well (Quiet or not).
func WithTextSink(ctx context.Context, fn func(string)) context.Context {
	return context.WithValue(ctx, textSinkKey{}, fn)
}

func init() {
	inner := complete
	complete = func(ctx context.Context, cfg *config.Config, msgs []llm.Message, tools []llm.Tool, onText func(string)) (*llm.Result, error) {
		if fn, ok := ctx.Value(textSinkKey{}).(func(string)); ok && fn != nil {
			prev := onText
			onText = func(s string) {
				fn(s)
				if prev != nil {
					prev(s)
				}
			}
		}
		return inner(ctx, cfg, msgs, tools, onText)
	}
}
