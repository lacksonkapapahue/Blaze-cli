// Package steps says what the agent is doing in plain words (v0.7.0), the same
// phrases Blaze Desktop's chat shows (frontend steps.js): "Reading main.go",
// "Running the tests", "Searching the web for "…"". A tool call becomes a short
// phrase with no commands, flags, paths, JSON or code. It is a small deterministic
// table, no model calls. Detail is the exact call, shown only when the user expands
// a finished turn (Ctrl+O).
package steps

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Max is the longest phrase.
const Max = 60

func clip(t string, n int) string {
	t = strings.Join(strings.Fields(t), " ")
	if utf8.RuneCountInString(t) > n {
		r := []rune(t)
		return string(r[:n-1]) + "…"
	}
	return t
}

func unq(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

var flagRe = regexp.MustCompile(`^/[A-Za-z?]$`)

func isFlag(s string) bool { return strings.HasPrefix(s, "-") || flagRe.MatchString(s) }

// place is a path as a person would say it: its last part, or "your home folder".
func place(p, kind string) string {
	p = strings.TrimRight(unq(strings.TrimSpace(p)), `/\`)
	switch {
	case p == "" || p == ".":
		if kind == "folder" {
			return "the project folder"
		}
		return ""
	case p == "~" || p == "$HOME" || p == "%USERPROFILE%":
		return "your home folder"
	case p == "..":
		return "the parent folder"
	}
	if strings.ContainsAny(p, "*?") {
		if m := regexp.MustCompile(`\*\.([A-Za-z0-9]+)$`).FindStringSubmatch(p); m != nil {
			return "the ." + m[1] + " files"
		}
		return "matching files"
	}
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	b := p
	if len(parts) > 0 {
		b = parts[len(parts)-1]
	}
	if b == "" {
		return "the whole disk"
	}
	return clip(b, 40)
}

func host(u string) string {
	if u == "" {
		return ""
	}
	if !regexp.MustCompile(`(?i)^[a-z]+://`).MatchString(u) {
		u = "https://" + u
	}
	x, err := url.Parse(u)
	if err != nil || x.Host == "" {
		return ""
	}
	return strings.TrimPrefix(x.Host, "www.")
}

func words(s string) string {
	return strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(s))
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// ---------- shell ----------

// tokens splits a command line into words, keeping quoted strings together.
func tokens(s string) []string {
	var out []string
	var cur strings.Builder
	q, has := rune(0), false
	for _, c := range s {
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			q, has = c, true
		case unicode.IsSpace(c):
			if cur.Len() > 0 || has {
				out = append(out, cur.String())
			}
			cur.Reset()
			has = false
		default:
			cur.WriteRune(c)
		}
	}
	if cur.Len() > 0 || has {
		out = append(out, cur.String())
	}
	return out
}

type seg struct {
	text  string
	piped bool
}

// segments splits on ; && || | & and newlines outside quotes.
func segments(cmd string) []seg {
	var out []seg
	var cur strings.Builder
	q, piped := byte(0), false
	push := func(next bool) {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, seg{t, piped})
		}
		cur.Reset()
		piped = next
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		var n, p byte
		if i+1 < len(cmd) {
			n = cmd[i+1]
		}
		if i > 0 {
			p = cmd[i-1]
		}
		switch {
		case q != 0:
			cur.WriteByte(c)
			if c == q {
				q = 0
			}
		case c == '\'' || c == '"':
			q = c
			cur.WriteByte(c)
		case (c == '&' && n == '&') || (c == '|' && n == '|'):
			push(false)
			i++
		case c == '|':
			push(true)
		case c == ';' || c == '\n':
			push(false)
		case c == '&' && n != '>' && p != '>':
			push(false)
		default:
			cur.WriteByte(c)
		}
	}
	push(false)
	return out
}

