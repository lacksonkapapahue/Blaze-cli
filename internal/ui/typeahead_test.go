package ui

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func resetTA(t *testing.T) {
	t.Helper()
	ta = typeahead{}
	t.Cleanup(func() { ta = typeahead{} })
}

func TestTypeaheadQueuesLines(t *testing.T) {
	resetTA(t)
	old := color
	color = false // no composer box: the working line carries the one-line composer
	t.Cleanup(func() { color = old })
	ta.on = true
	for _, r := range "hi blaze\r" {
		ta.key(r)
	}
	for _, r := range "statsu" {
		ta.key(r)
	}
	ta.key(127) // backspace
	ta.key(127)
	for _, r := range "us?\n" {
		ta.key(r)
	}
	for _, r := range "half typed" {
		ta.key(r)
	}
	ta.key('\r')
	ta.key('\r') // an empty line queues nothing
	for _, r := range "two words" {
		ta.key(r)
	}
	ta.key(23) // ctrl-w
	if got := string(ta.buf); got != "two " {
		t.Fatalf("ctrl-w left %q", got)
	}
	if !strings.Contains(typeaheadTail(), "two") || !strings.Contains(typeaheadTail(), "3 queued") {
		t.Errorf("working line = %q", typeaheadTail())
	}
	want := []string{"hi blaze", "status?", "half typed"}
	for _, w := range want {
		if got, ok := NextQueued(); !ok || got != w {
			t.Fatalf("queued %q, want %q", got, w)
		}
	}
	if _, ok := NextQueued(); ok {
		t.Fatal("more queued than typed")
	}
	ta.on = false
	if typeaheadTail() != "" {
		t.Error("the working line shows a composer after the turn")
	}
}

// Outside a turn holdKeys is free; during one it stops the reader until released.
func TestHoldKeys(t *testing.T) {
	resetTA(t)
	holdKeys()() // no turn: nothing to do
	old := typeaheadOK
	typeaheadOK = func() bool { return true }
	defer func() { typeaheadOK = old }()
	ta.on = true
	release := holdKeys()
	ta.mu.Lock()
	held, reading := ta.held, ta.reading
	ta.mu.Unlock()
	if held != 1 || reading {
		t.Fatalf("held=%d reading=%v", held, reading)
	}
	ta.mu.Lock()
	ta.on = false // the turn ended before the prompt closed: don't restart the reader
	ta.mu.Unlock()
	release()
	time.Sleep(10 * time.Millisecond)
	if taReading.Load() {
		t.Fatal("reader restarted after the turn")
	}
}

func TestActivityToggle(t *testing.T) {
	SetActivity(nil)
	actHeightFn = func() int { return 200 }
	out := capture(toggleActivity)
	if !strings.Contains(strings.Join(out, "\n"), "No steps") {
		t.Fatalf("empty toggle = %q", out)
	}
	SetActivity(&Activity{Seconds: 3, Steps: []ActStep{
		{Plain: "Running the tests", Detail: "$ go test ./...", Result: "ok  pkg 0.1s", OK: true, Took: time.Second},
		{Plain: "Editing main.go", Detail: "file_edit {}", Result: "Edited main.go", OK: true, DiffPath: "main.go", Before: "a\n", After: "b\n"},
	}})
	expanded := strings.Join(capture(toggleActivity), "\n")
	for _, w := range []string{"Running the tests", "$ go test ./...", "ok  pkg 0.1s", "Editing main.go", "Updated main.go"} {
		if !strings.Contains(ansi.ReplaceAllString(expanded, ""), w) {
			t.Errorf("expanded view lacks %q:\n%s", w, expanded)
		}
	}
	n := actShown
	if n == 0 {
		t.Fatal("expanded view not tracked")
	}
	collapsed := strings.Join(capture(toggleActivity), "\n")
	if !strings.Contains(collapsed, "\x1b["+strconv.Itoa(n)+"A") || actShown != 0 {
		t.Fatalf("collapse didn't erase %d lines: %q", n, collapsed)
	}
	// Output above the prompt since expanding: collapsing can't erase, it says so.
	capture(toggleActivity)
	touchActivity()
	if c := strings.Join(capture(toggleActivity), "\n"); !strings.Contains(c, "collapsed") || strings.Contains(c, "\x1b[J") {
		t.Fatalf("touched collapse = %q", c)
	}
	if !strings.Contains(workedText(5, 12), "Worked · 5 steps · 12s · ctrl+o to expand") {
		t.Errorf("worked line = %q", workedText(5, 12))
	}
}
