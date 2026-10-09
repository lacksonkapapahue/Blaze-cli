// Package setup is the first-run wizard and `blaze doctor`.
package setup

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/sched"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

func ShowPC(in sysinfo.Info) {
	gpu := strings.Join(in.GPU, ", ")
	if gpu == "" {
		gpu = "none detected"
	}
	var toolsList []string
	for k := range in.Tools {
		toolsList = append(toolsList, k)
	}
	pm := strings.Join(in.PkgManagers, ", ")
	if pm == "" {
		pm = "none"
	}
	ui.Box(ui.AccentLabel("your pc"), []string{
		ui.Field("system", 13) + ui.Ink(in.OSName) + ui.Weak(" "+in.Arch),
		ui.Field("cpu", 13) + ui.Ink(trunc(in.CPU, 48)) + ui.Weak(fmt.Sprintf(" %d threads", in.Cores)),
		ui.Field("memory", 13) + ui.Ink(fmt.Sprintf("%.0f GB RAM", in.RAMGB)) + ui.Weak(fmt.Sprintf(" · %.0f GB free disk", in.DiskFreeGB)),
		ui.Field("gpu", 13) + ui.Ink(trunc(gpu, 56)),
		ui.Field("shell", 13) + ui.Ink(in.Shell) + ui.Weak(" · packages via "+pm),
		ui.Field("tools", 13) + ui.Ink(trunc(strings.Join(sortStrings(toolsList), " "), 56)),
	})
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

func sortStrings(s []string) []string {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s
}

func openBrowser(u string) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	case "darwin":
		_ = exec.Command("open", u).Start()
	default:
		_ = exec.Command("xdg-open", u).Start()
	}
}

func yes(q string, def bool) bool {
	d := 1
	if def {
		d = 0
	}
	return ui.Choose("  "+ui.Ink(q)+" ", []string{"Yes", "No"}, d, 1) == 0
}

// FromEnv builds a chain silently from API keys already in the environment.
func FromEnv(cfg *config.Config) bool {
	var chain []config.Entry
	for _, p := range config.Providers {
		if p.Env != "" && p.Free && cfg.Key(p.ID) != "" {
			chain = append(chain, config.Entry{Provider: p.ID})
		}
	}
	if len(chain) == 0 {
		return false
	}
	cfg.Chain = chain
	cfg.SetupDone = true
	_ = cfg.Save()
	return true
}