var (
	installSub = map[string]*regexp.Regexp{
		"npm": regexp.MustCompile(`^(i|install|ci|add)$`), "pnpm": regexp.MustCompile(`^(i|install|add)$`), "yarn": regexp.MustCompile(`^(install|add)?$`),
		"bun": regexp.MustCompile(`^(i|install|add)$`), "pip": regexp.MustCompile(`^install$`), "uv": regexp.MustCompile(`^(add|sync|pip)$`),
		"poetry": regexp.MustCompile(`^(install|add)$`), "cargo": regexp.MustCompile(`^(add|fetch)$`), "go": regexp.MustCompile(`^(get|mod)$`),
		"composer": regexp.MustCompile(`^(install|require)$`), "bundle": regexp.MustCompile(`^(install)?$`), "gem": regexp.MustCompile(`^install$`),
		"pipenv": regexp.MustCompile(`^install$`), "conda": regexp.MustCompile(`^install$`), "deno": regexp.MustCompile(`^(add|install)$`),
	}
	sysPkg = map[string]*regexp.Regexp{
		"apt": regexp.MustCompile(`^install$`), "apt-get": regexp.MustCompile(`^install$`), "dnf": regexp.MustCompile(`^install$`), "yum": regexp.MustCompile(`^install$`),
		"pacman": regexp.MustCompile(`^-S`), "yay": regexp.MustCompile(`^-S`), "paru": regexp.MustCompile(`^-S`), "brew": regexp.MustCompile(`^install$`),
		"winget": regexp.MustCompile(`^install$`), "choco": regexp.MustCompile(`^install$`), "scoop": regexp.MustCompile(`^install$`), "snap": regexp.MustCompile(`^install$`),
		"flatpak": regexp.MustCompile(`^install$`), "zypper": regexp.MustCompile(`^(in|install)$`), "apk": regexp.MustCompile(`^add$`),
	}
	testers   = regexp.MustCompile(`^(pytest|jest|vitest|mocha|rspec|phpunit|tox|nox|ava|karma|playwright|cypress)$`)
	readers   = regexp.MustCompile(`^(cat|less|more|head|tail|bat|batcat|nl|type|get-content|gc|xxd|hexdump|strings|od)$`)
	listers   = regexp.MustCompile(`^(ls|ll|la|dir|tree|exa|eza|get-childitem|gci)$`)
	finders   = regexp.MustCompile(`^(find|fd|fdfind|locate)$`)
	searchers = regexp.MustCompile(`^(grep|egrep|fgrep|rg|ag|ack|findstr|select-string|sls|git-grep)$`)
	filters   = regexp.MustCompile(`^(grep|egrep|rg|head|tail|sort|uniq|wc|awk|sed|cut|tr|jq|less|more|column|xargs|tee|cat|findstr|select-string|sls|nl|fold|fmt)$`)
	quietCmd  = regexp.MustCompile(`^(echo|printf|true|false|:|sleep|clear|cls|pwd|whoami|set|export|source|\.|exit|read|wait|date|unset|alias|write-host|write-output)$`)
	wrappers  = regexp.MustCompile(`(?i)^(sudo|doas|time|nohup|env|command|exec|nice|caffeinate|stdbuf|timeout|npx|pnpx|bunx|uvx|call|start-process|&)$`)
	assignRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	redirRe   = regexp.MustCompile(`(?:^|[^0-9&>])>>?\s*("[^"]+"|'[^']+'|[^\s;&|<>]+)`)
	redirCut  = regexp.MustCompile(`\d?>>?\s*&?\s*("[^"]+"|'[^']+'|[^\s;&|<>]+)`)
	heredocIn = regexp.MustCompile(`<<-?\s*['"]?\w+['"]?`)
	inputCut  = regexp.MustCompile(`<\s*\S+`)
	numRe     = regexp.MustCompile(`^\d+$`)
)

type phrase struct {
	p    float64
	t, k string // phrase, kind
	x    string // target (reads / listings)
}

func r(p float64, t string, kx ...string) phrase {
	ph := phrase{p: p, t: t, k: "run"}
	if len(kx) > 0 {
		ph.k = kx[0]
	}
	if len(kx) > 1 {
		ph.x = kx[1]
	}
	return ph
}

func anyMatch(xs []string, re *regexp.Regexp) bool {
	for _, x := range xs {
		if re.MatchString(x) {
			return true
		}
	}
	return false
}

func at(xs []string, i int) string {
	if i >= 0 && i < len(xs) {
		return xs[i]
	}
	return ""
}

func in(s string, set ...string) bool {
	for _, x := range set {
		if s == x {
			return true
		}
	}
	return false
}

var gitPhrase = map[string]phrase{
	"status": r(3, "Checking git status"), "diff": r(3, "Checking what changed"), "log": r(3, "Checking git history"), "show": r(3, "Checking git history"),
	"blame": r(3, "Checking git history"), "shortlog": r(3, "Checking git history"), "reflog": r(3, "Checking git history"),
	"add": r(5, "Staging changes"), "commit": r(6, "Committing changes"), "push": r(6, "Pushing changes"), "pull": r(6, "Pulling the latest changes"),
	"fetch": r(5, "Fetching the latest changes"), "checkout": r(5, "Switching branches"), "switch": r(5, "Switching branches"), "branch": r(3, "Checking branches"),
	"merge": r(6, "Merging changes"), "rebase": r(6, "Rebasing changes"), "stash": r(5, "Stashing changes"), "reset": r(6, "Resetting changes"),
	"restore": r(6, "Restoring files"), "tag": r(4, "Tagging a release"), "remote": r(3, "Checking git remotes"), "init": r(6, "Setting up git"),
	"grep": r(4, "Searching the code"), "rev-parse": r(2, "Checking git status"), "ls-files": r(2, "Looking through tracked files"),
	"config": r(2, "Checking git settings"), "cherry-pick": r(6, "Applying a commit"), "rm": r(6, "Removing files from git"), "mv": r(6, "Moving files in git"),
}

