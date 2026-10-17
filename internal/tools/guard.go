package tools

// The safety checks every command goes through before it runs: run_shell, run_code,
// git, job_input and scheduled commands, in every mode.
//
//   blocked  the hard blocklist (wiping a disk, the system or the home folder). Refused
//            in every mode, Build and Auto included, not only in Autopilot.
//   risky    asks the user first in Auto (Autopilot approves it unless it is blocked).
//
// This is best-effort pattern matching on the command text, not a sandbox: a command
// built at run time (a script, eval, a variable set earlier) can still get past it.

import (
	"regexp"
	"strings"
)

// homeVars are the ways a shell names the home folder; they are rewritten to ~.
var homeVars = regexp.MustCompile(`(?i)\$\{HOME\}|\$HOME\b|\$\{env:(USERPROFILE|HOME)\}|\$env:(USERPROFILE|HOME)\b|%USERPROFILE%|%HOMEDRIVE%%HOMEPATH%`)

// normalize undoes the simple tricks that hide a target from the regexes: quotes,
// $HOME spellings and long rm flags.
func normalize(cmd string) string {
	s := strings.NewReplacer(`"`, "", `'`, "").Replace(cmd)
	s = homeVars.ReplaceAllString(s, "~")
	s = longRec.ReplaceAllString(s, " -r")
	s = longForce.ReplaceAllString(s, " -f")
	s = endOpts.ReplaceAllString(s, " ")
	return s
}

var (
	longRec   = regexp.MustCompile(`(?i)\s--recursive\b`)
	longForce = regexp.MustCompile(`(?i)\s--force\b`)
	endOpts   = regexp.MustCompile(`\s--(\s|$)`)
	chmodMode = regexp.MustCompile(`^-[rwxXst]+$`)
)

// segments splits a (normalized) command into simple commands on anything that
// chains, pipes or nests one.
var segSplit = regexp.MustCompile("[;&|\n\r()`{}]+")

// fields splits a segment into words; commas and brackets count as spaces so the
// argument list of a script call (subprocess.run([rm,-rf,~])) reads like a command.
func fields(seg string) []string {
	return strings.FieldsFunc(seg, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == '[' || r == ']'
	})
}

