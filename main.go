// Blaze CLI: Blaze, the Android assistant, in your PC's terminal.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/agents"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/sched"
	"github.com/lacksonkapapahue/blaze-cli/internal/setup"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
	"github.com/lacksonkapapahue/blaze-cli/internal/update"
)

var version = "dev" // set by CI: -ldflags "-X main.version=v0.1.0"

func usage() {
	ui.Banner(version, "Blaze in your terminal.")
	ui.Box(ui.AccentLabel("commands"), []string{
		ui.Accent("blaze") + ui.Sub("                          chat in the current folder"),
		ui.Accent("blaze -c") + ui.Sub("                       continue the last chat here"),
		ui.Accent(`blaze "fix the failing test"`) + ui.Sub("   one task, then exit"),
		ui.Accent("blaze setup") + ui.Sub("                    detect this PC, add free models"),
		ui.Accent("blaze doctor") + ui.Sub("                   check models, shell, tools"),
		ui.Accent("blaze pc") + ui.Sub("                       what Blaze knows about this PC"),
		ui.Accent("blaze schedule list|remove") + ui.Sub("     scheduled jobs"),
		ui.Accent("blaze skills [name]") + ui.Sub("            skill library: list · show · on|off|delete|restore"),
		ui.Accent("blaze learn [on|off]") + ui.Sub("           learn memories and skills automatically"),
		ui.Accent("blaze login") + ui.Sub(" · ") + ui.Accent("logout") + ui.Sub(" · ") + ui.Accent("whoami") + ui.Sub("    your Blaze account (shared with Desktop)"),
		ui.Accent("blaze connect [app]") + ui.Sub("            connect Google, GitHub, Notion, Slack…"),
		ui.Accent("blaze keys [sync]") + ui.Sub("              your model keys (shared with Desktop and your phone)"),
		ui.Accent("blaze update") + ui.Sub("                   get the newest Blaze"),
		ui.Accent("blaze version"),
	})
	ui.Box(ui.AccentLabel("flags"), []string{
		ui.Ink("--autopilot") + ui.Sub("       nothing asks; catastrophic actions stay blocked (--yolo too)"),
		ui.Ink("--plan") + ui.Sub("            start in Plan mode: look and propose, change nothing"),
		ui.Ink("--model p/model") + ui.Sub("   try this model first (this run only)"),
		ui.Ink("-c, --continue") + ui.Sub("    pick up this folder's last conversation"),
	})
}

