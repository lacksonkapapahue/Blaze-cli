// Package tools is everything Blaze can do on the PC: shell, files, code,
// git, web, background jobs, package installs, memory, skills and schedules.
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
)

// Env is what tools need from the host: the PC profile and a way to ask the user.
type Env struct {
	Info    *sysinfo.Info
	Root    string // the folder Blaze was started in (the "project")
	Yolo    bool
	Confirm func(title, detail string) bool
	Status  func(line string) // one-line progress ("Running: npm test")
	Shell   *Shell
	Jobs    *Jobs
	Plan    bool                             // plan mode: look, don't touch
	OnDiff  func(path, before, after string) // show a file change
	OnTodo  func(items []Todo)               // show the task checklist
}

// changes lists the tools that alter the PC; plan mode blocks them.
var changes = map[string]bool{
	"run_shell": true, "file_write": true, "file_edit": true, "file_delete": true,
	"run_code": true, "install_tool": true, "schedule": true, "download_file": true,
	"job_input": true, "job_stop": true, "http_request": true, "git": true,
}

type handler func(e *Env, a args) string

type args map[string]any

func (a args) s(k string) string {
	switch v := a[k].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func (a args) i(k string, def int) int {
	switch v := a[k].(type) {
	case float64:
		return int(v)
	case string:
		var n int
		if _, err := fmt.Sscan(v, &n); err == nil {
			return n
		}
	}
	return def
}

func (a args) b(k string) bool {
	switch v := a[k].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "yes" || v == "1"
	}
	return false
}

type def struct {
	tool llm.Tool
	run  handler
}

var registry = map[string]def{}

type prop struct{ name, typ, desc string }

func p(name, typ, desc string) prop { return prop{name, typ, desc} }

func register(name, desc string, props []prop, required []string, h handler) {
	t := llm.Tool{Type: "function"}
	t.Function.Name = name
	t.Function.Description = desc
	pm := map[string]any{}
	for _, pr := range props {
		pm[pr.name] = map[string]any{"type": pr.typ, "description": pr.desc}
	}
	if required == nil {
		required = []string{}
	}
	t.Function.Parameters = map[string]any{"type": "object", "properties": pm, "required": required}
	registry[name] = def{t, h}
}

// All returns every tool definition, sorted by name.
func All() []llm.Tool {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]llm.Tool, 0, len(names))
	for _, n := range names {
		out = append(out, registry[n].tool)
	}
	return out
}

// Run executes a tool call. It never panics: crashes and empty results become plain errors with a fix.
func Run(e *Env, name, rawArgs string) (out string) {
	d, ok := registry[name]
	if !ok {
		return "Error: there is no tool called " + name + ". Use one of the listed tools."
	}
	a := args{}
	if strings.TrimSpace(rawArgs) != "" {
		if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return "Error in " + name + ": the arguments weren't valid JSON (" + err.Error() + "). Call it again with a JSON object."
		}
	}
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("Error in %s: %v\nFix: check the arguments and try again, or take a different approach.", name, r)
		}
	}()
	if e.Plan && changes[name] && !(name == "git" && readOnlyGit(a.s("args"))) {
		return "Plan mode is on, so nothing can be changed yet. Keep investigating with file_read, file_list, file_search, web_search, then finish with a numbered plan. The user switches modes to let you act."
	}
	out = d.run(e, a)
	if strings.TrimSpace(out) == "" {
		out = name + " finished with no output."
	}
	if len(out) > 30000 {
		out = out[:14000] + "\n…[" + fmt.Sprint(len(out)-28000) + " chars cut]…\n" + out[len(out)-14000:]
	}
	return out
}

// ---------- paths ----------

// ExpandPath resolves a tool's path argument the way the file tools do (relative to
// the current folder, ~ allowed).
func ExpandPath(e *Env, path string) string { return expand(e, path) }

func expand(e *Env, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return e.Shell.Cwd()
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		h, _ := os.UserHomeDir()
		path = filepath.Join(h, path[1:])
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.Shell.Cwd(), path)
	}
	return filepath.Clean(path)
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func (e *Env) ask(title, detail string) bool {
	if e.Yolo {
		return true
	}
	return e.Confirm(title, detail)
}

func (e *Env) status(s string) {
	if e.Status != nil {
		e.Status(s)
	}
}

func dataDir(sub string) string {
	d := filepath.Join(config.Dir(), sub)
	_ = os.MkdirAll(d, 0o700)
	return d
}

func isWindows() bool { return runtime.GOOS == "windows" }

func readOnlyGit(args string) bool {
	f := strings.Fields(args)
	if len(f) == 0 {
		return false
	}
	switch f[0] {
	case "status", "log", "diff", "show", "branch", "remote", "blame", "ls-files", "rev-parse":
		return len(f) == 1 || f[0] != "branch" || !strings.HasPrefix(f[1], "-d")
	}
	return false
}

func (e *Env) diff(path, before, after string) {
	if e.OnDiff != nil && before != after {
		e.OnDiff(rel(e, path), before, after)
	}
}
