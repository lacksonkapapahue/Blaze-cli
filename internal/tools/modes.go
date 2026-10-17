package tools

// Work modes and approval modes, ported from Blaze Desktop's modes.go so the CLI
// and the desktop behave the same:
//
//   Plan       read + scout only: the tool list is filtered and any other call is refused.
//   Build      every tool.
//   Auto       risky actions ask first.
//   Autopilot  nothing asks; a hard blocklist of catastrophic actions stays blocked,
//              questions get an automatic "decide yourself" reply, and the run is
//              bounded by a step and time budget.

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// AutoReply is what Autopilot answers when the agent asks the user something.
const AutoReply = "The user is away and can't answer. Make the decision yourself pragmatically, based on your understanding and best practice, note the assumption, and continue. If the task is already done and verified, don't start new work: finish with your summary and the assumptions you made."

// Autopilot's default budget for one run.
const (
	AutopilotSteps   = 150
	AutopilotMinutes = 45
)

// ---------------- Plan mode ----------------

// planTools are the read + scout tools. The CLI's own read-only tools are added.
var planTools = map[string]bool{
	"file_read": true, "file_list": true, "file_search": true,
	"web_search": true, "fetch_page": true, "system_info": true,
	"git":     true, // read-only subcommands only (planGit)
	"job_log": true, "use_skill": true, "load_skill": true, "recall": true,
	"see_screen": true, "screen_read": true, "read_screen": true, "screen_info": true,
	"notify":      true, // a notification changes nothing on disk
	"list_agents": true, // background agents: Plan may look, not start or steer them
	"todo_write":  true, "connector_tools": true, "suggest_connector": true,
}

var gitReadOnly = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "blame": true, "ls-files": true,
	"rev-parse": true, "describe": true, "shortlog": true, "grep": true, "whatchanged": true,
	"ls-tree": true, "cat-file": true, "rev-list": true, "show-ref": true, "for-each-ref": true,
	"branch": true, "tag": true, "remote": true, "reflog": true, "stash": true, "config": true,
}

// planGit is "" when a git argument string only reads. The string is split the way the
// git tool splits it (no shell), so anything that would chain another command is refused.
func planGit(raw string) string {
	argv, err := splitArgs(raw)
	if err != nil {
		return err.Error()
	}
	return planGitArgv(argv)
}

// planGlobal are the git options before the subcommand that Plan allows (-C takes a value).
var planGlobal = map[string]bool{
	"--no-pager": true, "--no-optional-locks": true, "--literal-pathspecs": true,
	"--no-replace-objects": true, "--bare": true,
}

// planGitDanger are options that write a file or start a program, for any subcommand.
// Long options are also refused abbreviated (git accepts unique prefixes).
var planGitDanger = []string{
	"--output", "--open-files-in-pager", "--exec", "--ext-diff", "--upload-pack",
	"--receive-pack", "--edit", "--config-env",
}

// planGitArgv is planGit on an already split argument list.
func planGitArgv(f []string) string {
	if len(f) > 0 && f[0] == "git" {
		f = f[1:]
	}
	for len(f) > 0 && strings.HasPrefix(f[0], "-") {
		switch {
		case f[0] == "-C" && len(f) > 1:
			f = f[2:]
		case planGlobal[f[0]]:
			f = f[1:]
		default:
			return "git option " + f[0] + " isn't allowed while planning"
		}
	}
	if len(f) == 0 {
		return ""
	}
	sub, rest := f[0], f[1:]
	if !gitReadOnly[sub] {
		return "git " + sub + " changes the repository"
	}
	for _, a := range rest {
		if a == "--" {
			break // pathspecs follow
		}
		if strings.HasPrefix(a, "--") {
			name, _, _ := strings.Cut(a, "=")
			for _, d := range planGitDanger {
				if len(name) >= 4 && strings.HasPrefix(d, name) {
					return "git option " + a + " writes a file or starts a program"
				}
			}
			continue
		}
		if strings.HasPrefix(a, "-O") || (sub == "grep" && strings.HasPrefix(a, "-") && strings.Contains(a, "O")) {
			return "git option " + a + " starts a program"
		}
	}
	has := func(xs ...string) bool {
		for _, a := range rest {
			for _, x := range xs {
				if a == x || strings.HasPrefix(a, x+"=") {
					return true
				}
			}
		}
		return false
	}
	switch sub {
	case "branch":
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") || has("-d", "-D", "-m", "-M", "-c", "-C", "--delete", "--move", "--copy", "--set-upstream-to", "-u", "--unset-upstream", "--edit-description", "-f", "--force") {
				return "git branch with those arguments changes branches"
			}
		}
	case "tag":
		if has("-d", "--delete", "-a", "--annotate", "-s", "--sign", "-u", "--local-user", "-f", "--force", "-m", "--message", "-F", "--file", "-e") {
			return "git tag with those arguments changes tags"
		}
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") && !has("-l", "--list") {
				return "git tag with a name creates a tag"
			}
		}
	case "remote":
		if len(rest) > 0 && rest[0] != "-v" && rest[0] != "--verbose" && rest[0] != "show" && rest[0] != "get-url" {
			return "git remote " + rest[0] + " changes remotes"
		}
	case "reflog":
		if len(rest) > 0 && (rest[0] == "expire" || rest[0] == "delete") {
			return "git reflog " + rest[0] + " changes history"
		}
	case "stash":
		if len(rest) == 0 || (rest[0] != "list" && rest[0] != "show") {
			return "git stash changes the working tree"
		}
	case "config":
		if has("--add", "--unset", "--unset-all", "--replace-all", "--rename-section", "--remove-section", "-e", "--edit") {
			return "git config with those arguments changes settings"
		}
		if len(rest) > 0 && (rest[0] == "get" || rest[0] == "list") {
			break
		}
		if !has("--get", "--get-all", "--list", "-l", "--get-regexp") {
			return "git config without --get/--list changes settings"
		}
	}
	if has("-o") && (sub == "diff" || sub == "log" || sub == "show") {
		return "writing git output to a file"
	}
	return ""
}