// part is one piece of a command line as a phrase with a priority.
func part(s seg) phrase {
	text := s.text
	redirect := ""
	if m := redirRe.FindStringSubmatch(text); m != nil && unq(m[1]) != "/dev/null" && !regexp.MustCompile(`^&\d$`).MatchString(m[1]) {
		redirect = unq(m[1])
	}
	text = redirCut.ReplaceAllString(text, " ")
	text = heredocIn.ReplaceAllString(text, " ")
	text = inputCut.ReplaceAllString(text, " ")
	w := tokens(text)
	for len(w) > 0 && (assignRe.MatchString(w[0]) || wrappers.MatchString(w[0])) {
		if strings.EqualFold(w[0], "timeout") && len(w) > 1 && w[1] != "" && w[1][0] >= '0' && w[1][0] <= '9' {
			w = w[1:]
		}
		w = w[1:]
	}
	if len(w) == 0 {
		return r(0, "", "quiet")
	}
	cs := strings.FieldsFunc(w[0], func(r rune) bool { return r == '/' || r == '\\' })
	c := w[0]
	if len(cs) > 0 {
		c = cs[len(cs)-1]
	}
	c = regexp.MustCompile(`\.(exe|cmd|bat|ps1)$`).ReplaceAllString(strings.ToLower(c), "")
	a := w[1:]
	var pos []string
	for _, x := range a {
		if !isFlag(x) {
			pos = append(pos, x)
		}
	}
	sub := strings.ToLower(at(pos, 0))
	if c == "python3" || c == "py" {
		c = "python"
	}
	if c == "pip3" {
		c = "pip"
	}

	if redirect != "" && (quietCmd.MatchString(c) || in(c, "cat", "jq", "sed", "awk")) {
		return r(5, "Writing "+place(redirect, ""), "write", place(redirect, ""))
	}
	if quietCmd.MatchString(c) {
		return r(0, "", "quiet")
	}
	if in(c, "cd", "pushd", "popd", "set-location") {
		d := at(pos, 0)
		if d == "" {
			d = "~"
		}
		return r(0.5, "Opening "+place(d, "folder"), "cd")
	}

	// Installs.
	if re := sysPkg[c]; re != nil && anyMatch(a, re) {
		var pk []string
		for _, x := range pos {
			if !re.MatchString(x) && !regexp.MustCompile(`^(install|-S\w*)$`).MatchString(x) {
				pk = append(pk, x)
			}
		}
		if len(pk) == 1 {
			return r(9, "Installing "+place(pk[0], ""), "install")
		}
		return r(9, "Installing dependencies", "install")
	}
	if c == "python" && at(a, 0) == "-m" && at(a, 1) == "pip" && strings.Contains(at(a, 2), "install") {
		return r(9, "Installing dependencies", "install")
	}
	if re := installSub[c]; re != nil && (re.MatchString(sub) || (c == "yarn" && len(pos) == 0)) {
		if c == "go" && sub == "mod" && !in(at(pos, 1), "download", "tidy") {
			return r(3, "Checking Go modules")
		}
		if c == "go" && sub == "mod" && at(pos, 1) == "tidy" {
			return r(6, "Tidying Go modules")
		}
		return r(9, "Installing dependencies", "install")
	}
	if (c == "go" || c == "cargo") && sub == "install" {
		t := regexp.MustCompile(`@.*$`).ReplaceAllString(at(pos, 1), "")
		if t == "" {
			t = "a tool"
		}
		return r(9, "Installing "+place(t, ""), "install")
	}
	if c == "pipx" && sub == "install" {
		return r(9, "Installing "+orStr(place(at(pos, 1), ""), "a tool"), "install")
	}

	// Tests, builds, linters, scripts.
	pkgRun := in(c, "npm", "pnpm", "yarn", "bun")
	script := ""
	if pkgRun {
		script = sub
		if sub == "run" || sub == "run-script" {
			script = at(pos, 1)
		}
	}
	if pkgRun && regexp.MustCompile(`^(t|test|tests|test:.*|e2e|spec)$`).MatchString(script) {
		return r(8, "Running the tests", "test")
	}
	if in(c, "go", "cargo", "dotnet", "deno", "bun", "mix", "swift") && sub == "test" {
		return r(8, "Running the tests", "test")
	}
	if c == "python" && at(a, 0) == "-m" && in(at(a, 1), "pytest", "unittest", "tox", "nose2") {
		return r(8, "Running the tests", "test")
	}
	if testers.MatchString(c) || (c == "make" && in(sub, "test", "check", "tests")) || (in(c, "gradle", "gradlew", "mvn") && in(sub, "test", "check")) {
		return r(8, "Running the tests", "test")
	}
	if c == "docker" && (sub == "build" || (sub == "buildx" && at(pos, 1) == "build")) {
		return r(7, "Building a Docker image", "build")
	}
	if pkgRun && regexp.MustCompile(`^(build|build:.*|compile|dist|bundle|package)$`).MatchString(script) {
		return r(7, "Building the project", "build")
	}
	if (in(c, "go", "cargo", "dotnet", "wails", "swift", "zig") && sub == "build") || in(c, "tsc", "webpack", "esbuild", "rollup", "cmake", "ninja", "msbuild", "javac", "gcc", "g++", "clang", "rustc") ||
		(c == "make" && (sub == "" || in(sub, "all", "build", "release", "install"))) || (in(c, "vite", "next", "nuxt", "astro") && sub == "build") ||
		(in(c, "gradle", "gradlew", "mvn") && in(sub, "build", "assemble", "package", "compile", "install")) {
		return r(7, "Building the project", "build")
	}
	if in(c, "eslint", "ruff", "flake8", "pylint", "golangci-lint", "staticcheck", "shellcheck", "stylelint", "mypy", "biome") || (c == "go" && sub == "vet") || (c == "cargo" && sub == "clippy") ||
		(pkgRun && regexp.MustCompile(`^(lint|lint:.*|typecheck|check)$`).MatchString(script)) {
		return r(6, "Checking the code for problems", "lint")
	}
	if in(c, "prettier", "gofmt", "goimports", "black", "rustfmt", "isort", "clang-format") || (c == "go" && sub == "fmt") || (c == "cargo" && sub == "fmt") || (pkgRun && in(script, "format", "fmt")) {
		return r(6, "Formatting the code", "format")
	}
	if (pkgRun && in(script, "dev", "start", "serve", "preview", "watch")) || (in(c, "vite", "next", "nuxt", "astro") && (sub == "" || in(sub, "dev", "start", "preview"))) ||
		(c == "wails" && sub == "dev") || (c == "python" && at(a, 0) == "-m" && at(a, 1) == "http.server") || (c == "php" && at(a, 0) == "-S") || (c == "rails" && in(sub, "s", "server")) {
		return r(6, "Starting the dev server", "serve")
	}
	if pkgRun && script != "" && sub == "run" {
		return r(6, "Running the "+words(strings.SplitN(script, ":", 2)[0])+" script")
	}
	if pkgRun && in(sub, "outdated", "ls", "list", "audit", "why") {
		return r(3, "Checking the dependencies")
	}
	if c == "make" && sub != "" {
		return r(6, "Running the "+words(sub)+" task")
	}
	if c == "just" || c == "task" {
		if sub != "" {
			return r(6, "Running the "+words(sub)+" task")
		}
		return r(6, "Running a task")
	}

	// Version / presence checks.
	if len(a) == 1 && in(a[0], "--version", "-v", "-V", "version") {
		return r(2, "Checking the "+c+" version", "check")
	}
	if in(c, "which", "where", "whereis", "get-command", "gcm") && at(pos, 0) != "" {
		return r(2, "Checking "+place(pos[0], "")+" is installed", "check")
	}

	// Running programs and scripts.
	if c == "go" && sub == "run" {
		if p := at(pos, 1); p != "" && p != "." {
			return r(6, "Running "+place(p, ""))
		}
		return r(6, "Running the program")
	}
	if c == "cargo" && sub == "run" {
		return r(6, "Running the program")
	}
	if regexp.MustCompile(`^(python|node|deno|bun|ruby|perl|php|bash|sh|zsh|pwsh|powershell|lua|java|tsx|ts-node|osascript)$`).MatchString(c) {
		if anyMatch(a, regexp.MustCompile(`^(-c|-e|--eval|-command|-Command|-p)$`)) || len(pos) == 0 || (c == "deno" && sub == "eval") {
			return r(4, "Running a quick script")
		}
		f := at(pos, 0)
		if c == "deno" && sub == "run" {
			f = at(pos, 1)
		}
		return r(6, "Running "+orStr(place(f, ""), "a script"))
	}
	if regexp.MustCompile(`^\.{0,2}/`).MatchString(w[0]) && !strings.ContainsAny(w[0], " \t") {
		return r(6, "Running "+place(w[0], ""))
	}

	// Git.
	if c == "git" || c == "gh" {
		g := append([]string(nil), a...)
		for len(g) > 0 && (isFlag(g[0]) || in(g[0], "-C", "-c")) {
			if in(g[0], "-C", "-c") {
				g = g[1:]
			}
			if len(g) > 0 {
				g = g[1:]
			}
		}
		s := strings.ToLower(at(g, 0))
		if c == "gh" {
			switch s {
			case "pr":
				switch at(g, 1) {
				case "merge":
					return r(5, "Merging a pull request")
				case "create":
					return r(5, "Opening a pull request")
				}
				return r(5, "Checking pull requests")
			case "issue":
				if at(g, 1) == "create" {
					return r(5, "Opening an issue")
				}
				return r(5, "Checking issues")
			case "run", "workflow":
				return r(4, "Checking the CI runs")
			case "repo":
				if at(g, 1) == "clone" {
					return r(7, "Cloning "+orStr(place(at(g, 2), ""), "a repository"))
				}
			}
			return r(4, "Checking GitHub")
		}
		var gp []string
		for _, x := range g {
			if !isFlag(x) {
				gp = append(gp, x)
			}
		}
		switch {
		case s == "clone":
			u := strings.TrimSuffix(at(gp, 1), ".git")
			return r(7, "Cloning "+orStr(place(u, ""), "a repository"))
		case s == "grep" && at(gp, 1) != "":
			return r(4, `Searching the code for "`+clip(gp[1], 30)+`"`, "search")
		case s == "checkout" && in("-b", g...):
			return r(5, "Creating a branch")
		case s == "branch" && len(gp) > 1:
			return r(5, "Creating a branch")
		}
		if ph, ok := gitPhrase[s]; ok {
			ph.k = "git"
			return ph
		}
		return r(3, "Using git", "git")
	}

	// Reading, listing, searching.
	if readers.MatchString(c) {
		var f []string
		for _, x := range pos {
			if !numRe.MatchString(x) {
				f = append(f, x)
			}
		}
		if len(f) == 0 {
			if s.piped {
				return r(0.2, "", "filter")
			}
			return r(2, "Reading a file", "read")
		}
		p := 3.0
		if s.piped {
			p = 0.2
		}
		return r(p, "Reading "+place(f[0], ""), "read", place(f[0], ""))
	}
	if listers.MatchString(c) {
		d := ""
		for _, x := range pos {
			if !numRe.MatchString(x) {
				d = x
				break
			}
		}
		return r(2, "Looking through "+place(d, "folder"), "list", place(d, "folder"))
	}
	if finders.MatchString(c) {
		nm := ""
		for i, x := range a {
			if x == "-name" || x == "-iname" {
				nm = at(a, i+1)
			}
		}
		if c == "locate" && at(pos, 0) != "" {
			return r(3, `Looking for "`+clip(pos[0], 30)+`"`, "list")
		}
		if nm != "" {
			return r(3, "Looking for "+place(nm, "")+" in "+place(at(pos, 0), "folder"), "list")
		}
		return r(2, "Looking through "+place(at(pos, 0), "folder"), "list", place(at(pos, 0), "folder"))
	}
	if searchers.MatchString(c) {
		if s.piped {
			return r(0.2, "", "filter")
		}
		term := at(pos, 0)
		for i, x := range a {
			if in(strings.ToLower(x), "-e", "--regexp", "-pattern") {
				term = at(a, i+1)
			}
		}
		if term != "" {
			return r(4, `Searching for "`+clip(term, 30)+`"`, "search", clip(term, 30))
		}
		return r(3, "Searching the files", "search")
	}
	if s.piped && filters.MatchString(c) {
		return r(0.2, "", "filter")
	}
	switch {
	case c == "wc":
		return r(3, "Counting lines in "+orStr(place(at(pos, 0), ""), "a file"), "read")
	case c == "du":
		return r(3, "Checking folder sizes", "check")
	case in(c, "df", "get-psdrive"):
		return r(3, "Checking disk space", "check")
	case in(c, "free", "vm_stat"):
		return r(3, "Checking memory use", "check")
	case in(c, "uname", "lsb_release", "systeminfo", "neofetch", "fastfetch", "hostnamectl", "sw_vers", "ver", "lscpu", "lspci", "lsusb", "nvidia-smi", "sysctl"):
		return r(3, "Checking your computer", "check")
	case in(c, "lsof", "ss", "netstat"):
		return r(3, "Checking open ports", "check")
	case in(c, "ps", "top", "htop", "btop", "pgrep", "tasklist", "get-process"):
		return r(3, "Checking running programs", "check")
	case in(c, "kill", "pkill", "killall", "taskkill", "stop-process"):
		return r(5, "Stopping a program")
	case c == "jq" && at(pos, 1) != "":
		return r(3, "Reading "+place(pos[1], ""), "read")
	case in(c, "stat", "file"):
		return r(2, "Checking "+orStr(place(at(pos, 0), ""), "a file"), "read")
	}

	// Changing files.
	last := at(pos, len(pos)-1)
	switch {
	case (c == "sed" && anyMatch(a, regexp.MustCompile(`^-i`))) || (c == "perl" && anyMatch(a, regexp.MustCompile(`^-p?i`))):
		return r(5, "Editing "+orStr(place(last, ""), "a file"), "edit")
	case in(c, "mkdir", "md", "new-item"):
		return r(5, "Creating "+orStr(place(at(pos, 0), ""), "a folder"), "edit")
	case c == "touch":
		return r(5, "Creating "+orStr(place(at(pos, 0), ""), "a file"), "edit")
	case in(c, "rm", "del", "erase", "rmdir", "rd", "remove-item", "unlink", "trash"):
		if len(pos) > 1 {
			return r(5, fmt.Sprintf("Deleting %d files", len(pos)), "edit")
		}
		return r(5, "Deleting "+orStr(place(at(pos, 0), ""), "files"), "edit")
	case in(c, "mv", "move", "ren", "rename", "move-item"):
		verb := "Moving "
		if len(pos) == 2 && !strings.ContainsAny(pos[1], `/\`) && place(pos[0], "") != place(pos[1], "") {
			verb = "Renaming "
		}
		return r(5, verb+orStr(place(at(pos, 0), ""), "files"), "edit")
	case in(c, "cp", "copy", "xcopy", "robocopy", "rsync", "copy-item"):
		return r(5, "Copying "+orStr(place(at(pos, 0), ""), "files"), "edit")
	case c == "ln":
		return r(4, "Linking "+orStr(place(at(pos, 0), ""), "a file"), "edit")
	case in(c, "chmod", "chown", "icacls"):
		return r(4, "Changing permissions on "+orStr(place(last, ""), "a file"), "edit")
	case c == "tee" && at(pos, 0) != "":
		return r(5, "Writing "+place(pos[0], ""), "write")
	case c == "unzip" || (c == "7z" && sub == "x") || c == "expand-archive" || (c == "tar" && anyMatch(a, regexp.MustCompile(`^-?[a-z]*x`))):
		return r(5, "Unpacking an archive", "edit")
	case c == "zip" || c == "compress-archive" || (c == "tar" && anyMatch(a, regexp.MustCompile(`^-?[a-z]*c`))):
		return r(5, "Packing files into an archive", "edit")
	case redirect != "":
		return r(5, "Writing "+place(redirect, ""), "write")
	}

	// Network.
	if in(c, "curl", "wget", "http", "https", "xh", "invoke-webrequest", "iwr", "invoke-restmethod", "irm") {
		u := ""
		for _, x := range a {
			if regexp.MustCompile(`(?i)^https?://`).MatchString(x) {
				u = x
				break
			}
		}
		if u == "" {
			for _, x := range pos {
				if regexp.MustCompile(`(?i)\.[a-z]{2,}(/|$|:)`).MatchString(x) || strings.HasPrefix(x, "localhost") {
					u = x
					break
				}
			}
		}
		dl := c == "wget" || anyMatch(a, regexp.MustCompile(`(?i)^(-o|-O|--output|--remote-name|-outfile)$`))
		verb := "Fetching "
		if dl {
			verb = "Downloading from "
		}
		return r(5, verb+orStr(host(u), "a web page"), "fetch")
	}
	switch {
	case in(c, "ping", "traceroute", "tracert", "test-connection"):
		return r(4, "Checking the connection to "+orStr(host(at(pos, 0)), "the internet"), "check")
	case in(c, "nslookup", "dig", "host"):
		return r(3, "Looking up an address", "check")
	case in(c, "ssh", "mosh"):
		t := at(pos, 0)
		if i := strings.LastIndex(t, "@"); i >= 0 {
			t = t[i+1:]
		}
		return r(5, "Connecting to "+orStr(t, "a server"))
	case in(c, "scp", "sftp"):
		return r(5, "Copying files to a server", "edit")
	case in(c, "docker", "podman", "docker-compose"):
		s, s2 := sub, at(pos, 1)
		if c == "docker-compose" {
			s, s2 = "compose", sub
		}
		if s == "compose" {
			m := map[string]string{"up": "Starting the containers", "down": "Stopping the containers", "logs": "Checking container logs", "ps": "Checking Docker containers", "build": "Building the containers"}
			return r(6, orStr(m[s2], "Managing the containers"))
		}
		m := map[string]string{"ps": "Checking Docker containers", "images": "Checking Docker images", "run": "Starting a container", "start": "Starting a container", "stop": "Stopping a container", "rm": "Removing a container", "logs": "Checking container logs", "exec": "Running a command in a container", "pull": "Downloading a Docker image", "push": "Uploading a Docker image", "inspect": "Checking a container", "info": "Checking Docker", "version": "Checking Docker"}
		return r(5, orStr(m[s], "Using Docker"))
	case in(c, "systemctl", "service", "launchctl", "sc"):
		act, svc := sub, at(pos, 1)
		if c == "service" {
			act, svc = at(pos, 1), at(pos, 0)
		}
		ger := map[string]string{"start": "Starting", "stop": "Stopping", "restart": "Restarting", "reload": "Reloading", "enable": "Enabling", "disable": "Disabling"}
		if g := ger[act]; g != "" {
			return r(5, g+" "+orStr(place(svc, ""), "a service"))
		}
		return r(3, "Checking "+orStr(place(svc, ""), "a service"), "check")
	case in(c, "journalctl", "dmesg", "get-eventlog", "get-winevent"):
		return r(3, "Reading the system logs", "read")
	case in(c, "open", "xdg-open", "start", "explorer", "invoke-item", "ii"):
		t := at(pos, 0)
		if regexp.MustCompile(`^https?:`).MatchString(t) {
			return r(4, "Opening "+orStr(host(t), "a page"), "open")
		}
		return r(4, "Opening "+orStr(place(t, ""), "it"), "open")
	case in(c, "code", "cursor", "subl", "vim", "nvim", "nano"):
		return r(4, "Opening "+orStr(place(at(pos, 0), "folder"), "the editor"), "open")
	case in(c, "ffmpeg", "magick", "convert"):
		return r(5, "Converting media")
	case in(c, "sqlite3", "psql", "mysql"):
		return r(5, "Querying the database")
	case in(c, "kubectl", "helm"):
		return r(5, "Checking the cluster")
	case c == "terraform":
		if sub == "apply" {
			return r(5, "Applying infrastructure changes")
		}
		return r(5, "Checking infrastructure")
	case in(c, "printenv"):
		return r(1, "Checking environment settings", "check")
	case c == "history":
		return r(1, "Checking recent commands", "check")
	case in(c, "crontab", "schtasks"):
		return r(4, "Checking scheduled tasks", "check")
	case c == "ollama":
		switch sub {
		case "pull":
			return r(5, "Downloading a model")
		case "run":
			return r(5, "Running a local model")
		}
		return r(5, "Checking local models")
	}
	return r(1, "", "other")
}

