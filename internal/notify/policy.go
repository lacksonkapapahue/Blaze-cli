package notify

import (
	"time"
)

// Policy is when the proactive watcher may look at the screen and speak. It is
// deliberately stingy: a fixed interval that doubles with every ignored
// suggestion, an hourly and a daily cap, quiet hours, and nothing while the user
// is away. The watcher sleeps for Verdict.Wait instead of polling.
type Policy struct {
	Every     time.Duration // between checks (default 10 min, never below 5)
	MaxHour   int           // suggestions per rolling hour (default 3)
	MaxDay    int           // suggestions per rolling day (default 8)
	QuietFrom string        // "22:00"; "" with QuietTo "" = no quiet hours
	QuietTo   string        // "08:00"
	AwayAfter time.Duration // idle longer than this = away (default 5 min)
}

// Verdict says whether to check now and, if not, how long to sleep.
type Verdict struct {
	Run  bool
	Wait time.Duration // sleep this long before asking again (always > 0)
	Why  string        // shown in Settings › Notifications
}

const (
	minEvery    = 5 * time.Minute
	maxBackoff  = 3 // the interval doubles at most 3 times (x8)
	awayRecheck = 2 * time.Minute
)

func (p Policy) norm() Policy {
	if p.Every <= 0 {
		p.Every = 10 * time.Minute
	}
	if p.Every < minEvery {
		p.Every = minEvery
	}
	if p.MaxHour <= 0 {
		p.MaxHour = 3
	}
	if p.MaxDay <= 0 {
		p.MaxDay = 8
	}
	if p.AwayAfter <= 0 {
		p.AwayAfter = 5 * time.Minute
	}
	return p
}

// Interval is the wait between checks after `ignored` suggestions in a row went unused.
func (p Policy) Interval(ignored int) time.Duration {
	p = p.norm()
	if ignored > maxBackoff {
		ignored = maxBackoff
	}
	if ignored < 0 {
		ignored = 0
	}
	return p.Every << uint(ignored)
}

// Check decides at now, given the last check, the notification history (newest
// first) and the user's idle time (-1 = unknown).
func (p Policy) Check(now, last time.Time, h []Entry, idle time.Duration) Verdict {
	p = p.norm()
	if InQuiet(now, p.QuietFrom, p.QuietTo) {
		return Verdict{Wait: untilClock(now, p.QuietTo), Why: "Quiet hours until " + p.QuietTo}
	}
	if w, full := capWait(now, h, time.Hour, p.MaxHour); full {
		return Verdict{Wait: w, Why: "Paused: hourly limit reached"}
	}
	if w, full := capWait(now, h, 24*time.Hour, p.MaxDay); full {
		return Verdict{Wait: w, Why: "Paused: daily limit reached"}
	}
	iv := p.Interval(IgnoredAt(h, now))
	if !last.IsZero() {
		if next := last.Add(iv); now.Before(next) {
			return Verdict{Wait: next.Sub(now), Why: ""}
		}
	}
	if idle >= 0 && idle > p.AwayAfter {
		return Verdict{Wait: awayRecheck, Why: "Waiting: you're away from the computer"}
	}
	return Verdict{Run: true, Wait: iv}
}

// capWait: when `max` suggestions were sent inside `win`, how long until the oldest
// of them falls out of the window.
func capWait(now time.Time, h []Entry, win time.Duration, max int) (time.Duration, bool) {
	var in []time.Time
	for _, e := range h {
		if now.Sub(e.At) >= win {
			break
		}
		if e.Kind == "suggest" && e.Err == "" {
			in = append(in, e.At)
		}
	}
	if len(in) < max {
		return 0, false
	}
	oldest := in[max-1] // the max-th newest decides when a slot frees up
	w := oldest.Add(win).Sub(now)
	if w < time.Minute {
		w = time.Minute
	}
	return w, true
}

// untilClock is the time from now to the next hh:mm (at least a minute).
func untilClock(now time.Time, hhmm string) time.Duration {
	m, ok := hm(hhmm)
	if !ok {
		return 30 * time.Minute
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), m/60, m%60, 0, 0, now.Location())
	if !t.After(now) {
		t = t.Add(24 * time.Hour)
	}
	d := t.Sub(now)
	if d < time.Minute {
		d = time.Minute
	}
	return d
}