func main() {
	ui.Init()
	defer ui.ShowCursor()
	args := os.Args[1:]
	yolo, cont, plan := false, false, false
	var model string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--yolo" || a == "-y" || a == "--autopilot":
			yolo = true
		case a == "--plan":
			plan = true
		case a == "--continue" || a == "-c" || a == "--resume" || a == "-r":
			cont = true
		case a == "--model" && i+1 < len(args):
			model = args[i+1]
			i++
		case strings.HasPrefix(a, "--model="):
			model = strings.TrimPrefix(a, "--model=")
		default:
			rest = append(rest, a)
		}
	}
	cfg := config.Load()
	if model != "" {
		e, err := parseModel(cfg, model)
		if err != nil {
			fmt.Fprintln(os.Stderr, "blaze: --model: "+err.Error())
			os.Exit(2)
		}
		cfg.Override = &e // this run only; /model saves a choice
	}
	cmd := ""
	if len(rest) > 0 {
		cmd = rest[0]
	}
	switch cmd {
	case "help", "-h", "--help":
		usage()
		return
	case "update", "upgrade":
		force := len(rest) > 1 && (rest[1] == "--force" || rest[1] == "-f")
		ok, err := update.Run(version, force)
		if err != nil {
			ui.Println("  " + ui.Ink("■ ") + ui.Sub(err.Error()))
			os.Exit(1)
		}
		if ok {
			ui.Println("  " + ui.Weak("Run blaze to use it."))
		}
		return
	case "login", "logout", "whoami", "connect", "disconnect", "keys":
		if err := accountCmd(cmd, rest[1:]); err != nil {
			fail(err.Error())
			os.Exit(1)
		}
		return
	case "version", "--version", "-v":
		fmt.Println("blaze", version)
		return
	case "pc":
		setup.ShowPC(sysinfo.Detect())
		return
	case "setup":
		in := sysinfo.Detect()
		setup.Run(cfg, &in)
		return
	case "doctor":
		setup.Doctor(cfg, sysinfo.Detect(), version)
		return
	case "schedule":
		sub := ""
		if len(rest) > 1 {
			sub = rest[1]
		}
		switch sub {
		case "remove", "rm":
			if len(rest) < 3 {
				ui.Println("usage: blaze schedule remove NAME")
				return
			}
			ui.Println("  " + sched.Remove(rest[2]))
		default:
			ui.Println(sched.ListText())
			ui.Println(ui.Sub("  Add jobs by asking Blaze, e.g. \"every day at 7 back up ~/notes to my drive\"."))
		}
		return
	case "skills":
		seedSkills()
		skillsCmd(rest[1:])
		return
	case "learn":
		learnCmd(rest[1:])
		return
	case "run-job":
		if len(rest) < 2 {
			os.Exit(2)
		}
		os.Exit(runJob(cfg, rest[1]))
	}

	in := sysinfo.Detect()
	if !cfg.HasAnyModel() {
		if !setup.FromEnv(cfg) {
			if !ui.IsTTY() && len(rest) > 0 {
				fmt.Fprintln(os.Stderr, "Blaze isn't set up yet: run `blaze setup` or set GEMINI_API_KEY.")
				os.Exit(1)
			}
			setup.Run(cfg, &in)
			if !cfg.HasAnyModel() {
				return
			}
		}
	}
	cwd, _ := os.Getwd()
	seedSkills() // bundled skill library (skills_cmd.go)
	a := agent.New(cfg, &in, cwd, yolo)
	defer a.Close()
	if p := loadPrefs(); !yolo && !plan {
		a.Env.Plan = p.WorkMode == "plan"
		if p.Approval == "autopilot" {
			a.Approval = "autopilot"
		}
	}
	if plan {
		a.Env.Plan = true
	}

	// One-shot: everything after the flags is the task (stdin is appended when piped).
	if len(rest) > 0 {
		prompt := strings.Join(rest, " ")
		if st, _ := os.Stdin.Stat(); st != nil && st.Mode()&os.ModeCharDevice == 0 {
			if b, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20)); len(b) > 0 {
				prompt += "\n\n<stdin>\n" + string(b) + "\n</stdin>"
			}
		}
		wire(a, false)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if _, err := a.Ask(ctx, prompt); err != nil {
			ui.Println("\n  " + ui.Ink("■ ") + ui.Sub(err.Error()))
			os.Exit(1)
		}
		return
	}
	go autoSyncKeys()
	repl(a, wire(a, true), in, cont)
}

func accountCmd(cmd string, args []string) error {
	switch cmd {
	case "login":
		return cmdLogin(args)
	case "logout":
		return cmdLogout()
	case "whoami":
		return cmdWhoami()
	case "connect":
		return cmdConnect(args)
	case "disconnect":
		return cmdDisconnect(args)
	case "keys":
		return cmdKeys(args)
	}
	return nil
}

var slashCmds = []string{"/agents", "/auto", "/autopilot", "/build", "/clear", "/compact", "/connect", "/disconnect", "/doctor", "/exit", "/help", "/init", "/jobs", "/keys", "/learn", "/login", "/logout", "/model", "/new", "/notify", "/pc", "/plan", "/quit", "/resume", "/schedule", "/setup", "/skills", "/update", "/whoami", "/yolo"}