// Run is the interactive wizard.
func Run(cfg *config.Config, in *sysinfo.Info) {
	ui.BannerAnimated("setup", "Let's get Blaze running on this PC. About a minute.")
	ui.Println("  " + ui.Label("01 · detecting your pc"))
	ShowPC(*in)

	// Missing developer tools.
	if miss := in.Missing(); len(miss) > 0 {
		ui.Println("\n  " + ui.Label("02 · tools"))
		for _, t := range miss {
			cmd := in.InstallCommand(t)
			if cmd == "" {
				ui.Step(t + " isn't installed and there's no package manager to install it. Blaze will tell you when it's needed.")
				continue
			}
			if yes(fmt.Sprintf("%s isn't installed. Install it now (%s)?", t, cmd), t == "git") {
				sh := tools.NewShell(in.Shell, in.ShellPath, in.Home)
				ui.Step("Installing " + t + "…")
				r := sh.Run(cmd, 20*time.Minute)
				if strings.HasPrefix(r, "exit 0") {
					ui.Step(ui.Accent("■ ") + t + " installed")
				} else {
					ui.Step("Couldn't install " + t + ": " + strings.SplitN(r, "\n", 3)[min(1, len(strings.SplitN(r, "\n", 3))-1)])
				}
			}
		}
		*in = sysinfo.Detect()
	}

	// Models.
	ui.Println("\n  " + ui.Label("03 · ai model"))
	ui.Println("  " + ui.Sub("Blaze runs on free model tiers. Add one or more; it switches between them when one is busy."))
	var free []config.Provider
	for _, p := range config.Providers {
		if p.Free && !config.Keyless(p.ID) {
			free = append(free, p)
		}
	}
	var rows []string
	for _, p := range free {
		have := ""
		if cfg.Key(p.ID) != "" {
			have = ui.Accent("  key saved")
		}
		rows = append(rows, ui.Ink(p.Name)+ui.Weak("  "+p.KeyPage)+have)
	}
	ollamaOK := false
	if _, ok := in.Tools["ollama"]; ok {
		ollamaOK = true
		rows = append(rows, ui.Ink("Ollama (local, offline)")+ui.Weak("  runs on your own "+gpuOrCPU(*in)))
	}
	ui.Println("  " + ui.Sub("Tick the ones you want. Gemini is a good start."))
	ticked := make([]bool, len(rows))
	ticked[0] = true
	var chain []config.Entry
	for _, i := range ui.Checklist(rows, ticked) {
		if i == len(free) && ollamaOK {
			if e, ok := setupOllama(); ok {
				chain = append(chain, e)
			}
			continue
		}
		if i >= len(free) {
			continue
		}
		if e, ok := setupProvider(cfg, free[i]); ok {
			chain = append(chain, e)
		}
	}
	if len(chain) == 0 {
		ui.Println("\n  " + ui.Ink("No key added, so Blaze will use the keyless model (slower, smaller).") + ui.Sub(" Run ") + ui.Accent("blaze setup") + ui.Sub(" any time to add a free key."))
		chain = []config.Entry{{Provider: "pollinations", Model: "openai-fast"}}
	}
	cfg.Chain = chain
	cfg.SetupDone = true
	if err := cfg.Save(); err != nil {
		ui.Println("  Couldn't save settings: " + err.Error())
		return
	}
	ui.Println("")
	ui.Box(ui.AccentLabel("ready"), []string{
		ui.Ink("Blaze is set up."),
		"",
		ui.Accent("blaze") + ui.Sub("                 chat in this folder"),
		ui.Accent(`blaze "question"`) + ui.Sub("      one answer and exit"),
		ui.Accent("blaze doctor") + ui.Sub("          check everything"),
		ui.Accent("blaze schedule list") + ui.Sub("   scheduled jobs"),
	})
}

func gpuOrCPU(in sysinfo.Info) string {
	if len(in.GPU) > 0 {
		return trunc(in.GPU[0], 30)
	}
	return "CPU"
}

func setupProvider(cfg *config.Config, p config.Provider) (config.Entry, bool) {
	ui.Println("\n  " + ui.Accent("■ ") + ui.Bold(p.Name))
	key := cfg.Key(p.ID)
	if key == "" {
		if yes("Open the page to get a free key?", true) {
			openBrowser(p.KeyPage)
		}
		key = ui.Secret("  " + ui.Ink("Paste the key ") + ui.Weak("("+p.KeyHint+", hidden) "))
		if key == "" {
			ui.Step("Skipped " + p.Name)
			return config.Entry{}, false
		}
	}
	ui.Step("Testing the key…")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	ids, err := llm.Models(ctx, p, key)
	if err != nil {
		ui.Step(ui.Ink("That key didn't work: ") + ui.Sub(trunc(err.Error(), 120)))
		return config.Entry{}, false
	}
	model := p.DefaultModel
	if model == "" || (len(ids) > 0 && !contains(ids, model)) {
		model = llm.PickModel(ids)
	}
	where, err := cfg.SetKey(p.ID, key)
	if err != nil {
		ui.Step("Couldn't save the key: " + err.Error())
		return config.Entry{}, false
	}
	ui.Step(ui.Accent("■ ") + fmt.Sprintf("Works · using %s · key saved in %s", model, where))
	return config.Entry{Provider: p.ID, Model: model}, true
}