// PlanAllows reports whether a tool may run in Plan mode (git is checked per call).
func PlanAllows(name string) bool { return planTools[name] }

// PlanGit is "" when a git argument string only reads; otherwise why it doesn't.
func PlanGit(raw string) string { return planGit(raw) }

// ---------------- Autopilot ----------------

// catastrophic matches actions Autopilot never takes, even though it confirms nothing else.
var catastrophic = regexp.MustCompile(`(?i)(\bmkfs(\.\w+)?\b|\bdd\s+[^|;&]*of=/dev/|\bformat(\.com)?\s+[a-z]:|\bdiskpart\b|\bClear-Disk\b|\bInitialize-Disk\b|\bFormat-Volume\b|\bwipefs\b|\bsgdisk\s+(-Z|--zap)|\bfdisk\b|\bparted\b|>\s*/dev/(sd|nvme|disk|hd)|:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}|\bchmod\s+-R\s+0?777\s+/(\s|$)|\bchown\s+-R\s+\S+\s+/(\s|$)|\brm\s+(-[a-z]*\s+)*-[a-z]*r[a-z]*\s+(-[a-z]+\s+)*(/|/\*|~|~/|~/\*|\$HOME|\$HOME/\*|/home|/Users|/etc|/usr|/boot|/var|C:\\?|C:\\Windows|C:\\Users)(\s|$|")|Remove-Item\b.*-Recurse.*\s([A-Z]:\\?|~|\$HOME|\$env:USERPROFILE|C:\\Users|C:\\Windows)(\s|$|")|\brd\s+/s\s+.*[A-Z]:\\(\s|$)|\bdel\s+/[sfq].*[A-Z]:\\\*|\bbcdedit\b|\bvssadmin\s+delete\b|\breg\s+delete\s+HK(LM|EY_LOCAL_MACHINE)\b)`)

// catastrophicPath is true for deleting a disk root, the home folder or one of its parents.
func catastrophicPath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsAny(p, "\n") {
		return false
	}
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(p, "~") {
		p = filepath.Join(home, strings.TrimLeft(p[1:], `/\`))
	}
	if !filepath.IsAbs(p) {
		return false
	}
	c := filepath.Clean(p)
	if c == filepath.VolumeName(c)+string(filepath.Separator) || c == "/" {
		return true
	}
	if home != "" {
		h := filepath.Clean(home)
		if eqPath(c, h) {
			return true
		}
		if rel, err := filepath.Rel(c, h); err == nil && !strings.HasPrefix(rel, "..") {
			return true // a parent of the home folder
		}
		for _, d := range []string{".blaze", ".ssh", "Desktop", "Documents"} {
			if eqPath(c, filepath.Join(h, d)) {
				return true
			}
		}
	}
	for _, d := range []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/var", "/opt", "/root", "/home", "/Users", "/dev", "/sys", "/proc", "/private", "/System", "/Library", "/Applications", `C:\Windows`, `C:\Users`, `C:\Program Files`, `C:\Program Files (x86)`} {
		if eqPath(c, filepath.Clean(d)) {
			return true
		}
	}
	return false
}

func eqPath(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Catastrophic is the hard-blocklist reason for an action ("" = not on it).
func Catastrophic(title, detail string) string {
	if why := blocked(detail); why != "" {
		return why
	}
	if strings.HasPrefix(title, "Delete") && catastrophicPath(detail) {
		return "it would delete a disk root, your home folder or a system folder"
	}
	return ""
}

var questionRe = regexp.MustCompile(`(?i)(\?\s*(\*\*)?\s*$|\b(would you like|do you want|should i|shall i|let me know|which (one|option)|can you (confirm|tell|provide)|could you (confirm|tell|provide)|please (confirm|choose|provide|tell))\b)`)

// AsksUser is true when a reply that ran no tools ends by asking the user something.
func AsksUser(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	lines := strings.Split(t, "\n")
	tail := ""
	for i := len(lines) - 1; i >= 0 && len(tail) < 400; i-- {
		tail = lines[i] + "\n" + tail
		if strings.TrimSpace(lines[i]) != "" && len(tail) > 80 {
			break
		}
	}
	return questionRe.MatchString(strings.TrimSpace(tail))
}