func repl(a *agent.Agent, bg *bgAgents, in sysinfo.Info, cont bool) {
	ui.Welcome(version, modelName(a), in.OSName+" · "+in.Shell+" · "+shortPath(a.Env.Root),
		ui.Weak("/ commands  ·  @ files  ·  ! shell  ·  # remember  ·  ⇧tab modes  ·  /agents"))
	ui.FocusReporting(true)
	defer ui.FocusReporting(false)
	bg.startupNotice()
	if tag := update.Hint(version); tag != "" {
		ui.Println("  " + ui.Accent("■ ") + ui.Ink("Blaze "+tag+" is out. ") + ui.Sub("Type ") + ui.Accent("/update") + ui.Sub(" to get it."))
	}
	if cont {
		resume(a)
	}
	menuCfg = a.Cfg
	ed := &ui.Editor{
		HistoryFile: filepath.Join(config.Dir(), "history"),
		Commands:    slashCmds,
		Menu:        slashMenu,
		Placeholder: "Ask anything, or give a task",
		Boxed:       true,
		OnShiftTab:  func() { cycleMode(a) },
	}
	ed.Glyph = func(line string) string {
		switch {
		case a.Env.Plan:
			return ui.Accent("▤ ")
		case a.Autopilot():
			return ui.Accent("» ")
		}
		return ui.Accent("› ")
	}
	ed.Footer = func(line string) string {
		switch {
		case strings.HasPrefix(line, "!"):
			return "  " + ui.Accent("! shell mode") + ui.Weak(" · runs here, Blaze sees the output")
		case strings.HasPrefix(line, "#"):
			return "  " + ui.Accent("# memory") + ui.Weak(" · Blaze keeps this for next time")
		}
		tail := ""
		if st := bg.statusLine(); st != "" {
			tail = ui.Weak("  ·  ") + st
		}
		switch {
		case a.Env.Plan:
			return "  " + ui.Accent("▤ plan") + ui.Weak(" · read and propose, nothing changes (shift+tab)") + tail
		case a.Autopilot():
			return "  " + ui.Accent("⏵⏵ build · autopilot") + ui.Weak(" · nothing asks (shift+tab)") + tail
		}
		return "  " + ui.Weak("build · auto · ? /help · @ files · ! shell · # remember · shift+tab modes") + tail
	}

	// Ctrl-C cancels the running turn (the prompt reads Ctrl-C itself).
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	var mu sync.Mutex
	var cancel context.CancelFunc
	go func() {
		for range sig {
			mu.Lock()
			c := cancel
			mu.Unlock()
			if c != nil {
				c()
			}
		}
	}()
	var run func(prompt string)
	run = func(prompt string) {
		ctx, c := context.WithCancel(context.Background())
		mu.Lock()
		cancel = c
		mu.Unlock()
		restore := ui.Quiet()
		_, err := a.Ask(ctx, prompt)
		restore()
		mu.Lock()
		cancel = nil
		mu.Unlock()
		c()
		a.Save()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				ui.Println("\n  " + ui.Weak("Stopped."))
			} else {
				ui.Println("\n  " + ui.Ink("■ ") + ui.Sub(err.Error()))
			}
			return
		}
		if restartAfterTurn {
			return
		}
		// Plan mode ends like Claude Code's: approve the plan and switch to Build.
		if a.Env.Plan && !strings.HasPrefix(prompt, agents.ResultPrefix) {
			ui.Println("")
			switch ui.Choose("  "+ui.Ink("Go ahead with this plan?")+"  ", []string{"Yes, Autopilot", "Yes, ask me (Auto)", "Keep planning"}, 1, 2) {
			case 0:
				setWork(a, false)
				setApproval(a, true)
			case 1:
				setWork(a, false)
				setApproval(a, false)
			default:
				return
			}
			run("The plan is approved. Carry it out now, step by step, keeping the checklist updated, and check your work.")
		}
	}

	// wake runs what background agents left for the main loop: their approvals and
	// questions first, then each finished agent's result as its own turn.
	wake := func() {
		for bg.pending() {
			bg.drainAsks()
			if v, ok := bg.nextResult(); ok {
				ui.Println("\n  " + ui.Accent("▸ ") + ui.Ink("agent "+v.Handle+" "+v.Status) + ui.Weak(" · Blaze reads its report"))
				run(agents.ResultMessage(v))
			}
		}
	}
	lastIdle := time.Time{}
	for {
		if restartAfterTurn {
			restartNow(a, bg)
			return
		}
		line, err := ed.Read(ui.Prompt())
		if errors.Is(err, ui.ErrWake) {
			wake()
			continue
		}
		if errors.Is(err, ui.ErrInterrupt) {
			if time.Since(lastIdle) < 2*time.Second {
				if confirmQuit(bg) {
					ui.Goodbye()
					return
				}
				continue
			}
			lastIdle = time.Now()
			ui.Println("  " + ui.Weak("Ctrl-C again to quit"))
			continue
		}
		if err != nil {
			bg.m.StopAll()
			ui.Goodbye()
			return
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case line == "?":
			help()
			continue
		case strings.HasPrefix(line, "!"):
			shellMode(a, strings.TrimSpace(line[1:]))
			continue
		case strings.HasPrefix(line, "#"):
			fact := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if fact != "" {
				b, _ := json.Marshal(map[string]string{"fact": fact})
				tools.Run(a.Env, "remember", string(b))
				ui.StepDone(true, "Remembered", fact, 0)
			}
			continue
		case strings.HasPrefix(line, "/"):
			if slash(a, bg, line, run) {
				if confirmQuit(bg) {
					ui.Goodbye()
					return
				}
			}
			continue
		}
		run(attachFiles(a, line))
	}
}