var (
	promptRe = regexp.MustCompile(`^\s*(\$|>|PS [^>]*>)\s*`)
)

// stripHeredocs drops heredoc bodies (file contents, not commands).
func stripHeredocs(cmd string) string {
	for {
		loc := regexp.MustCompile(`<<-?\s*['"]?(\w+)['"]?[^\n]*\n`).FindStringSubmatchIndex(cmd)
		if loc == nil {
			return cmd
		}
		tag := cmd[loc[2]:loc[3]]
		rest := cmd[loc[1]:]
		end := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(tag) + `\s*$`).FindStringIndex(rest)
		head := cmd[:loc[0]] + "\n"
		if end == nil {
			return head
		}
		cmd = head + rest[end[1]:]
	}
}

// DescribeShell is a whole command line as one phrase for its main intent.
func DescribeShell(cmd string) string {
	cmd = strings.ReplaceAll(cmd, "\r", "")
	cmd = stripHeredocs(cmd)
	cmd = strings.ReplaceAll(cmd, "\\\n", " ")
	cmd = promptRe.ReplaceAllString(cmd, "")
	var ps []phrase
	for _, s := range segments(cmd) {
		ps = append(ps, part(s))
	}
	if len(ps) == 0 {
		return "Running a command"
	}
	best := ps[0]
	for _, p := range ps {
		if p.p > best.p {
			best = p
		}
	}
	if best.t == "" || best.p < 0.5 {
		if len(ps) == 1 && ps[0].k == "cd" {
			return ps[0].t
		}
		return "Running a command"
	}
	if best.k == "read" || best.k == "list" {
		var names []string
		seen := map[string]bool{}
		for _, p := range ps {
			if p.k == best.k && p.p == best.p && p.x != "" && !seen[p.x] {
				seen[p.x] = true
				names = append(names, p.x)
			}
		}
		verb, noun := "Reading ", "files"
		if best.k == "list" {
			verb, noun = "Looking through ", "folders"
		}
		if len(names) == 2 {
			return clip(verb+names[0]+" and "+names[1], Max)
		}
		if len(names) > 2 {
			return clip(fmt.Sprintf("%s%s and %d more %s", verb, names[0], len(names)-1, noun), Max)
		}
	}
	return clip(best.t, Max)
}

