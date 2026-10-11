package agent

// How a tool call reads on screen. Describe is the exact action ("$ go test ./...",
// "edit main.go"), shown when you expand a "Worked · N steps" line; Plain is the
// same step in plain words ("Running the tests", "Editing main.go"), the line you
// see while Blaze works (ported from Blaze Desktop's friendly.go). Both parse the
// arguments as JSON, so escaped quotes, newlines and unicode come out right; an
// argument that isn't a JSON object falls back to the tool's name.

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

// Describe is the one-line exact wording of a tool call ("$ npm test", "read README.md").
func Describe(tc llm.ToolCall) string { return describe(tc) }

// Plain is the plain-words wording of a tool call ("Running the tests").
func Plain(tc llm.ToolCall) string { return PlainStep(tc.Function.Name, tc.Function.Arguments) }

// toolArgs parses a call's arguments; ok is false when they aren't a JSON object.
func toolArgs(raw string) (map[string]any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, true
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

// argStr is a top-level string argument on one line (newlines and tabs become
// spaces, runs of spaces collapse), clipped to n runes ("" when absent or not a string).
func argStr(m map[string]any, k string, n int) string {
	s, _ := m[k].(string)
	s = strings.Join(strings.Fields(s), " ")
	return clipRunes(s, n)
}

func clipRunes(s string, n int) string {
	if n > 0 && utf8.RuneCountInString(s) > n {
		r := []rune(s)
		return string(r[:n]) + "…"
	}
	return s
}

func describe(tc llm.ToolCall) string {
	n := tc.Function.Name
	human := strings.ReplaceAll(n, "_", " ")
	m, ok := toolArgs(tc.Function.Arguments)
	if !ok {
		return human
	}
	v := func(k string) string { return argStr(m, k, 70) }
	with := func(prefix, val string) string {
		if val == "" {
			return human
		}
		return prefix + val
	}
	switch n {
	case "run_shell":
		return with("$ ", v("command"))
	case "file_read", "file_write", "file_edit", "file_delete", "file_list":
		return with(strings.TrimPrefix(n, "file_")+" ", v("path"))
	case "git":
		return with("git ", v("args"))
	case "web_search":
		return with("search ", v("query"))
	case "fetch_page":
		return with("read ", v("url"))
	case "run_code":
		return with("run ", v("language"))
	}
	return human
}

// plainRe: a model-written step summary that reads as words, not a command.
var plainRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 ,.'()]*$`)

var installRe = regexp.MustCompile(`(?i)\b(npm|pnpm|yarn|bun) (i|install|add)\b|\bpip3? install\b|\b(apt|apt-get|dnf|yum|pacman|brew|winget|choco|scoop|cargo|go) (install|-S|get)\b`)

var testRe = regexp.MustCompile(`(?i)\b(go test|npm (run )?test|pnpm test|yarn test|pytest|cargo test|jest|vitest|mvn test|gradle test|make test)\b`)

var buildRe = regexp.MustCompile(`(?i)\b(go build|npm run build|pnpm build|yarn build|cargo build|make|mvn package|gradle build|tsc)\b`)

// PlainStep says what a tool call is doing in plain English.
func PlainStep(name, rawArgs string) string {
	m, ok := toolArgs(rawArgs)
	if !ok {
		m = map[string]any{}
	}
	v := func(k string) string { return argStr(m, k, 0) }
	file := func() string {
		p := v("path")
		if p == "" {
			return "a file"
		}
		return filepath.Base(filepath.FromSlash(p))
	}
	or := func(a, b string) string {
		if a != "" {
			return a
		}
		return b
	}
	var s string
	switch name {
	case "notify":
		s = "Sending a notification"
	case "file_read":
		s = "Reading " + file()
	case "file_edit":
		s = "Editing " + file()
	case "file_write":
		s = "Writing " + file()
	case "file_delete":
		s = "Deleting " + file()
	case "file_list":
		s = "Looking through your files"
	case "file_search":
		s = `Searching your files for "` + or(v("pattern"), v("query")) + `"`
	case "web_search":
		s = `Searching the web for "` + v("query") + `"`
	case "fetch_page":
		s = "Reading " + or(hostOf(v("url")), "a web page")
	case "run_shell":
		cmd := v("command")
		switch sum := v("summary"); {
		case plainRe.MatchString(sum) && utf8.RuneCountInString(sum) <= 60:
			s = strings.ToUpper(sum[:1]) + sum[1:]
		case installRe.MatchString(cmd):
			s = "Installing packages"
		case testRe.MatchString(cmd):
			s = "Running the tests"
		case buildRe.MatchString(cmd):
			s = "Building the project"
		default:
			s = "Running a command"
		}
	case "git":
		s = "Checking the project history"
	case "run_code":
		s = "Running " + or(v("language"), "some") + " code"
	case "install_tool":
		s = "Installing " + or(v("name"), "a tool")
	case "update_blaze":
		s = "Checking for a Blaze update"
	case "remember":
		s = "Saving that to memory"
	case "schedule":
		s = "Setting up a schedule"
	case "use_skill", "load_skill":
		s = "Following a saved skill"
	case "recall":
		s = "Checking what I remember"
	case "system_info":
		s = "Checking your computer"
	case "forget":
		s = "Updating memory"
	case "write_skill":
		s = "Saving a skill"
	case "todo_write":
		s = "Updating the checklist"
	case "http_request":
		s = "Calling " + or(hostOf(v("url")), "a web service")
	case "download_file":
		s = "Downloading " + or(hostOf(v("url")), "a file")
	case "open_url":
		s = "Opening " + or(hostOf(or(v("target"), v("url"))), "a page")
	case "job_log", "job_input":
		s = "Checking a background job"
	case "job_stop":
		s = "Stopping a background job"
	case "dispatch_agent":
		s = "Starting background agent " + or(v("handle"), v("name"))
	case "dispatch_specialist":
		s = "Briefing specialist " + v("name")
	case "check_specialist", "list_agents":
		s = "Checking on the agents"
	case "message_agent", "message_specialist":
		s = "Steering " + or(v("handle"), v("name"))
	case "cancel_agent", "cancel_specialist":
		s = "Stopping " + or(v("handle"), v("name"))
	case "apply_specialist":
		s = "Applying " + v("name") + "'s changes"
		if v("action") == "discard" {
			s = "Discarding " + v("name") + "'s changes"
		}
	default:
		s = "Working on it"
	}
	return clipRunes(strings.TrimSpace(s), 59)
}

func hostOf(s string) string {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return strings.TrimPrefix(u.Host, "www.")
	}
	return s
}