// attachFiles turns @path mentions into attached file contents.
func attachFiles(a *agent.Agent, line string) string {
	var extra strings.Builder
	for _, w := range strings.Fields(line) {
		if !strings.HasPrefix(w, "@") || len(w) < 2 {
			continue
		}
		p := strings.TrimRight(w[1:], ",.;:)")
		if strings.HasPrefix(p, "~") {
			h, _ := os.UserHomeDir()
			p = h + p[1:]
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(a.Env.Shell.Cwd(), p)
		}
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.IsDir() {
			ents, _ := os.ReadDir(p)
			var names []string
			for i, e := range ents {
				if i == 200 {
					break
				}
				n := e.Name()
				if e.IsDir() {
					n += "/"
				}
				names = append(names, n)
			}
			fmt.Fprintf(&extra, "\n\n<folder path=%q>\n%s\n</folder>", p, strings.Join(names, "\n"))
			ui.StepDone(true, "Attached "+relPath(a, p)+"/", fmt.Sprintf("%d entries", len(names)), 0)
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil || strings.ContainsRune(string(b[:min(len(b), 4000)]), 0) {
			continue
		}
		if len(b) > 100000 {
			b = append(b[:100000], []byte("\n…(cut)")...)
		}
		fmt.Fprintf(&extra, "\n\n<file path=%q>\n%s\n</file>", p, b)
		ui.StepDone(true, "Attached "+relPath(a, p), fmt.Sprintf("%d lines", strings.Count(string(b), "\n")+1), 0)
	}
	return line + extra.String()
}

// shellMode runs a command straight away (Claude Code's "!") and lets Blaze see it.
func shellMode(a *agent.Agent, cmd string) {
	if cmd == "" {
		return
	}
	var sp ui.Spinner
	sp.Start("$ " + cmd)
	t0 := time.Now()
	out := a.Env.Shell.Run(cmd, 10*time.Minute)
	sp.Stop()
	ok := strings.HasPrefix(out, "exit 0")
	ui.StepDone(ok, "$ "+cmd, "", time.Since(t0))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "exit ") {
		lines = lines[1:]
	}
	shown := lines
	if len(shown) > 40 {
		shown = append(append([]string{}, lines[:20]...), append([]string{fmt.Sprintf("… %d lines …", len(lines)-40)}, lines[len(lines)-20:]...)...)
	}
	for _, l := range shown {
		ui.Println("    " + ui.Body(l))
	}
	rec := out
	if len(rec) > 8000 {
		rec = rec[:4000] + "\n…\n" + rec[len(rec)-4000:]
	}
	a.History = append(a.History, llm.Message{Role: "user", Content: "<shell>\n$ " + cmd + "\n" + rec + "\n</shell>\n(I ran this myself; no reply needed unless I ask.)"},
		llm.Message{Role: "assistant", Content: "Noted."})
	a.Save()
}

func resume(a *agent.Agent) {
	n, err := a.Resume()
	if err != nil {
		ui.Step(err.Error())
		return
	}
	last := strings.SplitN(a.LastUser(), "\n", 2)[0]
	ui.StepDone(true, fmt.Sprintf("Resumed %d messages", n), "last: "+last, 0)
}