// ---------- every tool ----------

var conn = map[string]string{
	"gmail_search": "Searching your email", "gmail_read": "Reading an email", "gmail_draft": "Drafting an email", "gmail_send": "Sending an email",
	"gcal_events": "Checking your calendar", "gcal_create": "Adding a calendar event", "drive_search": "Searching your Google Drive", "drive_read": "Reading a Drive file",
	"gcontacts_search": "Looking up a contact", "github_my_repos": "Checking your GitHub repos", "github_my_prs": "Checking your pull requests",
	"github_issues": "Checking GitHub issues", "github_read": "Reading a GitHub issue", "github_create_issue": "Opening a GitHub issue", "github_comment": "Commenting on GitHub",
	"github_notifications": "Checking GitHub notifications", "github_search_repos": "Searching GitHub", "notion_search": "Searching Notion", "notion_read": "Reading a Notion page",
	"notion_append": "Adding to a Notion page", "notion_create_page": "Creating a Notion page", "slack_channels": "Checking your Slack channels", "slack_read": "Reading Slack",
	"slack_post": "Posting to Slack", "spotify_now_playing": "Checking what's playing", "spotify_play": "Playing music on Spotify", "spotify_control": "Controlling Spotify",
	"spotify_search": "Searching Spotify", "suggest_connector": "Suggesting an account to connect", "notify": "Sending a notification",
	"remember": "Saving that to memory", "forget": "Updating memory", "recall": "Checking what I remember", "schedule": "Setting up a schedule",
	"use_skill": "Following a saved skill", "load_skill": "Following a saved skill", "write_skill": "Saving a skill", "system_info": "Checking your computer",
	"job_log": "Checking a background job", "job_input": "Sending input to a background job", "job_stop": "Stopping a background job",
	"todo_write": "Updating the plan", "find_tools": "Getting the right tools", "ask_user": "Asking you a question",
	// The lead's agent tools (Desktop's wording).
	"dispatch_agent": "Handing this to a background agent", "dispatch_specialist": "Briefing a specialist", "apply_specialist": "Applying a specialist's changes",
	"check_specialist": "Checking on the specialists", "message_specialist": "Checking on the specialists", "cancel_specialist": "Checking on the specialists",
	"message_agent": "Checking on your agents", "cancel_agent": "Checking on your agents", "list_agents": "Checking on your agents",
}

