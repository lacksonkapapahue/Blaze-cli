package ui

import (
	"strings"
	"testing"
	"time"
)

func w1Plain(t *testing.T) {
	t.Helper()
	old := color
	color = false
	t.Cleanup(func() { color = old })
}

func TestRailText(t *testing.T) {
	cases := []struct {
		done, total int
		want        string
	}{
		{0, 0, ""},
		{3, 5, "■■■□□ 3/5"},
		{0, 4, "□□□□ 0/4"},
		{4, 4, "■■■■ 4/4"},
		{9, 4, "■■■■ 4/4"},           // clamped
		{1, 40, "■□□□□□□□□□ 1/40"},   // any progress shows, scaled to 10
		{39, 40, "■■■■■■■■■□ 39/40"}, // full only when done
		{20, 40, "■■■■■□□□□□ 20/40"},
	}
	for _, c := range cases {
		if got := RailText(c.done, c.total); got != c.want {
			t.Errorf("RailText(%d,%d) = %q, want %q", c.done, c.total, got, c.want)
		}
	}
	w1Plain(t)
	if got := w1Rail(3, 5); got != "■■■□□ 3/5" {
		t.Errorf("plain rail = %q", got)
	}
}

func TestChips(t *testing.T) {
	a := &Activity{Steps: []ActStep{
		{Plain: "Editing main.go", OK: true, DiffPath: "main.go", Before: "a\nb\nc\n", After: "a\nB\nc\nd\n"},
		{Plain: "Editing main.go", OK: true, DiffPath: "main.go", Before: "x\n", After: "x\ny\n"},
		{Plain: "Writing notes.md", OK: true, DiffPath: "notes.md", Before: "", After: "one\ntwo\n"},
		{Plain: "Running the tests", OK: false},
		{Plain: "Running the tests", OK: true},
	}}
	c := ChipsOf(a)
	if c.Added != 5 || c.Removed != 1 || c.Files != 2 || c.Tests != "pass" {
		t.Fatalf("chips = %+v", c)
	}
	if got := ChipsText(c); got != "+5 −1 · 2 files · tests ✓" {
		t.Errorf("ChipsText = %q", got)
	}
	if got := ChipsText(TurnChips{Files: 1, Tests: "fail"}); got != "1 file · tests ✗" {
		t.Errorf("ChipsText = %q", got)
	}
	if got := ChipsText(TurnChips{Added: 4, Files: 1}); got != "+4 · 1 file" {
		t.Errorf("adds only = %q", got)
	}
	if got := ChipsText(TurnChips{Removed: 2, Files: 1}); got != "−2 · 1 file" {
		t.Errorf("removes only = %q", got)
	}
	if got := ChipsText(ChipsOf(nil)); got != "" {
		t.Errorf("empty chips = %q", got)
	}
}

func TestCountChanges(t *testing.T) {
	if a, r := w1CountChanges("", "x\ny\n"); a != 2 || r != 0 {
		t.Errorf("create: +%d -%d", a, r)
	}
	if a, r := w1CountChanges("x\ny\n", ""); a != 0 || r != 2 {
		t.Errorf("delete: +%d -%d", a, r)
	}
	if a, r := w1CountChanges("x\nx\ny", "x\ny\ny"); a != 1 || r != 1 {
		t.Errorf("dup lines: +%d -%d", a, r)
	}
}

func TestTimeGreeting(t *testing.T) {
	cases := map[int]string{0: "Working late.", 4: "Working late.", 5: "Good morning.", 11: "Good morning.",
		12: "Good afternoon.", 16: "Good afternoon.", 17: "Good evening.", 22: "Good evening.", 23: "Working late."}
	for h, want := range cases {
		if got := TimeGreeting(h, ""); got != want {
			t.Errorf("TimeGreeting(%d) = %q, want %q", h, got, want)
		}
	}
	if got := TimeGreeting(9, "  Ada Lovelace "); got != "Good morning, Ada." {
		t.Errorf("with name = %q", got)
	}
}

