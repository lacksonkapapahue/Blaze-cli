package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
)

func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	t.Setenv("BLAZE_HOME", t.TempDir())
	dir := t.TempDir()
	info := &sysinfo.Info{OS: "linux", Shell: "sh", ShellPath: "/bin/sh"}
	a := New(&config.Config{MaxSteps: 5}, info, dir, false)
	a.Quiet = true
	return a
}

func names(ts []llm.Tool) map[string]bool {
	m := map[string]bool{}
	for _, t := range ts {
		m[t.Function.Name] = true
	}
	return m
}

func TestPlanModeFiltersTools(t *testing.T) {
	a := newTestAgent(t)
	extra := llm.Tool{Type: "function"}
	extra.Function.Name = "dispatch_agent"
	ro := llm.Tool{Type: "function"}
	ro.Function.Name = "gmail_search"
	a.Extra = func() []llm.Tool { return []llm.Tool{extra, ro} }
	a.ReadOnly = func(n string) bool { return n == "gmail_search" }

	build := names(a.toolList())
	if !build["run_shell"] || !build["file_write"] || !build["dispatch_agent"] {
		t.Fatalf("Build mode should have every tool, got %v", build)
	}
	a.Env.Plan = true
	plan := names(a.toolList())
	for _, n := range []string{"run_shell", "file_write", "file_edit", "dispatch_agent"} {
		if plan[n] {
			t.Errorf("Plan mode offers %s", n)
		}
	}
	for _, n := range []string{"file_read", "file_list", "gmail_search"} {
		if !plan[n] {
			t.Errorf("Plan mode is missing %s", n)
		}
	}
}

func TestPlanModeRefusesWritesAndGitChanges(t *testing.T) {
	a := newTestAgent(t)
	a.Env.Plan = true
	if r := a.exec(context.Background(), "file_write", `{"path":"x.txt","content":"hi"}`); r != PlanRefusal {
		t.Fatalf("file_write in Plan = %q", r)
	}
	if r := a.exec(context.Background(), "git", `{"args":"commit -m x"}`); !strings.HasPrefix(r, "Plan mode is on") {
		t.Fatalf("git commit in Plan = %q", r)
	}
}

func TestHostHandlerRunsFirst(t *testing.T) {
	a := newTestAgent(t)
	a.Handle = func(ctx context.Context, name, args string) (string, bool) {
		if name == "notify" {
			return "shown", true
		}
		return "", false
	}
	if r := a.exec(context.Background(), "notify", `{}`); r != "shown" {
		t.Fatalf("host tool = %q", r)
	}
}

func TestAutopilotGate(t *testing.T) {
	a := newTestAgent(t)
	a.Approval = "autopilot"
	if !a.gate("Run a risky command?", "rm -rf ./build") {
		t.Fatal("Autopilot should approve an ordinary risky command")
	}
	if a.gate("Run a risky command?", "rm -rf /") {
		t.Fatal("Autopilot must block wiping the disk")
	}
	if a.gate("Delete?", "/") {
		t.Fatal("Autopilot must block deleting the disk root")
	}
}

func TestYoloFlagMeansAutopilot(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	info := &sysinfo.Info{OS: "linux", Shell: "sh", ShellPath: "/bin/sh"}
	a := New(&config.Config{}, info, t.TempDir(), true)
	if !a.Autopilot() || a.Env.Yolo {
		t.Fatalf("--yolo should map to Autopilot with the gate on (approval=%s yolo=%v)", a.Approval, a.Env.Yolo)
	}
}