var plainBad = regexp.MustCompile(`[{}<>$|;` + "`" + `\\=]|&&|\s--?\w|~/|/\w+/|\bhttps?:|^\W`)

// IsPlain is true for text that reads as words, not a command or code.
func IsPlain(s string) bool {
	t := strings.TrimSpace(s)
	return t != "" && len(t) <= 70 && !plainBad.MatchString(t) && unicode.IsLetter([]rune(t)[0])
}

func orStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

var mcpRe = regexp.MustCompile(`(?i)^mcp_([a-z0-9]+)_(.+)$`)

// mcpServerRe is an MCP server's tool from mcp.json: mcp__<server>__<tool>.
var mcpServerRe = regexp.MustCompile(`^mcp__(.+?)__(.+)$`)

// Describe is a tool call as a short plain phrase.
func Describe(name, rawArgs string) string {
	var a map[string]any
	_ = json.Unmarshal([]byte(rawArgs), &a)
	v := func(k string) string {
		var s string
		switch x := a[k].(type) {
		case string:
			s = x
		case float64, bool:
			s = fmt.Sprint(x)
		}
		return strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	}
	raw := func(k string) string { s, _ := a[k].(string); return s }
	file := func() string { return orStr(place(orStr(v("path"), orStr(v("file"), v("filename"))), ""), "a file") }
	summary := ""
	if IsPlain(v("summary")) {
		summary = clip(capFirst(v("summary")), Max)
	}
	switch name {
	case "run_shell", "shell", "bash", "run_command", "terminal", "exec":
		s := DescribeShell(orStr(raw("command"), orStr(raw("cmd"), raw("script"))))
		bg := a["background"] == true
		if bg && strings.HasPrefix(s, "Running ") && s != "Running the tests" && s != "Running a command" {
			s = "Starting " + strings.TrimPrefix(s, "Running ")
		}
		if s == "Running a command" && summary != "" {
			s = summary
		} else if s == "Running a command" && bg {
			s = "Starting a background job"
		}
		return s
	case "file_read":
		return "Reading " + file()
	case "file_edit":
		return "Editing " + file()
	case "file_write":
		return "Writing " + file()
	case "file_delete":
		return "Deleting " + file()
	case "file_list":
		return "Looking through " + place(v("path"), "folder")
	case "file_search":
		if t := orStr(v("pattern"), v("query")); t != "" {
			return clip(`Searching the files for "`+clip(t, 30)+`"`, Max)
		}
		return "Searching the files"
	case "web_search":
		if q := v("query"); q != "" {
			return clip(`Searching the web for "`+clip(q, 36)+`"`, Max)
		}
		return "Searching the web"
	case "fetch_page":
		return "Reading " + orStr(host(v("url")), "a web page")
	case "http_request":
		return "Calling " + orStr(host(v("url")), "a web service")
	case "download_file":
		return "Downloading from " + orStr(host(v("url")), "the web")
	case "open_url":
		t := orStr(v("target"), v("url"))
		if strings.HasPrefix(t, "http:") || strings.HasPrefix(t, "https:") {
			return "Opening " + orStr(host(t), "a page")
		}
		return "Opening " + orStr(place(t, ""), "a page")
	case "git":
		return DescribeShell("git " + orStr(v("args"), orStr(v("command"), "status")))
	case "run_code":
		langs := map[string]string{"python": "Python", "py": "Python", "node": "JavaScript", "javascript": "JavaScript", "js": "JavaScript", "typescript": "TypeScript", "ts": "TypeScript", "bash": "shell", "sh": "shell", "powershell": "PowerShell", "go": "Go"}
		if summary != "" {
			return summary
		}
		return "Running a " + orStr(langs[strings.ToLower(v("language"))], "quick") + " script"
	case "update_blaze":
		return "Checking for a Blaze update"
	case "install_tool":
		return "Installing " + orStr(place(v("name"), ""), "a tool")
	case "connector_tools":
		if g := strings.TrimPrefix(v("group"), "mcp:"); g != "" {
			return clip("Getting "+capFirst(words(g))+" ready", Max)
		}
		return "Getting your accounts ready"
	}
	if s, ok := conn[name]; ok {
		return s
	}
	if m := mcpServerRe.FindStringSubmatch(name); m != nil {
		server := capFirst(words(m[1]))
		if t := words(m[2]); t != "" {
			return orStr(summary, clip("Asking "+server+" to "+t, Max))
		}
		return orStr(summary, clip("Using "+server, Max))
	}
	if m := mcpRe.FindStringSubmatch(name); m != nil {
		return orStr(summary, clip("Using "+capFirst(words(m[1]))+": "+words(m[2]), Max))
	}
	if name == "" {
		return "Working on it"
	}
	return orStr(summary, clip("Using "+words(name), Max))
}

