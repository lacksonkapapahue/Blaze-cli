package tools

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// splitArgs splits a git argument string into words the way a POSIX shell would
// (spaces separate words, '…' and "…" group them, \ escapes a quote, a space or a
// backslash), without running a shell. Anything that would chain, pipe, redirect or
// substitute a command (; & | < > ` $( or a line break outside quotes) is refused, as
// is substitution inside double quotes. A leading ~ is the home folder.
func splitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	const (
		plain = iota
		single
		double
	)
	state := plain
	rs := []rune(s)
	bad := func(what string) error {
		return fmt.Errorf("%s isn't allowed in git arguments: git runs without a shell, so run one git command at a time (no ; && || | < > $( ) or backticks)", what)
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch state {
		case single:
			if r == '\'' {
				state = plain
			} else {
				cur.WriteRune(r)
			}
		case double:
			switch {
			case r == '"':
				state = plain
			case r == '`':
				return nil, bad("`")
			case r == '$' && i+1 < len(rs) && rs[i+1] == '(':
				return nil, bad("$(")
			case r == '\\' && i+1 < len(rs) && (rs[i+1] == '"' || rs[i+1] == '\\'):
				i++
				cur.WriteRune(rs[i])
			default:
				cur.WriteRune(r)
			}
		default:
			switch {
			case r == ' ' || r == '\t':
				if inWord {
					out = append(out, cur.String())
					cur.Reset()
					inWord = false
				}
				continue
			case r == '\n' || r == '\r':
				return nil, bad("a line break")
			case strings.ContainsRune(";&|<>`", r):
				return nil, bad(string(r))
			case r == '$' && i+1 < len(rs) && rs[i+1] == '(':
				return nil, bad("$(")
			case r == '\'':
				state = single
			case r == '"':
				state = double
			case r == '\\' && i+1 < len(rs) && strings.ContainsRune(`"' \`, rs[i+1]):
				i++
				cur.WriteRune(rs[i])
			case r == '~' && !inWord && (i+1 == len(rs) || strings.ContainsRune(`/\ `, rs[i+1])):
				h, _ := os.UserHomeDir()
				cur.WriteString(h)
			default:
				cur.WriteRune(r)
			}
		}
		inWord = true
	}
	if state != plain {
		return nil, fmt.Errorf("unbalanced quotes in git arguments")
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

// gitSub splits git's argument list into its global options and the subcommand + rest.
func gitSub(argv []string) (global []string, sub string, rest []string) {
	i := 0
	for i < len(argv) && strings.HasPrefix(argv[i], "-") {
		if (argv[i] == "-C" || argv[i] == "-c") && i+1 < len(argv) {
			i++
		}
		i++
	}
	global = argv[:i]
	if i < len(argv) {
		sub, rest = argv[i], argv[i+1:]
	}
	return
}

// gitRisky is true for git commands that throw away work or run a configured program;
// they ask first in Auto.
func gitRisky(argv []string) bool {
	global, sub, rest := gitSub(argv)
	for _, g := range global {
		if g == "-c" || strings.HasPrefix(g, "-c") || strings.HasPrefix(g, "--config-env") || strings.HasPrefix(g, "--exec-path") {
			return true // -c core.sshCommand=… and friends run programs
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
	for _, a := range rest {
		if strings.HasPrefix(a, "--upload-pack") || strings.HasPrefix(a, "--receive-pack") || strings.HasPrefix(a, "--exec") {
			return true
		}
	}
	switch sub {
	case "push":
		if has("-f", "--force", "--force-with-lease", "--force-if-includes", "--mirror", "--delete", "-d", "--prune") {
			return true
		}
		for _, a := range rest {
			if strings.HasPrefix(a, "+") || strings.HasPrefix(a, ":") {
				return true // force or delete refspec
			}
		}
	case "reset":
		return has("--hard", "--merge", "--keep")
	case "clean":
		return !has("-n", "--dry-run")
	case "branch":
		return has("-D", "--force", "-f") || (has("-d", "--delete") && has("-f", "--force"))
	case "checkout", "switch", "restore":
		if has("-f", "--force", "--discard-changes", "--overwrite-ignore") {
			return true
		}
		if sub != "switch" {
			for _, a := range rest {
				if a == "." || a == "*" || a == ":/" {
					return true // discards every change
				}
			}
		}
	case "stash":
		return len(rest) > 0 && (rest[0] == "drop" || rest[0] == "clear")
	case "reflog":
		return len(rest) > 0 && (rest[0] == "expire" || rest[0] == "delete")
	case "gc":
		return has("--prune", "--aggressive")
	case "filter-branch", "filter-repo", "update-ref", "replace", "prune":
		return true
	}
	return false
}

// runGit is the git tool: git is started directly with an argument list, never through
// a shell, so its arguments can't chain another command.
func runGit(e *Env, a args) string {
	if _, err := exec.LookPath("git"); err != nil {
		return "git isn't installed. " + installHint(e, "git")
	}
	argv, err := splitArgs(a.s("args"))
	if err != nil {
		return "Error: " + err.Error() + "."
	}
	if len(argv) > 0 && argv[0] == "git" {
		argv = argv[1:]
	}
	if len(argv) == 0 {
		return "Error: give git something to do, e.g. args=\"status\"."
	}
	line := "git " + strings.Join(argv, " ")
	if why := e.check("Run a risky git command?", line, gitRisky(argv) || risky(line), "The user did NOT approve this git command."); why != "" {
		return why
	}
	if pth := strings.TrimSpace(a.s("path")); pth != "" {
		argv = append([]string{"-C", expand(e, pth)}, argv...)
	}
	e.status(line)
	return e.Shell.RunArgv(10*time.Minute, "git", argv...)
}