// cmdName is a word's command name: no folder, lowercase, no .exe.
func cmdName(w string) string {
	if i := strings.LastIndexAny(w, `/\`); i >= 0 {
		w = w[i+1:]
	}
	return strings.TrimSuffix(strings.ToLower(w), ".exe")
}

// deleters are the commands whose targets are checked.
var deleters = map[string]bool{
	"rm": true, "remove-item": true, "ri": true, "rd": true, "rmdir": true, "del": true, "erase": true,
	"find": true, "chown": true, "chmod": true, "chgrp": true,
}

// target is one path a destructive command acts on.
type target struct {
	cmd  string
	path string
}

// destructive lists the targets of recursive deletes, find -delete and recursive
// chown/chmod in cmd. none is true when such a command has no visible target (it reads
// them from a pipe or xargs).
func destructive(cmd string) (ts []target, none bool) {
	for _, seg := range segSplit.Split(normalize(cmd), -1) {
		f := fields(seg)
		for i, w := range f {
			name := cmdName(w)
			if !deleters[name] {
				continue
			}
			got, ok := targetsOf(name, f[i+1:])
			if !ok {
				break
			}
			if len(got) == 0 {
				none = true
			}
			for _, p := range got {
				ts = append(ts, target{name, p})
			}
			break
		}
	}
	return ts, none
}

// targetsOf is the paths a command acts on; ok is false when it doesn't destroy anything
// (not recursive, no -delete).
func targetsOf(name string, rest []string) (paths []string, ok bool) {
	switch name {
	case "find":
		del := false
		var starts []string
		opts := false
		for i, a := range rest {
			if strings.HasPrefix(a, "-") || a == "!" {
				opts = true
			}
			if !opts {
				starts = append(starts, a)
			}
			if a == "-delete" || (a == "-exec" || a == "-execdir" || a == "-ok") && i+1 < len(rest) && cmdName(rest[i+1]) == "rm" {
				del = true
			}
		}
		if !del {
			return nil, false
		}
		if len(starts) == 0 {
			starts = []string{"."}
		}
		return starts, true
	case "chown", "chmod", "chgrp":
		rec := false
		var args []string
		for _, a := range rest {
			switch {
			case a == "-R" || a == "--recursive" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "R")):
				rec = true
			case strings.HasPrefix(a, "-") && name == "chmod" && chmodMode.MatchString(a):
				args = append(args, a) // chmod -w: a mode, not a flag
			case strings.HasPrefix(a, "-"):
			default:
				args = append(args, a)
			}
		}
		if !rec || len(args) == 0 {
			return nil, rec
		}
		return args[1:], true // the first word is the owner or the mode
	default: // rm, Remove-Item and its aliases, rd/rmdir, del/erase
		rec := false
		for i := 0; i < len(rest); i++ {
			a := rest[i]
			l := strings.ToLower(a)
			win := len(a) == 2 && a[0] == '/' && (name == "rd" || name == "rmdir" || name == "del" || name == "erase")
			switch {
			case win: // cmd switches: /s /q /f
				if l == "/s" {
					rec = true
				}
			case l == "-path" || l == "-literalpath" || l == "-lp":
				if i+1 < len(rest) {
					paths = append(paths, rest[i+1])
					i++
				}
			case strings.HasPrefix(a, "-"):
				if strings.Contains(l[1:], "r") {
					rec = true // -r, -rf, -fr, -R, -Recurse (PowerShell)
				}
			default:
				paths = append(paths, a)
			}
		}
		if name == "rmdir" && !rec {
			return nil, false // rmdir only removes empty folders
		}
		return paths, rec
	}
}

var (
	driveRoot  = regexp.MustCompile(`(?i)^[a-z]:[\\/]*$`)
	driveSys   = regexp.MustCompile(`(?i)^[a-z]:[\\/]+(windows|users|program files( \(x86\))?|programdata)[\\/]*$`)
	globSuffix = regexp.MustCompile(`[\\/]+\*(\.\*)?$`)
)

// badTarget is true when deleting p would wipe a disk root, the system or the home folder.
func badTarget(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	if strings.HasSuffix(p, "*") {
		q := globSuffix.ReplaceAllString(p, "")
		if q == "" {
			q = "/"
		}
		if q == "~*" || q == "*" {
			return false
		}
		p = q
	}
	if p == "/" || p == "~" || driveRoot.MatchString(p) || driveSys.MatchString(p) {
		return true
	}
	return catastrophicPath(p)
}

// unsafeTarget is true when a delete target isn't a plain path inside the current folder:
// absolute, the home folder, a variable, a glob, or one that climbs out with "..".
func unsafeTarget(p string) bool {
	if p == "." || p == ".." || p == "*" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || strings.HasPrefix(p, "~") ||
		strings.ContainsAny(p, "$%*?") || driveRoot.MatchString(p[:min(len(p), 3)]) {
		return true
	}
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return true
		}
	}
	return false
}

// codeHome is a script expression that names the home folder or a disk root.
const codeHome = `(os\.path\.expanduser\(\s*['"]~[/\\]?['"]\s*\)|(pathlib\.)?Path\.home\(\)|os\.homedir\(\)|os\.environ\[\s*['"](HOME|USERPROFILE)['"]\s*\]|os\.getenv\(\s*['"](HOME|USERPROFILE)['"]\s*\)|process\.env\.(HOME|USERPROFILE)|['"](/|~[/\\]?|[A-Za-z]:[/\\]{0,2})['"])`

// codeWipe matches Python or Node code that deletes the home folder or a disk root.
var codeWipe = regexp.MustCompile(`(?i)(\b(rmtree|rmSync|rmdirSync|removedirs|rm|rmdir)\s*\(\s*` + codeHome + `\s*[,)])|(\[\s*['"]rm['"]\s*,[^\]]*` + codeHome + `\s*[,\]])`)

// codeDelete matches script calls that delete folders; they ask first in Auto.
var codeDelete = regexp.MustCompile(`(?i)\b(shutil\.rmtree|os\.removedirs|fs\.(rm|rmSync|rmdir|rmdirSync)|fsPromises\.rm|Remove-Item)\b`)

// blocked is the hard-blocklist reason for a command ("" = not on it).
func blocked(cmd string) string {
	if catastrophic.MatchString(cmd) || catastrophic.MatchString(normalize(cmd)) || codeWipe.MatchString(cmd) {
		return "it could wipe a disk, the system or your home folder"
	}
	ts, _ := destructive(cmd)
	for _, t := range ts {
		if badTarget(t.path) {
			return "it would delete or change " + t.path + " (a disk root, your home folder or a system folder)"
		}
	}
	return ""
}

// riskyMore are risky commands the original danger pattern misses.
var riskyMore = regexp.MustCompile(`(?i)(\bwipefs\b|\bfdisk\b|\bsfdisk\b|\bparted\b|\bmkfs|\bdd\b[^|;&\n]*\bof=|\bFormat-Volume\b|\bClear-Disk\b|\bbcdedit\b|\bvssadmin\s+delete\b|\bcipher\s+/w\b|\bshred\b)`)

// risky is true when a command should ask first in Auto.
func risky(cmd string) bool {
	if danger.MatchString(cmd) || riskyMore.MatchString(cmd) || catastrophic.MatchString(normalize(cmd)) || codeDelete.MatchString(cmd) {
		return true
	}
	ts, none := destructive(cmd)
	if none {
		return true
	}
	for _, t := range ts {
		if unsafeTarget(t.path) {
			return true
		}
	}
	return false
}

// refusal is the tool result for a blocked command.
func refusal(why string) string {
	return "Refused: " + why + ". Blaze never runs this, in any mode. Use a narrower command on the exact path you mean, or ask the user to do it themselves."
}

// check runs the blocklist on text and, when risk is true, asks first. It returns the
// tool result to give back instead of running ("" = go ahead).
func (e *Env) check(title, text string, risk bool, denied string) string {
	if why := blocked(text); why != "" {
		return refusal(why)
	}
	if risk && !e.ask(title, text) {
		return denied
	}
	return ""
}