// Detail is the exact call for the expanded view: the command itself for a shell,
// the code for run_code, otherwise the tool name and its arguments.
func Detail(name, rawArgs string) string {
	var a map[string]any
	_ = json.Unmarshal([]byte(rawArgs), &a)
	s := func(k string) string { x, _ := a[k].(string); return x }
	switch name {
	case "run_shell", "shell", "bash", "run_command", "terminal", "exec":
		return "$ " + orStr(s("command"), orStr(s("cmd"), s("script")))
	case "git":
		return "$ git " + s("args")
	case "run_code":
		return orStr(s("language"), "code") + ":\n" + s("code")
	}
	var b []byte
	if a != nil {
		b, _ = json.MarshalIndent(a, "", "  ")
	}
	if len(b) == 0 || string(b) == "{}" || string(b) == "null" {
		return name
	}
	return name + " " + string(b)
}

var (
	runningRe = regexp.MustCompile(`^Running:\s*(.*)$`)
	jobRe     = regexp.MustCompile(`^Job \d+ started:\s*(.*)$`)
	searchRe  = regexp.MustCompile(`^Searching:\s*(.*)$`)
	urlLineRe = regexp.MustCompile(`^(Reading|Downloading|GET|POST|PUT|PATCH|DELETE|HEAD)\s+(https?://\S+)`)
	wroteRe   = regexp.MustCompile(`^(Wrote|Edited)\s+(.+)$`)
)

