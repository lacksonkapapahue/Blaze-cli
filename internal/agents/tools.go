package agents

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

func tool(name, desc string, props map[string]any, required ...string) llm.Tool {
	var t llm.Tool
	t.Type = "function"
	t.Function.Name = name
	t.Function.Description = desc
	t.Function.Parameters = map[string]any{"type": "object", "properties": props, "required": required}
	return t
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// MainToolNames are the tools only the main agent has.
var MainToolNames = []string{"dispatch_agent", "message_agent", "cancel_agent", "list_agents"}

// IsMainTool reports whether name is one of the agent-control tools.
func IsMainTool(name string) bool {
	for _, n := range MainToolNames {
		if n == name {
			return true
		}
	}
	return false
}

// MainTools are the main agent's tools for running background agents.
func MainTools() []llm.Tool {
	return []llm.Tool{
		tool("dispatch_agent", "Hand a task to a background agent and return at once. The agent works on its own (same tools and approvals as you) while the user keeps chatting; its result comes back to you as a platform message. Use for long, multi-step or parallel work. Put a short line for the user in the same reply (e.g. \"On it, I'll report back.\").",
			map[string]any{
				"handle": str("Short lowercase name for this agent, e.g. fix-tests or research-laptops. One agent per independent target."),
				"task":   str("A self-contained brief: the goal, every detail the agent needs (paths, names, constraints, what done looks like) and what to report back. The agent can't see this chat."),
				"tools":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional: limit the agent to these tool names (e.g. [\"web_search\",\"fetch_page\"]). Omit for all tools."},
			}, "handle", "task"),
		tool("message_agent", "Steer a running background agent: the instruction reaches it at its next step. Use this when the user changes or adds to a task an agent is already doing, instead of starting a duplicate.",
			map[string]any{"handle": str("The agent's handle."), "message": str("The instruction, self-contained.")}, "handle", "message"),
		tool("cancel_agent", "Stop a background agent (running or queued).",
			map[string]any{"handle": str("The agent's handle.")}, "handle"),
		tool("list_agents", "List this chat's background agents with their status and current step.", map[string]any{}),
	}
}

// AskTool lets a sub-agent ask the user a question (shown on its card).
func AskTool() llm.Tool {
	return tool("ask_user", "Ask the user a question and wait for the answer. Only when you truly cannot continue without them (a choice only they can make, a missing detail). Never for permission to use a tool: approvals happen on their own.",
		map[string]any{"question": str("One clear question."), "choices": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional short answers to offer."}}, "question")
}

// ToolCall is the outcome of one agent-control tool call by the main agent.
type ToolCall struct {
	Result     string
	Dispatched string // handle, when an agent was dispatched
}

// HandleMainTool runs dispatch_agent / message_agent / cancel_agent / list_agents.
func (m *Manager) HandleMainTool(name, rawArgs, conv, folder string) ToolCall {
	var a struct {
		Handle  string   `json:"handle"`
		Task    string   `json:"task"`
		Tools   []string `json:"tools"`
		Message string   `json:"message"`
	}
	if strings.TrimSpace(rawArgs) != "" {
		if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
			// Some models send tools as a comma string.
			var loose map[string]any
			if json.Unmarshal([]byte(rawArgs), &loose) != nil {
				return ToolCall{Result: "Error in " + name + ": the arguments weren't valid JSON. Call it again with a JSON object."}
			}
			a.Handle, _ = loose["handle"].(string)
			a.Task, _ = loose["task"].(string)
			a.Message, _ = loose["message"].(string)
			if s, ok := loose["tools"].(string); ok {
				for _, t := range strings.Split(s, ",") {
					if t = strings.TrimSpace(t); t != "" {
						a.Tools = append(a.Tools, t)
					}
				}
			}
		}
	}
	switch name {
	case "dispatch_agent":
		h, st, err := m.Dispatch(a.Handle, a.Task, a.Tools, conv, folder)
		if err != nil {
			return ToolCall{Result: "Error in dispatch_agent: " + err.Error()}
		}
		msg := fmt.Sprintf("%s: agent %q is working in the background. Its result will come back to you as a platform message. End your turn now with one short line to the user.", st, h)
		if st == "queued" {
			msg = fmt.Sprintf("queued: agent %q will start when one of the %d running agents finishes. Its result will come back to you. End your turn now with one short line to the user.", h, m.Cap())
		}
		return ToolCall{Result: msg, Dispatched: h}
	case "message_agent":
		if err := m.Message(a.Handle, a.Message); err != nil {
			return ToolCall{Result: "Error in message_agent: " + err.Error()}
		}
		return ToolCall{Result: fmt.Sprintf("Delivered to %q; it reads it at its next step.", CleanHandle(a.Handle))}
	case "cancel_agent":
		if err := m.Cancel(a.Handle); err != nil {
			return ToolCall{Result: "Error in cancel_agent: " + err.Error()}
		}
		return ToolCall{Result: fmt.Sprintf("Stopped %q.", CleanHandle(a.Handle))}
	case "list_agents":
		return ToolCall{Result: m.Summary(conv)}
	}
	return ToolCall{Result: "Error: there is no tool called " + name}
}

// Delegation teaches the main agent when to hand work off.
const Delegation = `
BACKGROUND AGENTS
You can hand work to background agents with dispatch_agent so the user can keep chatting while it runs.
- Delegate long, multi-step or parallelizable work, or anything that would keep the user waiting: fixing a failing build, research across many pages, refactors over several files, installs, long scripts.
- Do quick answers, small edits and single lookups yourself. Don't delegate what you can finish in a step or two.
- One agent per independent target. Never put two agents on the same file, folder or object; if the work overlaps, use one agent.
- The brief must stand alone: the agent can't see this chat. Include paths, names, constraints and what to report.
- After dispatch_agent, end your turn with one short line in the same reply ("On it, I'll report back."). Don't wait for it, don't poll it.
- When the user changes or adds to a running task, steer it with message_agent; don't start a duplicate. cancel_agent stops one; list_agents shows status.
- A "[Background agent result …]" message is from the platform, not the user: tell the user the outcome briefly in your own words.
`

// SubSystem is added to a sub-agent's system prompt.
func SubSystem(handle string) string {
	return fmt.Sprintf(`
YOU ARE A BACKGROUND AGENT (handle: %s)
The main Blaze chat handed you the task in the first message. The user is not watching you and can't see your messages; only your final reply goes back, to the main agent, which tells the user.
- Work the task through to the end with your tools, checking your work as you go.
- Approvals still apply: risky actions ask the user on your card and you wait.
- Use ask_user only when you truly can't continue without the user.
- A message starting "[Update from the main agent]" changes or adds to your task; follow it.
- Finish with a short report: what you did, the result (with paths, numbers, links), and anything the user must decide. No questions at the end unless you're blocked.
`, handle)
}
