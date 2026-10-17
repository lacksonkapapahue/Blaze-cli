package main

// MCP servers: `blaze mcp …`, `/mcp` in chat, the doctor check, and the wiring
// that offers the servers' tools to the agent (see internal/mcp). Servers live
// in ~/.blaze/mcp.json (shared with Blaze Desktop) plus the folder's
// .mcp.json and .blaze/mcp.json.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/mcp"
	"github.com/lacksonkapapahue/blaze-cli/internal/rea"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

var (
	mcpOnce sync.Once
	mcpMgr  *mcp.Manager
)

// getMCP is this session's MCP manager (servers start with startMCP).
func getMCP() *mcp.Manager {
	mcpOnce.Do(func() {
		cwd, _ := os.Getwd()
		mcpMgr = mcp.New(config.Dir(), cwd, version)
		mcpMgr.Log = func(s string) {
			ui.Above("  " + ui.Accent("■ ") + ui.Sub(s) + ui.Weak(" · /mcp for details"))
		}
	})
	return mcpMgr
}

// startMCP launches the configured servers in the background, so startup isn't
// slowed. Project servers of an untrusted folder are skipped with a notice.
func startMCP() { startMCPSession(false) }

// startMCPSession is startMCP for a session: chat (interactive, with a
// terminal) asks once whether to trust the folder's project servers.
func startMCPSession(chat bool) {
	interactive := chat && ui.IsTTY()
	if st, _ := os.Stdin.Stat(); st == nil || st.Mode()&os.ModeCharDevice == 0 {
		interactive = false
	}
	cwd, _ := os.Getwd()
	askProjectTrust(config.Dir(), cwd, interactive)
	getMCP().Start()
}

// askProjectTrust handles a folder whose .mcp.json / .blaze/mcp.json isn't
// trusted (or changed since): in chat it lists each server's command or URL
// and asks y/N, saving the trust on yes; otherwise it prints a one-line notice.
// Untrusted project servers never start.
func askProjectTrust(home, dir string, interactive bool) {
	p := mcp.LoadProject(home, dir)
	if len(p.Servers) == 0 || p.Trusted() {
		return
	}
	what := "isn't trusted"
	if p.Trust == mcp.TrustChanged {
		what = "changed since you trusted it"
	}
	if !interactive {
		fmt.Fprintf(os.Stderr, "blaze: skipped %d MCP server%s from this folder's project config (%s); run `blaze mcp trust` here to allow them.\n", len(p.Servers), plural(len(p.Servers)), what)
		return
	}
	rows := []string{ui.Sub("This folder's MCP config " + what + ". Starting these runs them on your PC:")}
	rows = append(rows, projectServerRows(p)...)
	ui.Box(ui.AccentLabel("project mcp servers"), rows)
	ans, err := ui.ReadLine("  " + ui.Ink("Trust this folder and start them? ") + ui.Weak("[y/N] "))
	if err == nil {
		switch strings.ToLower(strings.TrimSpace(ans)) {
		case "y", "yes":
			if err := mcp.Trust(home, dir, p.Hash); err != nil {
				ui.Println("  " + ui.Ink("■ ") + ui.Sub("couldn't save the trust: "+err.Error()))
				return
			}
			ui.Println("  " + ui.Accent("■ ") + ui.Sub("Trusted. They start now; editing the file asks again."))
			return
		}
	}
	ui.Println("  " + ui.Weak("Not started. `blaze mcp trust` here (or /mcp trust) allows them later."))
}

