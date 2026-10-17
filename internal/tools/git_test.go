package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("USERPROFILE", "/home/tester")
	ok := map[string][]string{
		"status":                     {"status"},
		"  log   --oneline -15 ":     {"log", "--oneline", "-15"},
		`commit -m "fix: a; b && c"`: {"commit", "-m", "fix: a; b && c"},
		`commit -m 'it''s $(fine)'`:  {"commit", "-m", "its $(fine)"},
		`commit -m "say \"hi\""`:     {"commit", "-m", `say "hi"`},
		`diff -- "a b.go" c\ d.go`:   {"diff", "--", "a b.go", "c d.go"},
		`log --grep="x|y"`:           {"log", "--grep=x|y"},
		`commit -m ""`:               {"commit", "-m", ""},
		"-C ~/proj status":           {"-C", "/home/tester/proj", "status"},
		`show HEAD~1:src\main.go`:    {"show", `HEAD~1:src\main.go`},
		"commit -m \"line1\nline2\"": {"commit", "-m", "line1\nline2"},
	}
	for in, want := range ok {
		got, err := splitArgs(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("splitArgs(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"status && rm -rf src",
		"status; touch pwned",
		"status || true",
		"log | sh",
		"log > out.txt",
		"apply < patch",
		"log $(rm -rf ~)",
		"log `id`",
		`commit -m "$(id)"`,
		"commit -m \"`id`\"",
		"status\nrm -rf ~",
		"status &",
		`commit -m "unterminated`,
	} {
		if got, err := splitArgs(in); err == nil {
			t.Errorf("splitArgs(%q) = %q, want an error", in, got)
		}
	}
}

func TestPlanGit(t *testing.T) {
	allowed := []string{
		"status",
		"git status",
		"log --oneline -15",
		`diff HEAD~1 -- "a b.go"`,
		"-C sub status",
		"--no-pager log -p",
		"grep -n foo",
		"grep -in foo -- src",
		"branch",
		"branch -a",
		"tag",
		"tag -l v*",
		"remote -v",
		"remote show origin",
		"config --get user.name",
		"config --list",
		"stash list",
		"show HEAD --stat",
		`log --grep="fix; bug"`,
		"diff --no-ext-diff",
	}
	for _, a := range allowed {
		if why := planGit(a); why != "" {
			t.Errorf("planGit(%q) = %q, want allowed", a, why)
		}
	}
	rejected := []string{
		"status && rm -rf src",
		"status && touch pwned",
		"status; rm -rf ~",
		"log | sh",
		"log $(rm -rf ~)",
		"status\nrm -rf src",
		"grep -O vim foo",
		"grep -Ovim foo",
		"grep -iO foo",
		"grep --open-files-in-pager=sh foo",
		"grep --open-files foo",
		"diff --output=README.md",
		"log --output x",
		"show -o x",
		"diff --ext-diff",
		"-c core.pager=sh log",
		"-c core.fsmonitor=touch status",
		"--exec-path=/tmp log",
		"-p log",
		"branch -D main",
		"branch newname",
		"branch --delete x",
		"tag v1",
		"tag -d v1",
		"remote add x y",
		"remote remove origin",
		"config user.name x",
		"config --add a.b c --get",
		"config --edit --get x",
		"stash",
		"stash pop",
		"reflog expire --all",
		"push",
		"commit -m x",
		"add -A",
		"reset --hard",
		"checkout main",
	}
	for _, a := range rejected {
		if why := planGit(a); why == "" {
			t.Errorf("planGit(%q) = \"\", want rejected", a)
		}
	}
	if PlanGit("status && rm -rf src") == "" {
		t.Error("PlanGit (used by the main agent and background agents) allowed status && rm -rf src")
	}
}

func TestGitRisky(t *testing.T) {
	for args, want := range map[string]bool{
		"push --force":                      true,
		"push -f origin main":               true,
		"push --force-with-lease":           true,
		"push origin +main":                 true,
		"push origin :old":                  true,
		"push origin main":                  false,
		"reset --hard HEAD~1":               true,
		"reset HEAD~1":                      false,
		"clean -fdx":                        true,
		"clean -n":                          false,
		"branch -D x":                       true,
		"branch -d x":                       false,
		"checkout -- .":                     true,
		"checkout -f main":                  true,
		"checkout main":                     false,
		"restore .":                         true,
		"stash drop":                        true,
		"stash":                             false,
		"-c core.sshCommand=evil fetch":     true,
		"fetch --upload-pack=evil":          true,
		"filter-branch --tree-filter x":     true,
		"commit -m 'reset --hard is risky'": false,
		"status":                            false,
	} {
		argv, err := splitArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		if got := gitRisky(argv); got != want {
			t.Errorf("gitRisky(%q) = %v, want %v", args, got, want)
		}
	}
}

// The git tool runs git directly: shell syntax is refused (Plan or not) and never runs.
func TestGitToolNoShell(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	e, asked := testEnv(t, true)
	e.Shell = NewShell("sh", "/bin/sh", dir)
	if out := Run(e, "git", `{"args":"init -q"}`); !strings.HasPrefix(out, "exit 0") {
		t.Fatalf("git init: %s", out)
	}
	if err := os.Mkdir(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, plan := range []bool{true, false} {
		e.Plan = plan
		for _, args := range []string{"status && rm -rf src", "status; touch pwned", "log $(touch pwned)"} {
			out := Run(e, "git", callJSON(t, map[string]any{"args": args}))
			if strings.HasPrefix(out, "exit") {
				t.Errorf("plan=%v git %q ran: %s", plan, args, out)
			}
			if plan && !strings.Contains(out, "Plan mode is on") {
				t.Errorf("plan=%v git %q = %q, want the Plan-mode refusal", plan, args, out)
			}
		}
		// A path with shell syntax is just a (missing) folder now, not a command.
		out := Run(e, "git", callJSON(t, map[string]any{"args": "status", "path": `x"; touch pwned; "`}))
		if strings.HasPrefix(out, "exit 0") {
			t.Errorf("plan=%v git with a hostile path succeeded: %s", plan, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "src")); err != nil {
		t.Error("src was removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("pwned was created")
	}
	e.Plan = true
	if out := Run(e, "git", `{"args":"status"}`); !strings.HasPrefix(out, "exit 0") {
		t.Errorf("Plan-mode git status: %s", out)
	}
	if out := Run(e, "git", `{"args":"branch -D main"}`); !strings.Contains(out, "Plan mode is on") {
		t.Errorf("Plan-mode git branch -D: %s", out)
	}
	e.Plan = false
	if out := Run(e, "git", `{"args":"commit --allow-empty -q -m \"a; b && c\" --author=\"T <t@x>\""}`); !strings.HasPrefix(out, "exit") {
		t.Errorf("commit with quoted shell characters: %s", out)
	}
	e.Confirm = func(title, detail string) bool { *asked = append(*asked, detail); return false }
	if out := Run(e, "git", `{"args":"reset --hard"}`); !strings.Contains(out, "did NOT approve") {
		t.Errorf("risky git didn't ask: %s", out)
	}
}
