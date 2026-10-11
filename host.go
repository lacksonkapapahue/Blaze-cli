package main

// The main agent's host tools and modes: background agents, connectors,
// update_blaze, notify, and the Plan/Build + Auto/Autopilot modes (saved in
// ~/.blaze/cli.json so they stick between runs, like the desktop's composer modes).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/agents"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/notify"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
	"github.com/lacksonkapapahue/blaze-cli/internal/update"
)

// ---------------- modes ----------------

type cliPrefs struct {
	WorkMode string `json:"work_mode"` // build (default) | plan
	Approval string `json:"approval"`  // auto (default) | autopilot
}

func prefsFile() string { return filepath.Join(config.Dir(), "cli.json") }

func loadPrefs() cliPrefs {
	var p cliPrefs
	if b, err := os.ReadFile(prefsFile()); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return p
}

func savePrefs(a *agent.Agent) {
	p := cliPrefs{WorkMode: "build", Approval: a.Approval}
	if a.Env.Plan {
		p.WorkMode = "plan"
	}
	_ = os.MkdirAll(config.Dir(), 0o700)
	b, _ := json.MarshalIndent(p, "", "  ")
	_ = os.WriteFile(prefsFile(), b, 0o600)
}

func setWork(a *agent.Agent, plan bool) {
	a.Env.Plan = plan
	savePrefs(a)
}

func setApproval(a *agent.Agent, autopilot bool) {
	a.Approval = "auto"
	if autopilot {
		a.Approval = "autopilot"
	}
	savePrefs(a)
}

// modeName is the footer's wording: "build · auto", "plan", "build · autopilot".
func modeName(a *agent.Agent) string {
	if a.Env.Plan {
		return "plan"
	}
	return "build · " + a.Approval
}

// cycleMode is Shift+Tab: Build·Auto → Build·Autopilot → Plan → Build·Auto.
// (Terminals can't tell Ctrl+Tab from Tab, so one key walks all three.)
func cycleMode(a *agent.Agent) {
	switch {
	case a.Env.Plan:
		a.Env.Plan, a.Approval = false, "auto"
	case a.Autopilot():
		a.Env.Plan = true
	default:
		a.Approval = "autopilot"
	}
	savePrefs(a)
}

// ---------------- wiring ----------------

// restartAfterTurn is set by update_blaze: the REPL restarts once the turn ends.
var restartAfterTurn bool

// wire plugs background agents, connectors, update_blaze and notify into the main agent.
func wire(a *agent.Agent, interactive bool) *bgAgents {
	var bg *bgAgents
	if interactive {
		bg = newBG(a)
		confirmHook = bg.confirmConnector
	}
	autopilotHook = a.Autopilot
	a.Extra = func() []llm.Tool {
		var ts []llm.Tool
		if bg != nil {
			ts = append(ts, agents.MainTools()...)
			ts = append(ts, updateBlazeTool())
		}
		ts = append(ts, notifyTool())
		ts = append(ts, getConns().Tools()...)
		return append(ts, getMCP().Tools()...)
	}
	a.ReadOnly = func(name string) bool {
		return getConns().ReadOnly(name) || getMCP().ReadOnly(name) || (bg != nil && leadPlanAllows[name])
	}
	a.ExtraSystem = func() string {
		s := ""
		if bg != nil {
			s += agents.Delegation + agents.Orchestration + mainChat
		}
		return s + getConns().PromptBlock() + getMCP().Summary()
	}
	a.Handle = func(ctx context.Context, name, args string) (string, bool) {
		switch {
		case bg != nil && agents.IsMainTool(name):
			return bg.lead(name, args), true
		case bg != nil && name == "update_blaze":
			a.StopSpinner()
			return selfUpdate(bg), true
		case name == "notify":
			return runNotify(args), true
		case getMCP().Handles(name): // before connectors: they claim every "mcp_" name
			return getMCP().Call(ctx, name, args, mcpPolicy(a)), true
		case getConns().Handles(name):
			return getConns().Run(ctx, name, args), true
		}
		return "", false
	}
	if bg != nil {
		a.Handoff = bg.handoff
	}
	return bg
}

// leadPlanAllows are the agent tools a Plan-mode lead keeps: it may look at agents and
// brief Plan specialists (they only scout), not start Build work or apply changes.
var leadPlanAllows = map[string]bool{"list_agents": true, "dispatch_specialist": true, "check_specialist": true, "message_specialist": true, "cancel_specialist": true}