// PlainStatus turns a tool's progress line ("Running: cat x") into plain words, or
// "" to keep the current phrase. Lines already in plain words pass through.
func PlainStatus(line string) string {
	t := strings.TrimSpace(strings.SplitN(line, "\n", 2)[0])
	if t == "" {
		return ""
	}
	if m := runningRe.FindStringSubmatch(t); m != nil {
		return DescribeShell(m[1])
	}
	if m := jobRe.FindStringSubmatch(t); m != nil {
		d := DescribeShell(m[1])
		if d == "Running a command" {
			return "Starting a background job"
		}
		return strings.Replace(d, "Running ", "Starting ", 1)
	}
	if strings.HasPrefix(t, "git ") {
		return DescribeShell(t)
	}
	if strings.HasPrefix(t, "pip install") || strings.HasPrefix(t, "npm install") {
		return "Installing dependencies"
	}
	if m := searchRe.FindStringSubmatch(t); m != nil {
		if m[1] != "" {
			return clip(`Searching the web for "`+clip(m[1], 36)+`"`, Max)
		}
		return "Searching the web"
	}
	if m := urlLineRe.FindStringSubmatch(t); m != nil {
		switch m[1] {
		case "Reading":
			return "Reading " + host(m[2])
		case "Downloading":
			return "Downloading from " + host(m[2])
		}
		return "Calling " + host(m[2])
	}
	if m := wroteRe.FindStringSubmatch(t); m != nil {
		if m[1] == "Wrote" {
			return "Writing " + place(m[2], "")
		}
		return "Editing " + place(m[2], "")
	}
	if strings.HasPrefix(t, "Installing ") || IsPlain(t) {
		return clip(t, Max)
	}
	return ""
}

// WorkedLine is the collapsed turn: "Worked · 6 steps · 14s".
func WorkedLine(n int, secs float64) string {
	parts := []string{"Worked", fmt.Sprintf("%d step%s", n, map[bool]string{true: "", false: "s"}[n == 1])}
	if secs >= 1 {
		if secs >= 60 {
			parts = append(parts, fmt.Sprintf("%dm %ds", int(secs)/60, int(secs+0.5)%60))
		} else {
			parts = append(parts, fmt.Sprintf("%.0fs", secs))
		}
	}
	return strings.Join(parts, " · ")
}

// Phrases for the moments the model is working between tools, in plain words like
// every other step (Blaze Desktop never shows "thinking").
const (
	Writing  = "Writing the reply"
	Learning = "Saving what I learned"
)

// Thinking is the working line while the model decides what to do: on the first
// step it reads the request, after a step it says what it is making of that step.
func Thinking(step int, last string) string {
	if step == 0 || last == "" {
		return "Reading your request"
	}
	switch {
	case strings.HasPrefix(last, "Running the tests"):
		return "Checking the test results"
	case strings.HasPrefix(last, "Searching the web"):
		return "Going through the search results"
	case strings.HasPrefix(last, "Building"):
		return "Checking the build"
	case strings.HasPrefix(last, "Briefing"):
		return "Wrapping up the handoff"
	case strings.HasPrefix(last, "Reading ") && !strings.HasPrefix(last, "Reading your"):
		return clip("Going over "+strings.TrimPrefix(last, "Reading "), Max)
	case strings.HasPrefix(last, "Writing ") || strings.HasPrefix(last, "Editing "):
		return "Checking the change"
	}
	return "Working out the next step"
}
