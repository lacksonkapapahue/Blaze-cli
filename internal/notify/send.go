package notify

// The CLI's sender. The CLI can't use the desktop's in-process ("native") method,
// so it uses the command method: the shared cache's exec method when the desktop
// already probed this machine, otherwise the best command found here (probed once
// per run, never written over the desktop's cache).

import (
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	sendMu   sync.Mutex
	probed   bool
	execM    *Method
	missingM []string
	sent     []time.Time
)

// MaxPerHour caps notifications from one CLI run.
const MaxPerHour = 6

// CommandMethod is the exec method the CLI uses here (nil when none), and what
// was looked for and missing.
func CommandMethod() (*Method, []string) {
	sendMu.Lock()
	defer sendMu.Unlock()
	return commandMethodLocked()
}

func commandMethodLocked() (*Method, []string) {
	if probed {
		return execM, missingM
	}
	probed = true
	if c := Load(); c != nil {
		for _, m := range []*Method{c.Primary, c.Fallback} {
			if m != nil && m.Kind == "exec" && len(m.Argv) > 0 {
				mm := *m
				execM = &mm
				return execM, nil
			}
		}
	}
	execs, missing := ExecCandidates()
	missingM = missing
	if len(execs) > 0 {
		execM = &execs[0]
	}
	return execM, missingM
}

// ErrNoMethod: nothing on this computer can show a notification.
var ErrNoMethod = errors.New("no notification method on this computer")

// ErrRateLimited: too many notifications, or the same one again too soon.
var ErrRateLimited = errors.New("rate limited")

// Send shows n with the command method, rate limited (MaxPerHour, and the same
// title + body at most once in 10 minutes), and records it in the shared history.
func Send(n Note) (string, error) {
	n.Clean()
	if n.Kind == "" {
		n.Kind = "tool"
	}
	sendMu.Lock()
	now := time.Now()
	var keep []time.Time
	for _, t := range sent {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	sent = keep
	if len(sent) >= MaxPerHour {
		sendMu.Unlock()
		return "", ErrRateLimited
	}
	for _, e := range loadHistory() {
		if now.Sub(e.At) > 10*time.Minute {
			break
		}
		if e.Err == "" && strings.EqualFold(e.Title, n.Title) && strings.EqualFold(e.Body, n.Body) {
			sendMu.Unlock()
			return "", ErrRateLimited
		}
	}
	m, _ := commandMethodLocked()
	sent = append(sent, now)
	sendMu.Unlock()
	if m == nil {
		return "", ErrNoMethod
	}
	if n.ID == "" {
		n.ID = now.Format("20060102150405.000")
	}
	err := RunExec(*m, n)
	e := Entry{Note: n, At: now, Method: m.Name}
	if err != nil {
		e.Err = err.Error()
	}
	Record(e)
	return m.Name, err
}
