package steps

import (
	"regexp"
	"strings"
	"testing"
)

func TestDescribe(t *testing.T) {
	cases := []struct{ name, args, want string }{
		{"file_read", `{"path":"internal/ui/editor.go"}`, "Reading editor.go"},
		{"file_edit", `{"path":"main.go","old":"x","new":"y"}`, "Editing main.go"},
		{"file_list", `{"path":"."}`, "Looking through the project folder"},
		{"web_search", `{"query":"wails tray"}`, `Searching the web for "wails tray"`},
		{"fetch_page", `{"url":"https://www.example.com/a?b=1"}`, "Reading example.com"},
		{"run_shell", `{"command":"go test ./..."}`, "Running the tests"},
		{"run_shell", `{"command":"cd src && npm install"}`, "Installing dependencies"},
		{"run_shell", `{"command":"go build -o /tmp/blaze . && go vet ./..."}`, "Building the project"},
		{"run_shell", `{"command":"cat a.md; cat b.md | head -5"}`, "Reading a.md and b.md"},
		{"run_shell", `{"command":"grep -rn \"TODO\" internal | wc -l"}`, `Searching for "TODO"`},
		{"run_shell", `{"command":"curl -sL https://api.github.com/repos/x/y -o out.json"}`, "Downloading from api.github.com"},
		{"run_shell", `{"command":"echo hi > notes.md"}`, "Writing notes.md"},
		{"run_shell", `{"command":"x --y","summary":"checking disk space"}`, "Checking disk space"},
		{"run_shell", `{"command":"x","summary":"rm -rf ~/; echo"}`, "Running a command"},
		{"run_shell", `{"command":"npm run dev","background":true}`, "Starting the dev server"},
		{"run_shell", `{"command":"cat > f.go <<'EOF'\npackage main\nrm -rf /\nEOF"}`, "Writing f.go"},
		{"git", `{"args":"status --short"}`, "Checking git status"},
		{"git", `{"args":"push origin main"}`, "Pushing changes"},
		{"run_code", `{"language":"python","code":"print(1)"}`, "Running a Python script"},
		{"dispatch_specialist", `{"name":"api-tests","brief":"..."}`, "Briefing a specialist"},
		{"dispatch_agent", `{}`, "Handing this to a background agent"},
		{"mcp_linear_list_issues", `{}`, "Using Linear: list issues"},
		{"mystery_tool", `{}`, "Using mystery tool"},
		{"", ``, "Working on it"},
	}
	for _, c := range cases {
		if got := Describe(c.name, c.args); got != c.want {
			t.Errorf("Describe(%s, %s) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

// A phrase never carries a command: no flags, pipes, redirects, $vars, paths with
// slashes, JSON or code, whatever the model sends.
func TestDescribeNeverLeaksCommands(t *testing.T) {
	leak := regexp.MustCompile(`[{}<>$|;` + "`" + `\\=]|&&|\s-{1,2}\w|/\w|\bhttps?:|\./`)
	cmds := []string{
		"rm -rf ~/projects/old && echo done",
		"sudo apt-get install -y ripgrep jq",
		"python3 -c 'import os; print(os.listdir(\"/etc\"))'",
		"find . -name '*.go' -exec grep -l Wake {} \\;",
		"for f in *.go; do gofmt -l $f; done",
		"git -C /tmp/repo log --oneline -5 | cat",
		"PATH=/tmp/go/bin:$PATH GOTOOLCHAIN=local go test ./internal/...",
		"docker run --rm -v $(pwd):/src golang:1.26 go build ./...",
		"powershell -Command \"Get-ChildItem -Recurse C:\\Users\"",
		"node scripts/build.mjs --prod",
		"./install.sh --prefix=/usr/local",
		"kubectl get pods -n kube-system -o wide",
		"echo $SECRET_TOKEN | base64 -d",
		"curl -X POST -H 'Authorization: Bearer abc' https://x.io/v1/run -d '{\"a\":1}'",
		"awk '{print $1}' access.log | sort | uniq -c",
		"weird-binary --flag=1 /some/path",
	}
	for _, c := range cmds {
		for _, tool := range []string{"run_shell", "git"} {
			args := `{"command":` + quote(c) + `,"args":` + quote(c) + `}`
			got := Describe(tool, args)
			if leak.MatchString(got) || len([]rune(got)) > Max {
				t.Errorf("%s %q leaked: %q", tool, c, got)
			}
		}
		if got := PlainStatus("Running: " + c); leak.MatchString(got) {
			t.Errorf("PlainStatus(%q) leaked: %q", c, got)
		}
	}
	for _, s := range []string{`{"path":"a.go","content":"func main() { os.Exit(1) }"}`, `not json`, `{"code":"rm -rf /"}`} {
		for _, tool := range []string{"file_write", "run_code", "notion_append", "x_y"} {
			if got := Describe(tool, s); leak.MatchString(got) || strings.Contains(got, "rm ") {
				t.Errorf("%s %s leaked: %q", tool, s, got)
			}
		}
	}
}

func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func TestDetailIsExact(t *testing.T) {
	if got := Detail("run_shell", `{"command":"go test ./..."}`); got != "$ go test ./..." {
		t.Errorf("shell detail = %q", got)
	}
	if got := Detail("git", `{"args":"log -3"}`); got != "$ git log -3" {
		t.Errorf("git detail = %q", got)
	}
	if got := Detail("file_read", `{"path":"a.go"}`); !strings.Contains(got, `"path": "a.go"`) {
		t.Errorf("tool detail = %q", got)
	}
}

func TestPlainStatus(t *testing.T) {
	cases := map[string]string{
		"Running: go test ./...":            "Running the tests",
		"Job 3 started: npm run dev":        "Starting the dev server",
		"Searching: blaze cli":              `Searching the web for "blaze cli"`,
		"Reading https://go.dev/doc/":       "Reading go.dev",
		"Making its own copy of the folder": "Making its own copy of the folder",
		"$ rm -rf /tmp/x":                   "",
	}
	for in, want := range cases {
		if got := PlainStatus(in); got != want {
			t.Errorf("PlainStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWorkedLine(t *testing.T) {
	cases := map[string]string{
		WorkedLine(1, 0.4):  "Worked · 1 step",
		WorkedLine(5, 12.2): "Worked · 5 steps · 12s",
		WorkedLine(9, 75):   "Worked · 9 steps · 1m 15s",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestThinking(t *testing.T) {
	cases := []struct {
		step       int
		last, want string
	}{
		{0, "", "Reading your request"},
		{1, "Running the tests", "Checking the test results"},
		{2, `Searching the web for "x"`, "Going through the search results"},
		{1, "Reading main.go", "Going over main.go"},
		{1, "Editing main.go", "Checking the change"},
		{3, "Looking through the project folder", "Working out the next step"},
	}
	for _, c := range cases {
		got := Thinking(c.step, c.last)
		if got != c.want {
			t.Errorf("Thinking(%d, %q) = %q, want %q", c.step, c.last, got, c.want)
		}
		if strings.Contains(strings.ToLower(got), "thinking") || !IsPlain(got) {
			t.Errorf("Thinking(%d, %q) = %q is not plain words", c.step, c.last, got)
		}
	}
}
