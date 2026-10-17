package tools

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/sched"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
)

var httpc = &http.Client{Timeout: 40 * time.Second}

const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36 BlazeCLI"

var (
	reScript = regexp.MustCompile(`(?is)<(script|style|noscript|svg|head)[^>]*>.*?</(script|style|noscript|svg|head)>`)
	reBlock  = regexp.MustCompile(`(?i)</?(p|div|br|li|h[1-6]|tr|section|article|header|footer|pre)[^>]*>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpace  = regexp.MustCompile(`[ \t\r\f\v]+`)
	reLines  = regexp.MustCompile(`\n\s*\n+`)
	reTitle  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

func htmlText(s string) string {
	t := ""
	if m := reTitle.FindStringSubmatch(s); m != nil {
		t = strings.TrimSpace(html.UnescapeString(m[1])) + "\n\n"
	}
	s = reScript.ReplaceAllString(s, " ")
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = reSpace.ReplaceAllString(s, " ")
	s = reLines.ReplaceAllString(s, "\n\n")
	return t + strings.TrimSpace(s)
}

func get(u string) (int, string, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := httpc.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
	return resp.StatusCode, string(b), nil
}

var reDDG = regexp.MustCompile(`(?is)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>.*?<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)

func init() {
	register("web_search", "Search the web (free, no key). Returns titles, links and snippets. Then fetch_page the best links.",
		[]prop{p("query", "string", "Search words"), p("count", "integer", "Results, default 8")}, []string{"query"},
		func(e *Env, a args) string {
			e.status("Searching: " + a.s("query"))
			code, body, err := get("https://html.duckduckgo.com/html/?q=" + url.QueryEscape(a.s("query")))
			if err != nil {
				return "Error: search failed: " + err.Error() + "\n[hint: no internet?]"
			}
			if code >= 300 {
				return fmt.Sprintf("Error: search returned HTTP %d. Try again with different words.", code)
			}
			n := a.i("count", 8)
			var b strings.Builder
			for i, m := range reDDG.FindAllStringSubmatch(body, n) {
				link := html.UnescapeString(m[1])
				if u, err := url.Parse(link); err == nil && u.Query().Get("uddg") != "" {
					link = u.Query().Get("uddg")
				}
				fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, htmlText(m[2]), link, htmlText(m[3]))
			}
			if b.Len() == 0 {
				return "No results."
			}
			return b.String()
		})

	register("fetch_page", "Read a web page as plain text. Not for JSON APIs or POST (http_request).",
		[]prop{p("url", "string", "Page URL"), p("max_chars", "integer", "Default 12000")}, []string{"url"},
		func(e *Env, a args) string {
			u := a.s("url")
			if !strings.HasPrefix(u, "http") {
				u = "https://" + u
			}
			e.status("Reading " + u)
			code, body, err := get(u)
			if err != nil {
				return "Error: " + err.Error()
			}
			t := body
			if strings.Contains(strings.ToLower(body[:min(len(body), 2000)]), "<html") || strings.Contains(body[:min(len(body), 500)], "<!") {
				t = htmlText(body)
			}
			mx := a.i("max_chars", 12000)
			if len(t) > mx {
				t = t[:mx] + "\n…[cut]"
			}
			return fmt.Sprintf("HTTP %d · %s\n\n%s", code, u, t)
		})

	register("http_request", "Call an HTTP API: any method, headers, body. Returns status, headers and body. For APIs and webhooks, not for reading articles (fetch_page).",
		[]prop{p("url", "string", "URL"), p("method", "string", "GET, POST, PUT, PATCH, DELETE (default GET)"), p("headers", "object", "Header map"), p("body", "string", "Request body (JSON as a string)")},
		[]string{"url"}, func(e *Env, a args) string {
			m := strings.ToUpper(a.s("method"))
			if m == "" {
				m = "GET"
			}
			req, err := http.NewRequest(m, a.s("url"), strings.NewReader(a.s("body")))
			if err != nil {
				return "Error: " + err.Error()
			}
			req.Header.Set("User-Agent", ua)
			if h, ok := a["headers"].(map[string]any); ok {
				for k, v := range h {
					req.Header.Set(k, fmt.Sprint(v))
				}
			}
			if a.s("body") != "" && req.Header.Get("Content-Type") == "" {
				req.Header.Set("Content-Type", "application/json")
			}
			e.status(m + " " + a.s("url"))
			resp, err := httpc.Do(req)
			if err != nil {
				return "Error: " + err.Error()
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			var hb strings.Builder
			for _, k := range []string{"Content-Type", "Location", "Retry-After", "X-RateLimit-Remaining"} {
				if v := resp.Header.Get(k); v != "" {
					fmt.Fprintf(&hb, "%s: %s\n", k, v)
				}
			}
			s := string(b)
			if len(s) > 15000 {
				s = s[:15000] + "\n…[cut]"
			}
			return fmt.Sprintf("HTTP %d\n%s\n%s", resp.StatusCode, hb.String(), s)
		})

	register("download_file", "Download a file to disk (default ~/Downloads).",
		[]prop{p("url", "string", "URL"), p("path", "string", "Where to save; folder or file path")}, []string{"url"},
		func(e *Env, a args) string {
			h, _ := os.UserHomeDir()
			dst := a.s("path")
			if dst == "" {
				dst = filepath.Join(h, "Downloads")
			}
			dst = expand(e, dst)
			if st, err := os.Stat(dst); err == nil && st.IsDir() {
				name := filepath.Base(strings.Split(a.s("url"), "?")[0])
				if name == "" || name == "/" || name == "." {
					name = "download"
				}
				dst = filepath.Join(dst, name)
			}
			e.status("Downloading " + a.s("url"))
			req, _ := http.NewRequest("GET", a.s("url"), nil)
			req.Header.Set("User-Agent", ua)
			resp, err := (&http.Client{Timeout: 30 * time.Minute}).Do(req)
			if err != nil {
				return "Error: " + err.Error()
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				return fmt.Sprintf("Error: HTTP %d", resp.StatusCode)
			}
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			f, err := os.Create(dst)
			if err != nil {
				return "Error: " + err.Error()
			}
			defer f.Close()
			n, err := io.Copy(f, resp.Body)
			if err != nil {
				return "Error: " + err.Error()
			}
			return fmt.Sprintf("Saved %s (%s).", dst, human(n))
		})

	register("open_url", "Open a URL, file or folder on the user's screen with the default app (browser, editor, file manager).",
		[]prop{p("target", "string", "URL or path")}, []string{"target"},
		func(e *Env, a args) string {
			t := a.s("target")
			if !strings.Contains(t, "://") {
				t = expand(e, t)
			}
			var c *exec.Cmd
			switch runtime.GOOS {
			case "windows":
				c = exec.Command("rundll32", "url.dll,FileProtocolHandler", t)
			case "darwin":
				c = exec.Command("open", t)
			default:
				c = exec.Command("xdg-open", t)
			}
			if err := c.Start(); err != nil {
				return "Error: couldn't open it: " + err.Error()
			}
			return "Opened " + t
		})

	register("run_code", "Run a Python, Node.js, shell or PowerShell snippet and return its output. Python packages go in a private venv (~/.blaze/venv); Node packages in ~/.blaze/node. Use for calculations, data work, scripts.",
		[]prop{p("language", "string", "python, node, shell or powershell"), p("code", "string", "The code"), p("packages", "array", "Packages to install first, e.g. [\"requests\"]"), p("timeout_s", "integer", "Default 120")},
		[]string{"language", "code"}, func(e *Env, a args) string { return runCode(e, a) })

	register("git", "Run git in the current folder (or path). Examples: status, diff, log --oneline -15, add -A, commit -m \"msg\", push, pull, clone URL, checkout -b name. Runs git directly, not through a shell: one git command per call, no &&, pipes or redirects. Uses the user's own git credentials. Force-push, reset --hard and clean ask first.",
		[]prop{p("args", "string", "Everything after 'git'"), p("path", "string", "Repository folder, default current")}, []string{"args"},
		func(e *Env, a args) string { return runGit(e, a) })

	register("install_tool", "Install a program (git, python, node, go, docker, ollama, or any package name) with this PC's package manager. Always asks the user first.",
		[]prop{p("name", "string", "Tool or package name")}, []string{"name"},
		func(e *Env, a args) string {
			name := strings.ToLower(strings.TrimSpace(a.s("name")))
			cmd := e.Info.InstallCommand(name)
			if cmd == "" && len(e.Info.PkgManagers) > 0 {
				cmd = (&sysinfo.Info{PkgManagers: e.Info.PkgManagers}).InstallCommand("git")
				cmd = strings.Replace(cmd, "Git.Git", name, 1)
				cmd = strings.Replace(cmd, " git", " "+name, 1)
			}
			if cmd == "" {
				return "No package manager found on this PC. " + installHint(e, name)
			}
			if !e.Confirm("Install "+name+"?", cmd) {
				return "The user did NOT approve installing " + name + "."
			}
			e.status("Installing " + name)
			r := e.Shell.Run(cmd, 20*time.Minute)
			fresh := sysinfo.Detect()
			*e.Info = fresh
			return r + "\n(PC profile refreshed.)"
		})

	register("system_info", "Fresh details about this PC: OS, CPU, RAM, GPU, disk, shell, package managers, installed tools. Already in your prompt; call only to refresh after installs.",
		nil, nil, func(e *Env, a args) string {
			fresh := sysinfo.Detect()
			*e.Info = fresh
			return fresh.Summary()
		})

	register("schedule", "Schedule a shell command or a Blaze prompt to run by itself (Windows Task Scheduler, cron on Linux/macOS). action=add|list|remove. when: 'daily 07:00', 'hourly', 'every 30m', 'weekly mon 09:00', 'boot' or a cron line.",
		[]prop{p("action", "string", "add, list or remove"), p("name", "string", "Short unique name"), p("when", "string", "Schedule"), p("command", "string", "Shell command to run"), p("prompt", "string", "Or: a prompt for Blaze to run")},
		[]string{"action"}, func(e *Env, a args) string {
			switch a.s("action") {
			case "list":
				return sched.ListText()
			case "remove":
				if !e.ask("Remove scheduled job?", a.s("name")) {
					return "Not removed."
				}
				return sched.Remove(a.s("name"))
			default:
				j := sched.Job{Name: a.s("name"), When: a.s("when"), Command: a.s("command"), Prompt: a.s("prompt"), Dir: e.Shell.Cwd()}
				if why := blocked(j.Command); why != "" {
					return refusal(why)
				}
				if !e.ask("Schedule this?", j.Name+" · "+j.When+" · "+nonEmpty(j.Command, "blaze: "+j.Prompt)) {
					return "Not scheduled."
				}
				return sched.Add(j)
			}
		})
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func installHint(e *Env, tool string) string {
	if c := e.Info.InstallCommand(tool); c != "" {
		return "Install it with install_tool name=" + tool + " (runs: " + c + ")."
	}
	return "Ask the user to install " + tool + " from its website."
}

func runCode(e *Env, a args) string {
	lang := strings.ToLower(a.s("language"))
	code := a.s("code")
	// The snippet goes through the same blocklist and approval as run_shell.
	if why := e.check("Run risky code?", code, risky(code), "The user did NOT approve this code. Don't run it; suggest a safer way or ask them."); why != "" {
		return why
	}
	to := time.Duration(a.i("timeout_s", 120)) * time.Second
	tmp := dataDir("tmp")
	var pkgs []string
	if arr, ok := a["packages"].([]any); ok {
		for _, x := range arr {
			pkgs = append(pkgs, fmt.Sprint(x))
		}
	}
	q := func(s string) string {
		if e.Shell.IsPowerShell() {
			return "'" + s + "'"
		}
		return "\"" + s + "\""
	}
	switch lang {
	case "python", "py", "python3":
		py := ""
		for _, c := range []string{"python3", "python"} {
			if _, ok := e.Info.Tools[c]; ok {
				py = c
				break
			}
		}
		if py == "" {
			return "Python isn't installed. " + installHint(e, "python")
		}
		venv := filepath.Join(dataDir(""), "venv")
		vpy := filepath.Join(venv, "bin", "python")
		if isWindows() {
			vpy = filepath.Join(venv, "Scripts", "python.exe")
		}
		if _, err := os.Stat(vpy); err != nil && len(pkgs) > 0 {
			e.status("Creating a Python venv")
			if r := e.Shell.Run(py+" -m venv "+q(venv), 5*time.Minute); !strings.HasPrefix(r, "exit 0") {
				return "Couldn't create the venv:\n" + r
			}
		}
		if _, err := os.Stat(vpy); err == nil {
			py = q(vpy)
			if isWindows() && e.Shell.IsPowerShell() {
				py = "& " + py
			}
		}
		if len(pkgs) > 0 {
			e.status("pip install " + strings.Join(pkgs, " "))
			if r := e.Shell.Run(py+" -m pip install -q "+strings.Join(pkgs, " "), 10*time.Minute); !strings.HasPrefix(r, "exit 0") {
				return "Package install failed:\n" + r
			}
		}
		f := filepath.Join(tmp, "snippet.py")
		_ = os.WriteFile(f, []byte(code), 0o644)
		e.status("Running Python")
		return e.Shell.Run(py+" "+q(f), to)
	case "node", "javascript", "js":
		if _, ok := e.Info.Tools["node"]; !ok {
			return "Node.js isn't installed. " + installHint(e, "node")
		}
		nd := dataDir("node")
		if len(pkgs) > 0 {
			e.status("npm install " + strings.Join(pkgs, " "))
			if r := e.Shell.Run("npm install --prefix "+q(nd)+" --silent "+strings.Join(pkgs, " "), 10*time.Minute); !strings.HasPrefix(r, "exit 0") {
				return "Package install failed:\n" + r
			}
		}
		f := filepath.Join(nd, "snippet.cjs")
		_ = os.WriteFile(f, []byte(code), 0o644)
		e.status("Running Node")
		return e.Shell.Run("node "+q(f), to)
	case "powershell", "ps", "pwsh":
		f := filepath.Join(tmp, "snippet.ps1")
		_ = os.WriteFile(f, []byte(code), 0o644)
		exe := "powershell"
		if _, err := exec.LookPath("pwsh"); err == nil {
			exe = "pwsh"
		}
		return e.Shell.Run(exe+" -NoProfile -ExecutionPolicy Bypass -File "+q(f), to)
	default:
		if isWindows() && !strings.Contains(e.Shell.Kind(), "sh") {
			return "On this PC use language=powershell for shell scripts."
		}
		f := filepath.Join(tmp, "snippet.sh")
		_ = os.WriteFile(f, []byte(code), 0o755)
		return e.Shell.Run("sh "+q(f), to)
	}
}
