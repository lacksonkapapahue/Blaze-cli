// Blaze CLI: Blaze, the Android assistant, in your PC's terminal.
package main

import (
	"context"
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
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/sched"
	"github.com/lacksonkapapahue/blaze-cli/internal/setup"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

var version = "dev" // set by CI: -ldflags "-X main.version=v0.1.0"

func usage() {
	ui.Banner(version, "Blaze in your terminal.")
	ui.Numbered([]string{
		ui.Accent("blaze") + ui.Sub("                       chat in the current folder"),
		ui.Accent(`blaze "fix the failing test"`) + ui.Sub("  one task, then exit"),
		ui.Accent("blaze setup") + ui.Sub("                 detect this PC, add free models, install tools"),
		ui.Accent("blaze doctor") + ui.Sub("                check models, shell, tools, scheduler"),
		ui.Accent("blaze pc") + ui.Sub("                    show what Blaze detected about this PC"),
		ui.Accent("blaze schedule list|remove NAME") + ui.Sub(" scheduled jobs"),
		ui.Accent("blaze version"),
	})
	ui.Println("\n  " + ui.Label("flags") + "  " + ui.Ink("--yolo") + ui.Sub(" skip approval prompts (deletes still ask)  ") + ui.Ink("--model provider/model"))
}

func main() {
	ui.Init()
	defer ui.ShowCursor()
	args := os.Args[1:]
	yolo := false
	var model string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--yolo" || a == "-y":
			yolo = true
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
		prov, m, _ := strings.Cut(model, "/")
		cfg.Chain = append([]config.Entry{{Provider: prov, Model: m}}, cfg.Chain...)
	}
	cmd := ""
	if len(rest) > 0 {
		cmd = rest[0]
	}
	switch cmd {
	case "help", "-h", "--help":
		usage()
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
	a := agent.New(cfg, &in, cwd, yolo)
	defer a.Close()

	// One-shot: everything after the flags is the task (stdin is appended when piped).
	if len(rest) > 0 {
		prompt := strings.Join(rest, " ")
		if st, _ := os.Stdin.Stat(); st != nil && st.Mode()&os.ModeCharDevice == 0 {
			if b, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20)); len(b) > 0 {
				prompt += "\n\n<stdin>\n" + string(b) + "\n</stdin>"
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if _, err := a.Ask(ctx, prompt); err != nil {
			ui.Println("\n  " + ui.Ink("■ ") + ui.Sub(err.Error()))
			os.Exit(1)
		}
		return
	}
	repl(a, in)
}

func repl(a *agent.Agent, in sysinfo.Info) {
	ui.BannerAnimated(version, in.OSName+" · "+in.Shell+" · "+shortPath(a.Env.Root))
	ui.Println("  " + ui.Sub("Ask anything, or give a task. ") + ui.Weak("/help commands · ↑ history · tab complete · ctrl-c stops a task"))
	ed := &ui.Editor{
		HistoryFile: filepath.Join(config.Dir(), "history"),
		Commands:    []string{"/clear", "/doctor", "/exit", "/help", "/jobs", "/model", "/new", "/pc", "/quit", "/schedule", "/setup", "/yolo"},
		Placeholder: "Ask anything · ↑ history · / commands",
	}

	// Ctrl-C cancels the running turn; at the prompt, twice in a row exits.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	var mu sync.Mutex
	var cancel context.CancelFunc
	lastIdle := time.Time{}
	go func() {
		for range sig {
			mu.Lock()
			c := cancel
			mu.Unlock()
			if c != nil {
				c()
				continue
			}
			if time.Since(lastIdle) < 2*time.Second {
				ui.Println("")
				ui.ShowCursor()
				a.Close()
				os.Exit(0)
			}
			lastIdle = time.Now()
			ui.Printf("\n  %s%s", ui.Weak("Ctrl-C again to quit"), ui.Prompt())
		}
	}()

	for {
		line, err := ed.Read(ui.Prompt())
		if errors.Is(err, ui.ErrInterrupt) {
			// In raw mode Ctrl-C arrives as a key, not a signal: twice quits.
			if time.Since(lastIdle) < 2*time.Second {
				ui.Goodbye()
				return
			}
			lastIdle = time.Now()
			ui.Println("  " + ui.Weak("Ctrl-C again to quit"))
			continue
		}
		if err != nil {
			ui.Goodbye()
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if slash(a, line) {
				ui.Goodbye()
				return
			}
			continue
		}
		ctx, c := context.WithCancel(context.Background())
		mu.Lock()
		cancel = c
		mu.Unlock()
		_, err = a.Ask(ctx, line)
		mu.Lock()
		cancel = nil
		mu.Unlock()
		c()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				ui.Println("\n  " + ui.Weak("Stopped."))
			} else {
				ui.Println("\n  " + ui.Ink("■ ") + ui.Sub(err.Error()))
			}
		}
	}
}

func slash(a *agent.Agent, line string) bool {
	f := strings.Fields(line)
	switch f[0] {
	case "/exit", "/quit", "/q":
		return true
	case "/clear", "/new":
		a.Reset()
		ui.Step("New conversation.")
	case "/yolo":
		a.Env.Yolo = !a.Env.Yolo
		ui.Step(fmt.Sprintf("Auto-approve is %s (deletes still ask).", map[bool]string{true: "on", false: "off"}[a.Env.Yolo]))
	case "/pc":
		setup.ShowPC(*a.Env.Info)
	case "/doctor":
		setup.Doctor(a.Cfg, *a.Env.Info, version)
	case "/setup":
		setup.Run(a.Cfg, a.Env.Info)
	case "/jobs":
		ui.Println(tools.Run(a.Env, "job_log", "{}"))
	case "/schedule":
		ui.Println(sched.ListText())
	case "/model":
		if len(f) > 1 {
			prov, m, _ := strings.Cut(f[1], "/")
			a.Cfg.Chain = append([]config.Entry{{Provider: prov, Model: m}}, a.Cfg.Chain...)
			ui.Step("Using " + f[1] + " first for this session.")
		} else {
			var rows []string
			for _, e := range a.Cfg.Chain {
				rows = append(rows, ui.Ink(e.Provider)+ui.Weak(" "+e.Model))
			}
			ui.Numbered(rows)
			ui.Println("  " + ui.Weak("/model provider/model to try one first"))
		}
	default:
		ui.Numbered([]string{
			ui.Accent("/clear") + ui.Sub("    new conversation"),
			ui.Accent("/model") + ui.Sub("    show or switch models"),
			ui.Accent("/yolo") + ui.Sub("     toggle auto-approve"),
			ui.Accent("/jobs") + ui.Sub("     background jobs"),
			ui.Accent("/schedule") + ui.Sub(" scheduled jobs"),
			ui.Accent("/pc") + ui.Sub("       what Blaze knows about this PC"),
			ui.Accent("/doctor") + ui.Sub("   check everything"),
			ui.Accent("/setup") + ui.Sub("    add models or tools"),
			ui.Accent("/exit"),
		})
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

func shortPath(p string) string {
	h, _ := os.UserHomeDir()
	if h != "" && strings.HasPrefix(p, h) {
		return "~" + p[len(h):]
	}
	return p
}