func TestRecapText(t *testing.T) {
	if got := RecapText(0, 3, time.Minute); got != "" {
		t.Errorf("no tasks = %q", got)
	}
	if got := RecapText(3, 14, 6*time.Minute+12*time.Second); got != "3 tasks · 14 files · 6m 12s" {
		t.Errorf("recap = %q", got)
	}
	if got := RecapText(1, 0, 42*time.Second); got != "1 task · 42s" {
		t.Errorf("recap = %q", got)
	}
	if got := w1Clock(time.Hour + 5*time.Minute + 9*time.Second); got != "1h 05m" {
		t.Errorf("clock = %q", got)
	}
}

func TestSessionTotals(t *testing.T) {
	statsBefore := SessionSoFar()
	RecordSessionTurn([]string{"a.go", "b.go"})
	RecordSessionTurn([]string{"a.go"})
	s := SessionSoFar()
	if s.Tasks != statsBefore.Tasks+2 || s.Files < 2 {
		t.Errorf("session = %+v", s)
	}
}

func TestResumeRow(t *testing.T) {
	w1Plain(t)
	r := w1ResumeRow("fix   the login\nbug please", 80)
	if !strings.Contains(r, "P I C K   U P") && !strings.Contains(r, "P I C K") {
		t.Errorf("no label: %q", r)
	}
	if !strings.HasSuffix(r, "/resume") {
		t.Errorf("no hint: %q", r)
	}
	if w1ResumeRow("", 80) != "" || w1ResumeRow("x", 10) != "" {
		t.Error("empty / tiny should hide")
	}
	long := w1ResumeRow(strings.Repeat("word ", 40), 60)
	if width(long) > 60 {
		t.Errorf("row too wide (%d): %q", width(long), long)
	}
}

func TestStepGutter(t *testing.T) {
	EndTurn()
	if w1StepGutter() {
		t.Error("no turn: no gutter")
	}
	BeginTurn()
	defer EndTurn()
	if w1StepGutter() {
		t.Error("first mark has nothing above it")
	}
	if !w1StepGutter() {
		t.Error("second consecutive mark should join")
	}
	touchActivity()
	if w1StepGutter() {
		t.Error("output in between breaks the run")
	}
	var s Spinner
	s.Set("Writing the reply")
	if w1StepGutter() {
		t.Error("the reply streaming breaks the run")
	}
}

func TestSpinLineExtras(t *testing.T) {
	BeginTurn()
	defer EndTurn()
	SetTurnStep(3)
	SetTurnProgress(2, 5)
	l := ansi.ReplaceAllString(spinLinePhase(0, PhaseReading, "Reading main.go", 0), "")
	for _, want := range []string{"Reading main.go", "■■□□□ 2/5", "step 3"} {
		if !strings.Contains(l, want) {
			t.Errorf("working line %q lacks %q", l, want)
		}
	}
	for _, ph := range []string{PhaseThinking, PhaseSearching, PhaseEditing, PhaseRunning} {
		for i := 0; i < 50; i++ {
			if got := ansi.ReplaceAllString(spinLinePhase(i, ph, "Reading main.go", 0), ""); !strings.Contains(got, "Reading main.go") {
				t.Fatalf("phase %s tick %d garbled: %q", ph, i, got)
			}
		}
	}
}

func TestActivityGutter(t *testing.T) {
	w1Plain(t)
	a := &Activity{Steps: []ActStep{
		{Plain: "Reading a.go", Detail: "$ cat a.go", OK: true},
		{Plain: "Reading b.go", Detail: "$ cat b.go", OK: true},
	}}
	ls := activityLines(a, 80)
	if !strings.HasPrefix(ls[2], "  │   $ cat a.go") {
		t.Errorf("first step's detail should carry the gutter: %q", ls[2])
	}
	if !strings.HasPrefix(ls[4], "      $ cat b.go") {
		t.Errorf("last step's detail hangs free: %q", ls[4])
	}
}