// projectServerRows lists project servers with what they run and where they're defined.
func projectServerRows(p mcp.Project) []string {
	var rows []string
	for _, s := range p.Servers {
		rows = append(rows, ui.Ink(fmt.Sprintf("%-14s", s.Name))+ui.Sub(" "+s.Kind()+" ")+ui.Accent(clipStr(s.Target(), 200)))
		extra := shortPath(s.Source)
		if len(s.Env) > 0 || len(s.Headers) > 0 {
			var ks []string
			for k := range s.Env {
				ks = append(ks, k)
			}
			for k := range s.Headers {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			extra += " · sets " + strings.Join(ks, ", ")
		}
		rows = append(rows, "    "+ui.Weak(extra))
	}
	return rows
}

// keepProjectTrust runs an edit of file and, when file is this folder's project
// file and the folder was trusted (or had no project servers) before, trusts
// the result: the user's own `blaze mcp add --project` shouldn't lock them out.
func keepProjectTrust(home, dir, file string, edit func() error) error {
	if !mcp.IsProjectFile(home, dir, file) {
		return edit()
	}
	before := mcp.LoadProject(home, dir)
	if err := edit(); err != nil {
		return err
	}
	if before.Trusted() || len(before.Servers) == 0 && len(before.Warnings) == 0 {
		if after := mcp.LoadProject(home, dir); after.Hash != "" {
			return mcp.Trust(home, dir, after.Hash)
		}
	}
	return nil
}

// stopMCP stops every server (on exit and before a restart).
func stopMCP() {
	if mcpMgr != nil {
		mcpMgr.Close()
	}
}

// mcpPolicy is how the agent's MCP calls are gated: Plan refuses tools that
// aren't read-only, Auto asks with the usual approval prompt (with "Always
// allow"), except REA tools explicitly allowlisted as static. Risky REA tools
// always ask, including in Autopilot.
func mcpPolicy(a *agent.Agent) mcp.Policy {
	return mcp.Policy{
		Plan:      a.Env.Plan,
		Autopilot: a.Autopilot(),
		Risky: func(server, tool string) bool {
			return strings.EqualFold(server, rea.ServerName) && rea.RiskyToolName(tool)
		},
		AutoReadOnly: func(server mcp.Server, tool string) bool {
			return isPinnedGlobalREA(server) && rea.AutoReadOnlyToolName(tool)
		},
		Ask: func(ctx context.Context, title, detail string) (bool, bool) {
			a.StopSpinner()
			return confirmHook(ctx, title, detail, true)
		},
		AskRisky: func(ctx context.Context, title, detail string) (bool, bool) {
			a.StopSpinner()
			return confirmHook(ctx, title, detail, false)
		},
	}
}

// ---------------- blaze mcp ----------------

func mcpUsage() {
	ui.Box(ui.AccentLabel("mcp"), []string{
		ui.Accent("blaze mcp list") + ui.Sub("                          servers, whether they start, their tools"),
		ui.Accent("blaze mcp add NAME -- CMD ARGS…") + ui.Sub("        a local server (stdio); --env K=V before --"),
		ui.Accent("blaze mcp add NAME --url URL") + ui.Sub("           a remote server; --header K=V (repeatable)"),
		ui.Accent("blaze mcp remove NAME") + ui.Sub("                  remove it (and its \"Always allow\" choices)"),
		ui.Accent("blaze mcp enable|disable NAME") + ui.Sub("          turn it on or off"),
		ui.Accent("blaze mcp trust|untrust") + ui.Sub("                allow (or stop) this folder's .mcp.json servers"),
		ui.Weak("--project: write ./.blaze/mcp.json, not ~/.blaze/mcp.json (shared with Desktop)"),
	})
}

// cmdMCP is `blaze mcp …`; inChat is true for /mcp (which then restarts the servers).
func cmdMCP(args []string, inChat bool) error {
	cwd, _ := os.Getwd()
	home := config.Dir()
	if len(args) == 0 {
		if inChat {
			mcpStatus(getMCP(), false)
			return nil
		}
		args = []string{"list"}
	}
	sub, rest := args[0], args[1:]
	changed := false
	switch sub {
	case "list", "ls", "status":
		m := getMCP()
		if !inChat {
			m.Log = nil // the box below says what failed
			m.Start()
			defer m.Close()
		}
		mcpStatus(m, !inChat)
		return nil
	case "add":
		s, project, err := parseMCPAdd(rest)
		if err != nil {
			return err
		}
		file := mcp.GlobalFile(home)
		if project {
			file = filepath.Join(cwd, ".blaze", "mcp.json")
		}
		if err := keepProjectTrust(home, cwd, file, func() error { return mcp.Add(file, s) }); err != nil {
			return err
		}
		ui.StepDone(true, "Added MCP server "+s.Name, shortPath(file), 0)
		changed = true
	case "remove", "rm", "delete":
		if len(rest) == 0 {
			return errors.New("use: blaze mcp remove NAME")
		}
		name := rest[0]
		file := mcp.FileDefining(home, cwd, name)
		if file == "" {
			return errors.New("there's no MCP server called " + name + "; `blaze mcp list` shows them")
		}
		if err := keepProjectTrust(home, cwd, file, func() error { _, err := mcp.Remove(file, name); return err }); err != nil {
			return err
		}
		_ = getMCP().Forget(name)
		ui.StepDone(true, "Removed MCP server "+name, shortPath(file), 0)
		changed = true
	case "enable", "disable", "on", "off":
		if len(rest) == 0 {
			return errors.New("use: blaze mcp " + sub + " NAME")
		}
		name := rest[0]
		file := mcp.FileDefining(home, cwd, name)
		if file == "" {
			return errors.New("there's no MCP server called " + name + "; `blaze mcp list` shows them")
		}
		off := sub == "disable" || sub == "off"
		if err := keepProjectTrust(home, cwd, file, func() error { _, err := mcp.SetDisabled(file, name, off); return err }); err != nil {
			return err
		}
		ui.StepDone(true, map[bool]string{true: "Turned off ", false: "Turned on "}[off]+name, shortPath(file), 0)
		changed = true
	case "trust":
		p := mcp.LoadProject(home, cwd)
		if len(p.Servers) == 0 {
			return errors.New("this folder has no project MCP servers (.mcp.json or .blaze/mcp.json)")
		}
		if err := mcp.Trust(home, cwd, p.Hash); err != nil {
			return err
		}
		ui.Box(ui.AccentLabel("trusted"), append([]string{ui.Sub("These may now start in " + shortPath(p.Dir) + ":")}, projectServerRows(p)...))
		ui.Println("  " + ui.Weak("Editing the project file makes it untrusted again. `blaze mcp untrust` undoes this."))
		changed = true
	case "untrust":
		ok, err := mcp.Untrust(home, cwd)
		if err != nil {
			return err
		}
		if !ok {
			ui.Println("  " + ui.Weak("This folder wasn't trusted."))
			return nil
		}
		ui.StepDone(true, "Untrusted this folder", shortPath(mcp.FolderKey(cwd)), 0)
		changed = true
	case "reload", "restart":
		if !inChat {
			return errors.New("reload is for /mcp in chat; servers start fresh every time you run blaze")
		}
		changed = true
	case "help", "-h", "--help":
		mcpUsage()
		return nil
	default:
		mcpUsage()
		return errors.New("unknown mcp command " + sub)
	}
	if changed && inChat {
		reloadMCPInChat()
	}
	return nil
}

func reloadMCPInChat() {
	var sp ui.Spinner
	sp.Start("Restarting MCP servers")
	m := getMCP()
	m.Reload()
	m.Wait(15 * time.Second)
	sp.Stop()
	mcpStatus(m, false)
}

func isPinnedGlobalREA(server mcp.Server) bool {
	if !strings.EqualFold(server.Name, rea.ServerName) || server.Project || server.Command != "npx" ||
		server.URL != "" || len(server.Args) != 3 ||
		server.Args[0] != "-y" || server.Args[1] != rea.Package || server.Args[2] != "mcp" {
		return false
	}
	source, err := filepath.Abs(server.Source)
	if err != nil {
		return false
	}
	global, err := filepath.Abs(mcp.GlobalFile(config.Dir()))
	return err == nil && filepath.Clean(source) == filepath.Clean(global)
}

// parseMCPAdd reads `NAME [--project] [--env K=V]… -- CMD ARGS…` or
// `NAME --url URL [--header K=V]…`.
func parseMCPAdd(a []string) (mcp.Server, bool, error) {
	var s mcp.Server
	project := false
	if len(a) == 0 || strings.HasPrefix(a[0], "-") {
		return s, false, errors.New("use: blaze mcp add NAME -- COMMAND ARGS…  or  blaze mcp add NAME --url URL")
	}
	s.Name = a[0]
	kv := func(v, what string) (string, string, error) {
		k, val, ok := strings.Cut(v, "=")
		if !ok || strings.TrimSpace(k) == "" {
			// Also accept "Header: value".
			if k2, v2, ok2 := strings.Cut(v, ":"); ok2 && what == "header" {
				return strings.TrimSpace(k2), strings.TrimSpace(v2), nil
			}
			return "", "", fmt.Errorf("--%s wants KEY=VALUE, got %q", what, v)
		}
		return strings.TrimSpace(k), val, nil
	}
	for i := 1; i < len(a); i++ {
		x := a[i]
		next := func() (string, error) {
			if i+1 >= len(a) {
				return "", errors.New(x + " needs a value")
			}
			i++
			return a[i], nil
		}
		switch {
		case x == "--":
			if i+1 >= len(a) {
				return s, false, errors.New("give the server's command after --")
			}
			s.Command, s.Args = a[i+1], append([]string{}, a[i+2:]...)
			i = len(a)
		case x == "--url" || strings.HasPrefix(x, "--url="):
			v, err := flagVal(x, "--url", next)
			if err != nil {
				return s, false, err
			}
			s.URL = v
		case x == "--header" || x == "-H" || strings.HasPrefix(x, "--header="):
			v, err := flagVal(x, "--header", next)
			if err != nil {
				return s, false, err
			}
			k, val, err := kv(v, "header")
			if err != nil {
				return s, false, err
			}
			if s.Headers == nil {
				s.Headers = map[string]string{}
			}
			s.Headers[k] = val
		case x == "--env" || x == "-e" || strings.HasPrefix(x, "--env="):
			v, err := flagVal(x, "--env", next)
			if err != nil {
				return s, false, err
			}
			k, val, err := kv(v, "env")
			if err != nil {
				return s, false, err
			}
			if s.Env == nil {
				s.Env = map[string]string{}
			}
			s.Env[k] = val
		case x == "--project":
			project = true
		default:
			return s, false, fmt.Errorf("unexpected %q: put the server's command after --", x)
		}
	}
	if s.URL != "" && s.Command != "" {
		return s, false, errors.New("give either --url or a command after --, not both")
	}
	if s.URL == "" && s.Command == "" {
		return s, false, errors.New("give the server's command after -- (blaze mcp add NAME -- npx -y some-server) or --url URL")
	}
	if s.URL != "" && !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://") {
		return s, false, errors.New("--url must start with http:// or https://")
	}
	return s, project, mcp.ValidName(s.Name)
}