func help() {
	ui.Box(ui.AccentLabel("in chat"), []string{
		ui.Accent("/clear") + ui.Sub("     new conversation"),
		ui.Accent("/resume") + ui.Sub("    reload this folder's last conversation"),
		ui.Accent("/compact") + ui.Sub("   summarise the chat to free up context"),
		ui.Accent("/init") + ui.Sub("      write BLAZE.md: how this project builds, tests, runs"),
		ui.Accent("/plan") + ui.Sub("      Plan mode (look, then propose; nothing changes)   ") + ui.Accent("/build"),
		ui.Accent("/auto") + ui.Sub("      risky actions ask   ") + ui.Accent("/autopilot") + ui.Sub(" nothing asks (catastrophic stays blocked)"),
		ui.Accent("/agents") + ui.Sub("    background agents: list · <name> · msg · cancel · resume · discard · new"),
		ui.Accent("/login") + ui.Sub("     Blaze account   ") + ui.Accent("/whoami") + ui.Sub("   ") + ui.Accent("/logout") + ui.Sub("   ") + ui.Accent("/connect") + ui.Sub(" [app]   ") + ui.Accent("/keys") + ui.Sub(" [sync]"),
		ui.Accent("/model") + ui.Sub("     pick the model Blaze uses (saved)"),
		ui.Accent("/skills") + ui.Sub("    skill library: list · <name> · on|off|delete|restore <name>   ") + ui.Accent("/learn") + ui.Sub(" [on|off]"),
		ui.Accent("/jobs") + ui.Sub("      background jobs   ") + ui.Accent("/schedule") + ui.Sub(" scheduled jobs"),
		ui.Accent("/pc") + ui.Sub("        this PC   ") + ui.Accent("/doctor") + ui.Sub(" check everything   ") + ui.Accent("/setup"),
		ui.Accent("/update") + ui.Sub("    get the newest Blaze   ") + ui.Accent("/exit"),
	})
	ui.Box(ui.AccentLabel("keys"), []string{
		ui.Ink("@path") + ui.Sub("         attach a file or folder (tab completes)"),
		ui.Ink("!command") + ui.Sub("      run it yourself; Blaze sees the output"),
		ui.Ink("#note") + ui.Sub("         remember this for next time"),
		ui.Ink("shift+tab") + ui.Sub("     cycle: build·auto → build·autopilot → plan"),
		ui.Ink("↑ ↓") + ui.Sub("           history   ") + ui.Ink("→ / tab") + ui.Sub(" accept suggestion"),
		ui.Ink("ctrl+← →") + ui.Sub("      jump words   ") + ui.Ink("ctrl+w/u/k") + ui.Sub(" cut   ") + ui.Ink("ctrl+l") + ui.Sub(" clear"),
		ui.Ink("ctrl+c") + ui.Sub("        stop a task · twice on an empty line quits"),
	})
}

// confirmQuit asks before quitting while background agents run (they'd stop and
// show as interrupted next time, with /agents resume).
func confirmQuit(bg *bgAgents) bool {
	r, q := bg.m.Running()
	if r+q == 0 {
		return true
	}
	ui.Println("  " + ui.Ink(fmt.Sprintf("%d background agent%s still working.", r+q, plural(r+q))))
	if ui.Choose("  "+ui.Ink("Quit anyway? They stop and can be resumed next time.")+"  ", []string{"Quit", "Stay"}, 1, 1) != 0 {
		return false
	}
	bg.m.StopAll()
	return true
}

