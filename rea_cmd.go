package main

import (
	"errors"
	"runtime"
	"strings"
	"sync"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/mcp"
	"github.com/lacksonkapapahue/blaze-cli/internal/rea"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

var reaPromptMu sync.Mutex
var reaPrompted bool

func cmdREA(args []string, inChat bool) error {
	sub := "status"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
	case "status":
		showREAStatus()
		return nil
	case "enable":
		status := rea.Check(config.Dir())
		if !status.Ready() {
			showREAStatus()
			return errors.New("fix the missing REA prerequisites above, then run `blaze rea enable` again")
		}
		if !ui.IsTTY() {
			return errors.New("`blaze rea enable` needs an interactive Yes/No confirmation; run it in a terminal")
		}
		showREAConfigTarget()
		if !ui.Confirm("Connect REA MCP server?", reaEnableDetail()) {
			ui.Step("REA was not connected.")
			return nil
		}
		return enableREA(inChat)
	case "disable":
		file := rea.GlobalFile(config.Dir())
		backup, changed, err := rea.DisableFile(file)
		if err != nil {
			return err
		}
		if !changed {
			ui.Step("REA is not registered in the global MCP config.")
			return nil
		}
		if backup != "" {
			ui.Step("Backed up MCP config to " + backup)
		}
		if inChat {
			getMCP().Reload()
			ui.Step("REA removed; MCP servers reloaded.")
		} else {
			ui.Step("REA removed. Restart Blaze to apply the change.")
		}
		return nil
	default:
		return errors.New("use `blaze rea status`, `blaze rea enable`, or `blaze rea disable`")
	}
}

func reaEnableDetail() string {
	return `"rea": {"command":"npx","args":["-y","rea-agents@6.5.0","mcp"]}` +
		"\n\nnpx also installs REA's optional Frida dependency."
}

func showREAConfigTarget() {
	ui.Step("Global MCP config: " + rea.GlobalFile(config.Dir()))
}

func enableREA(inChat bool) error {
	status := rea.Check(config.Dir())
	if !status.Ready() {
		showREAStatus()
		return errors.New("fix the missing REA prerequisites above, then run `blaze rea enable` again")
	}
	backup, changed, err := rea.EnableFile(rea.GlobalFile(config.Dir()))
	if err != nil {
		return err
	}
	if backup != "" {
		ui.Step("Backed up MCP config to " + backup)
	}
	if !changed {
		ui.Step("REA is already registered with the pinned command.")
	}
	if inChat {
		getMCP().Reload()
		ui.Step("REA MCP server reload started.")
	} else {
		ui.Step("REA added to the global MCP config. Restart Blaze to load it.")
	}
	return nil
}

func showREAStatus() {
	s := rea.Check(config.Dir())
	switch {
	case s.NodeFound && s.NodeSupported:
		ui.Println("  " + ui.Ink("Node: ") + ui.Sub(s.NodeVersion+" (supported; needs 22.19+, 24.11+ or 26+)"))
	case s.NodeFound && s.NodeVersion != "":
		ui.Println("  " + ui.Ink("Node: ") + ui.Ink(s.NodeVersion+" (too old; needs 22.19+, 24.11+ or 26+)"))
	case s.NodeFound:
		ui.Println("  " + ui.Ink("Node: ") + ui.Ink("couldn't read version"+reaErrorSuffix(s.NodeError)))
	default:
		ui.Println("  " + ui.Ink("Node: ") + ui.Sub("not found"+reaErrorSuffix(s.NodeError)))
	}
	if s.NpxPath == "" {
		ui.Println("  " + ui.Ink("npx: ") + ui.Ink("not found"))
	} else {
		ui.Println("  " + ui.Ink("npx: ") + ui.Sub(s.NpxPath))
	}
	if s.Registered {
		ui.Println("  " + ui.Ink("REA MCP server: ") + ui.Sub("registered"))
	} else {
		ui.Println("  " + ui.Ink("REA MCP server: ") + ui.Sub("not registered"))
	}
	if s.ConfigError != "" {
		ui.Println("  " + ui.Ink("MCP config: ") + ui.Ink(s.ConfigError))
	}
	if runtime.GOOS == "linux" {
		if s.Procps {
			ui.Println("  " + ui.Ink("Linux procps ps: ") + ui.Sub("present"))
		} else {
			ui.Println("  " + ui.Ink("Linux procps ps: ") + ui.Ink("not found"))
		}
	}
	fixes := s.Fixes(runtime.GOOS)
	if len(fixes) > 0 {
		ui.Println("  " + ui.Ink("Fix:"))
		for _, fix := range fixes {
			ui.Println("    " + ui.Accent(fix))
		}
	}
}

func reaErrorSuffix(err string) string {
	if err == "" {
		return ""
	}
	return " (" + err + ")"
}

func reaPromptShouldTrigger(prompt string) bool {
	q := strings.ToLower(prompt)
	artifact := strings.Contains(q, "apk") ||
		strings.Contains(q, "electron") || strings.Contains(q, "asar") ||
		strings.Contains(q, ".net assembly") || strings.Contains(q, "firmware") ||
		strings.Contains(q, "binary") && !strings.Contains(q, "binary search")
	if (strings.Contains(q, "ordinary source") || strings.Contains(q, "source repository") || strings.Contains(q, "source-code repo")) && !artifact {
		return false
	}
	if strings.Contains(q, "reverse engineer") || strings.Contains(q, "decompile") ||
		strings.Contains(q, "compare versions") || artifact {
		return true
	}
	if strings.Contains(q, " how ") && strings.Contains(q, " works in ") {
		return true
	}
	if strings.Contains(q, "how does ") && strings.Contains(q, " do ") {
		return true
	}
	return false
}

func reaToolsConnected() bool {
	m := getMCP()
	for _, state := range m.States() {
		if strings.EqualFold(state.Server.Name, rea.ServerName) {
			return state.Status == mcp.Connected && len(state.Tools) > 0 || state.Status == mcp.Starting
		}
	}
	return false
}

func maybeConnectREA(prompt string) {
	if !reaPromptShouldTrigger(prompt) || reaToolsConnected() {
		return
	}
	reaPromptMu.Lock()
	if reaPrompted {
		reaPromptMu.Unlock()
		return
	}
	reaPrompted = true
	reaPromptMu.Unlock()

	status := rea.Check(config.Dir())
	if !ui.IsTTY() {
		ui.Println("\n  " + ui.Ink("REA isn't connected. ") + ui.Sub("Use `blaze rea status` and `blaze rea enable` interactively, or use the pinned CLI fallback in the bundled skill."))
		return
	}
	if !status.Ready() {
		showREAStatus()
	}
	showREAConfigTarget()
	detail := reaEnableDetail()
	if !status.Ready() {
		ui.Step("Prerequisites are missing; approval will show fixes without changing the config.")
	}
	if !ui.Confirm("REA isn't connected. Connect it now? (runs npx rea-agents@6.5.0 locally)", detail) {
		ui.Step("REA not connected; use the pinned CLI fallback in the bundled skill.")
		return
	}
	if err := enableREA(true); err != nil {
		ui.Println("  " + ui.Ink("■ ") + ui.Sub("couldn't connect REA: "+err.Error()))
	}
}