func setupOllama() (config.Entry, bool) {
	p, _ := config.ProviderByID("ollama")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ids, err := llm.Models(ctx, p, "")
	if err != nil {
		ui.Step("Ollama isn't running. Start it (ollama serve) and run blaze setup again.")
		return config.Entry{}, false
	}
	if len(ids) == 0 {
		ui.Step("Ollama has no models yet. Pull one (e.g. ollama pull qwen3) and run blaze setup again.")
		return config.Entry{}, false
	}
	m := llm.PickModel(ids)
	ui.Step(ui.Accent("■ ") + "Ollama · using " + m)
	return config.Entry{Provider: "ollama", Model: m}, true
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Doctor checks every part and says what to fix.
func Doctor(cfg *config.Config, in sysinfo.Info, version string) {
	ui.Banner(version, "Checking everything Blaze depends on.")
	type item struct {
		name, detail string
		ok           bool
	}
	var items []item
	add := func(n string, ok bool, d string) { items = append(items, item{n, d, ok}) }

	// Internet
	c := &http.Client{Timeout: 8 * time.Second}
	resp, err := c.Get("https://www.google.com/generate_204")
	net := err == nil && resp.StatusCode < 400
	if resp != nil {
		resp.Body.Close()
	}
	add("Internet", net, map[bool]string{true: "online", false: "can't reach the internet"}[net])

	// Models
	if len(cfg.Chain) == 0 {
		add("AI model", false, "no key set up, using the keyless model: run blaze setup")
	}
	for _, e := range cfg.RouteChain() {
		p, ok := config.ProviderByID(e.Provider)
		if !ok {
			continue
		}
		key := cfg.Key(p.ID)
		if key == "" && !config.Keyless(p.ID) {
			add(p.Name, false, "no key saved: run blaze setup")
			continue
		}
		model := e.Model
		if model == "" {
			model = p.DefaultModel
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t0 := time.Now()
		_, err := llm.Chat(ctx, p, key, model, []llm.Message{{Role: "user", Content: "Reply with OK."}}, nil, nil)
		cancel()
		if err != nil {
			add(p.Name, false, model+": "+trunc(err.Error(), 90))
		} else {
			add(p.Name, true, fmt.Sprintf("%s answered in %.1fs", model, time.Since(t0).Seconds()))
		}
	}

	// Shell
	sh := tools.NewShell(in.Shell, in.ShellPath, in.Home)
	r := sh.Run("echo blaze-ok", 20*time.Second)
	add("Shell", strings.Contains(r, "blaze-ok"), in.Shell+" ("+in.ShellPath+")")
	for _, t := range []string{"git", "python", "node"} {
		v, ok := in.Tools[t]
		if t == "python" && !ok {
			v, ok = in.Tools["python3"]
		}
		d := v
		if !ok {
			d = "not installed"
			if c := in.InstallCommand(t); c != "" {
				d += " · install: " + c
			}
		}
		add(strings.ToUpper(t[:1])+t[1:], ok, trunc(d, 70))
	}
	add("Disk", in.DiskFreeGB > 1, fmt.Sprintf("%.0f GB free", in.DiskFreeGB))
	add("Package manager", len(in.PkgManagers) > 0, nzs(strings.Join(in.PkgManagers, ", "), "none found: Blaze can't install tools for you"))
	if runtime.GOOS != "windows" {
		_, err := exec.LookPath("crontab")
		add("Scheduler", err == nil, map[bool]string{true: "cron available", false: "crontab missing: scheduled jobs won't run"}[err == nil])
	} else {
		add("Scheduler", true, "Task Scheduler")
	}
	n := len(sched.Load())
	add("Scheduled jobs", true, fmt.Sprintf("%d", n))
	add("Config", true, config.Dir())

	var lines []string
	pass := 0
	for _, it := range items {
		if it.ok {
			pass++
		}
		lines = append(lines, ui.Dot(it.ok)+"  "+ui.Ink(fmt.Sprintf("%-17s", it.name))+ui.Sub(it.detail))
	}
	ui.Box(ui.AccentLabel(fmt.Sprintf("%d of %d passed", pass, len(items))), lines)
}

func nzs(a, b string) string {
	if a == "" {
		return b
	}
	return a
}