func slash(a *agent.Agent, bg *bgAgents, line string, run func(string)) bool {
	f := strings.Fields(line)
	switch f[0] {
	case "/exit", "/quit", "/q":
		return true
	case "/clear", "/new":
		a.Reset()
		ui.Step("New conversation.")
	case "/resume", "/continue":
		resume(a)
	case "/compact":
		var sp ui.Spinner
		sp.Start("compacting the conversation")
		n, err := a.Compact(context.Background())
		sp.Stop()
		if err != nil {
			ui.Step(err.Error())
		} else {
			ui.StepDone(true, fmt.Sprintf("Compacted %d messages into a summary", n), "", 0)
		}
	case "/init":
		run("Look around this project (file_list, read the README, build files and a few key sources) and write BLAZE.md in the project root for future sessions: what the project is, how to install, build, test and run it (exact commands), the layout of important folders, and conventions to follow. Under 60 lines, plain Markdown. If BLAZE.md exists, improve it instead.")
	case "/plan":
		setWork(a, !a.Env.Plan)
		ui.Step("Work mode: " + map[bool]string{true: "Plan (read and scout only; nothing changes).", false: "Build (every tool)."}[a.Env.Plan])
	case "/build":
		setWork(a, false)
		ui.Step("Work mode: Build (every tool).")
	case "/auto":
		setApproval(a, false)
		ui.Step("Approval mode: Auto (risky actions ask first).")
	case "/autopilot", "/yolo":
		setApproval(a, !a.Autopilot() || f[0] == "/autopilot")
		if a.Autopilot() {
			ui.Step("Approval mode: Autopilot (nothing asks; catastrophic actions stay blocked; questions get \"decide yourself\").")
		} else {
			ui.Step("Approval mode: Auto (risky actions ask first).")
		}
	case "/agents":
		bg.command(f[1:])
	case "/login", "/logout", "/whoami", "/connect", "/disconnect", "/keys":
		if err := accountCmd(strings.TrimPrefix(f[0], "/"), f[1:]); err != nil {
			fail(err.Error())
		}
	case "/notify":
		msg := strings.TrimSpace(strings.TrimPrefix(line, f[0]))
		if msg == "" {
			msg = "Notifications work."
		}
		ui.Println("  " + ui.Sub(runNotify(fmt.Sprintf(`{"title":"Blaze","body":%q}`, msg))))
	case "/pc":
		setup.ShowPC(*a.Env.Info)
	case "/doctor":
		setup.Doctor(a.Cfg, *a.Env.Info, version)
	case "/setup":
		setup.Run(a.Cfg, a.Env.Info)
	case "/update":
		ok, err := update.Run(version, len(f) > 1 && f[1] == "--force")
		if err != nil {
			ui.Println("  " + ui.Ink("■ ") + ui.Sub(err.Error()))
		} else if ok {
			restartAfterTurn = true // the REPL restarts into the new binary
		}
	case "/skills":
		skillsCmd(f[1:])
	case "/learn":
		learnCmd(f[1:])
	case "/jobs":
		ui.Println(tools.Run(a.Env, "job_log", "{}"))
	case "/schedule":
		ui.Println(sched.ListText())
	case "/model":
		modelCmd(a, f[1:])
	default:
		help()
	}
	return false
}

// runJob is what the OS scheduler calls.
func runJob(cfg *config.Config, name string) int {
	j, ok := sched.Get(name)
	if !ok {
		return 2
	}
	log, err := sched.LogWriter(name)
	if err != nil {
		return 1
	}
	defer log.Close()
	fmt.Fprintf(log, "\n=== %s ===\n", time.Now().Format("2006-01-02 15:04:05"))
	in := sysinfo.Detect()
	dir := j.Dir
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		dir = in.Home
	}
	if j.Command != "" {
		sh := tools.NewShell(in.Shell, in.ShellPath, dir)
		r := sh.Run(j.Command, 2*time.Hour)
		fmt.Fprintln(log, r)
		if strings.HasPrefix(r, "exit 0") {
			return 0
		}
		return 1
	}
	seedSkills()
	a := agent.New(cfg, &in, dir, false)
	a.Quiet = true
	a.Env.Confirm = func(title, detail string) bool {
		fmt.Fprintf(log, "[declined, nobody to ask] %s %s\n", title, detail)
		return false
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	out, err := a.Ask(ctx, j.Prompt)
	if err != nil {
		fmt.Fprintln(log, "Error:", err)
		return 1
	}
	fmt.Fprintln(log, out)
	return 0
}

func relPath(a *agent.Agent, p string) string {
	if r, err := filepath.Rel(a.Env.Shell.Cwd(), p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return shortPath(p)
}

func shortPath(p string) string {
	h, _ := os.UserHomeDir()
	if h != "" && strings.HasPrefix(p, h) {
		return "~" + p[len(h):]
	}
	return p
}