// mainChat keeps the main model light: it talks, plans and steers, and hands real
// work to agents and specialists so the user can always reach it (v0.7.0).
const mainChat = `
YOU ARE THE MAIN CHAT (always reachable)
The user can type to you at any time, even while you or your agents work: a message sent while you are busy waits for your next turn, so keep your own turns short.
- Talk, answer, plan and steer yourself. Work that needs more than two or three tool calls (builds, test runs, refactors, installs, research across pages) goes to a background agent, or to specialists when it splits into independent parts.
- Hand off and end your turn in the same reply with one short line. Never wait for or poll an agent; its result comes back to you as a platform message.
- When the user asks how it's going, answer from list_agents / check_specialist. When they change their mind, steer with message_agent / message_specialist, or cancel.
- When you describe what you or an agent is doing, use plain words ("running the tests", "reading the config"), not commands or code.
`

// ---------------- update_blaze ----------------

func updateBlazeTool() llm.Tool {
	var t llm.Tool
	t.Type = "function"
	t.Function.Name = "update_blaze"
	t.Function.Description = "Update Blaze (this CLI) to the newest release. Use when the user asks you to update Blaze or yourself. It checks GitHub, downloads the build for this computer, swaps the binary atomically and restarts Blaze after your reply (background agents resume). Never use a shell command to update Blaze."
	t.Function.Parameters = map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}
	return t
}

func selfUpdate(bg *bgAgents) string {
	tag, newer, err := update.Check(version)
	if err != nil {
		return "Couldn't check for a Blaze update: " + err.Error() + ". Tell the user in one line."
	}
	if !newer {
		return "Blaze is already on the newest version, " + version + ". Tell the user in one line."
	}
	ok, err := update.Run(version, true)
	if err != nil {
		return "Updating to " + tag + " failed: " + err.Error() + ". The current version keeps working. Tell the user in one line."
	}
	if !ok {
		return "Blaze is already on the newest version. Tell the user in one line."
	}
	restartAfterTurn = true
	if bg != nil {
		bg.markResume()
	}
	return fmt.Sprintf("Blaze %s is installed. Blaze restarts by itself right after your reply (background agents pick up again). Tell the user in one short line and call no more tools.", tag)
}

// restartNow swaps this process for the new binary (after /update or update_blaze).
func restartNow(a *agent.Agent, bg *bgAgents) {
	self, err := update.Self()
	if err != nil {
		ui.Println("  " + ui.Weak("Restart blaze to use the new version."))
		return
	}
	if bg != nil {
		bg.markResume()
		bg.m.StopAll()
	}
	a.Save()
	a.Close()
	stopMCP()
	ui.Println("  " + ui.Accent("■ ") + ui.Ink("Restarting Blaze…"))
	ui.FocusReporting(false)
	ui.ShowCursor()
	args := []string{"-c"}
	if err := update.Restart(self, args); err != nil {
		ui.Println("  " + ui.Weak("Couldn't restart ("+err.Error()+"). Run blaze again to use the new version."))
	}
}

// ---------------- notify ----------------

func notifyTool() llm.Tool {
	var t llm.Tool
	t.Type = "function"
	t.Function.Name = "notify"
	t.Function.Description = "Show a native desktop notification. Only for things the user must know while away from the terminal (a long task finished, something needs them). Never for greetings or progress chatter. Rate limited."
	t.Function.Parameters = map[string]any{"type": "object", "properties": map[string]any{
		"title":   map[string]any{"type": "string", "description": "Short title, under 60 characters."},
		"body":    map[string]any{"type": "string", "description": "One or two plain sentences."},
		"urgency": map[string]any{"type": "string", "enum": []string{"low", "normal", "critical"}, "description": "Default normal."},
	}, "required": []string{"title", "body"}}
	return t
}

func runNotify(raw string) string {
	var n notify.Note
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		return "Error in notify: the arguments weren't valid JSON."
	}
	if strings.TrimSpace(n.Title) == "" && strings.TrimSpace(n.Body) == "" {
		return "Error in notify: give a title and a body."
	}
	n.Kind = "tool"
	m, err := notify.Send(n)
	switch {
	case errors.Is(err, notify.ErrRateLimited):
		return "Not sent: too many notifications recently, or the same one was just sent. Don't retry."
	case errors.Is(err, notify.ErrNoMethod):
		_, missing := notify.CommandMethod()
		return "Not sent: this computer has no notification command. " + strings.Join(missing, "; ")
	case err != nil:
		return "Error in notify: " + err.Error()
	}
	return "Notification shown (" + m + ")."
}