func flagVal(x, flag string, next func() (string, error)) (string, error) {
	if v, ok := strings.CutPrefix(x, flag+"="); ok {
		return v, nil
	}
	return next()
}

// mcpStatus prints each server: connected (tools), failed (why), disabled.
// wait is true for `blaze mcp list` (start them and wait for the answer).
func mcpStatus(m *mcp.Manager, wait bool) {
	if wait {
		var sp ui.Spinner
		sp.Start("Starting MCP servers")
		m.Wait(mcp.InitTimeout + 5*time.Second)
		sp.Stop()
	}
	for _, w := range m.Warnings() {
		ui.Println("  " + ui.Ink("■ ") + ui.Sub(w))
	}
	sts := m.States()
	if len(sts) == 0 {
		ui.Box(ui.AccentLabel("mcp servers"), []string{
			ui.Sub("No MCP servers yet. Add one:"),
			ui.Accent("blaze mcp add github -- npx -y @modelcontextprotocol/server-github"),
			ui.Accent("blaze mcp add docs --url https://example.com/mcp --header Authorization=\"Bearer …\""),
			ui.Weak("They live in " + shortPath(mcp.GlobalFile(config.Dir())) + " (shared with Blaze Desktop) or the folder's .mcp.json."),
		})
		return
	}
	var rows []string
	for _, st := range sts {
		mark, word := ui.Weak("□ "), st.Status
		switch st.Status {
		case mcp.Connected:
			mark = ui.Accent("■ ")
			word = fmt.Sprintf("connected · %d tool%s", len(st.Tools), plural(len(st.Tools)))
		case mcp.Failed:
			mark = ui.Ink("✕ ")
			word = "failed"
		case mcp.Starting:
			word = "starting…"
		case mcp.Untrusted:
			mark = ui.Ink("! ")
			word = "untrusted · not started"
		}
		rows = append(rows, mark+ui.Ink(fmt.Sprintf("%-14s", st.Server.Name))+ui.Sub(fmt.Sprintf(" %-6s %s", st.Server.Kind(), word)))
		rows = append(rows, "    "+ui.Weak(clipStr(st.Server.Target(), 70)+" · "+shortPath(st.Server.Source)))
		if st.Err != "" {
			rows = append(rows, "    "+ui.Sub(clipStr(st.Err, 110)))
		}
		if st.Status == mcp.Connected && len(st.Tools) > 0 {
			var names []string
			for _, t := range st.Tools {
				names = append(names, t.Name)
			}
			rows = append(rows, "    "+ui.Weak(clipStr(strings.Join(names, ", "), 110)))
		}
	}
	ui.Box(ui.AccentLabel("mcp servers"), rows)
	cwd, _ := os.Getwd()
	switch p := mcp.LoadProject(config.Dir(), cwd); {
	case len(p.Servers) == 0:
	case p.Trusted():
		ui.Println("  " + ui.Accent("■ ") + ui.Sub("This folder's project servers are trusted.") + ui.Weak(" blaze mcp untrust to stop them"))
	case p.Trust == mcp.TrustChanged:
		ui.Println("  " + ui.Ink("! ") + ui.Sub("This folder's project MCP file changed since you trusted it, so its servers didn't start.") + ui.Weak(" blaze mcp trust to allow them"))
	default:
		ui.Println("  " + ui.Ink("! ") + ui.Sub("This folder's project servers aren't trusted, so they didn't start.") + ui.Weak(" blaze mcp trust to allow them"))
	}
	ui.Println("  " + ui.Weak("blaze mcp add · remove · enable · disable   (in chat: /mcp reload after editing mcp.json)"))
}

func clipStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// mcpDoctor adds one doctor line per server: does it start and list its tools.
func mcpDoctor(add func(name string, ok bool, detail string)) {
	cwd, _ := os.Getwd()
	m := mcp.New(config.Dir(), cwd, version)
	m.Start()
	defer m.Close()
	m.Wait(mcp.InitTimeout + 5*time.Second)
	reaStatus := rea.Check(config.Dir())
	reaSeen := false
	for _, w := range m.Warnings() {
		add("MCP config", false, w)
	}
	for _, st := range m.States() {
		if strings.EqualFold(st.Server.Name, rea.ServerName) {
			reaSeen = true
			switch {
			case !reaStatus.NodeSupported:
				detail := "Node too old"
				if !reaStatus.NodeFound {
					detail = "Node missing"
				}
				add("REA", false, detail+" · needs 22.19+, 24.11+ or 26+")
			case st.Status == mcp.Connected && len(st.Tools) > 0:
				add("REA", true, fmt.Sprintf("connected · %d tools", len(st.Tools)))
			default:
				add("REA", false, "not connected · run `blaze rea status`")
			}
			continue
		}
		name := "MCP " + st.Server.Name
		switch st.Status {
		case mcp.Connected:
			add(name, true, fmt.Sprintf("%d tools · %.1fs", len(st.Tools), st.Took.Seconds()))
		case mcp.Disabled:
			add(name, true, "turned off")
		case mcp.Untrusted:
			add(name, true, "project server not trusted, not started (blaze mcp trust)")
		case mcp.Starting:
			add(name, false, "didn't finish starting in time")
		default:
			add(name, false, st.Err)
		}
	}
	if !reaSeen {
		if !reaStatus.NodeSupported {
			detail := "Node too old"
			if !reaStatus.NodeFound {
				detail = "Node missing"
			}
			add("REA", false, detail+" · needs 22.19+, 24.11+ or 26+")
		} else {
			add("REA", false, "not connected · run `blaze rea enable`")
		}
	}
}
