package tools

import (
	"strings"
	"sync"
)

// Todo is one row of the task checklist the model keeps for multi-step work.
type Todo struct {
	Text   string
	Status string // "done", "doing", "todo"
}

var (
	todoMu sync.Mutex
	todos  []Todo
)

// Todos returns the current checklist.
func Todos() []Todo {
	todoMu.Lock()
	defer todoMu.Unlock()
	return append([]Todo(nil), todos...)
}

// ResetTodos clears the checklist (new conversation).
func ResetTodos() { todoMu.Lock(); todos = nil; todoMu.Unlock() }

func init() {
	register("todo_write", `Keep a visible checklist for tasks with 3+ steps. Send the WHOLE list every time, one item per line, each starting with "[x] " (done), "[>] " (doing now, only one) or "[ ] " (to do). Update it as you finish steps.`,
		[]prop{p("items", "string", "The full checklist, one line per item")}, []string{"items"},
		func(e *Env, a args) string {
			var list []Todo
			for _, l := range strings.Split(a.s("items"), "\n") {
				l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "-*"))
				if l == "" {
					continue
				}
				t := Todo{Status: "todo", Text: l}
				switch {
				case strings.HasPrefix(strings.ToLower(l), "[x]"):
					t.Status, t.Text = "done", strings.TrimSpace(l[3:])
				case strings.HasPrefix(l, "[>]"), strings.HasPrefix(l, "[-]"), strings.HasPrefix(l, "[~]"):
					t.Status, t.Text = "doing", strings.TrimSpace(l[3:])
				case strings.HasPrefix(l, "[ ]"):
					t.Text = strings.TrimSpace(l[3:])
				}
				list = append(list, t)
			}
			todoMu.Lock()
			todos = list
			todoMu.Unlock()
			if e.OnTodo != nil {
				e.OnTodo(list)
			}
			done := 0
			for _, t := range list {
				if t.Status == "done" {
					done++
				}
			}
			return "Checklist updated (" + itoa(done) + "/" + itoa(len(list)) + " done)."
		})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
