package ui

// Habit stats: every finished turn adds to its day in ~/.blaze/stats.json (one
// small record per day, the last 365 days kept), and /stats shows a card with the
// day streak, this week's tasks and time, and a 7-day sparkline.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

// DayStat is one day's totals.
type DayStat struct {
	Date  string  `json:"date"` // YYYY-MM-DD, local time
	Tasks int     `json:"tasks"`
	Secs  float64 `json:"secs"`
	Steps int     `json:"steps"`
	Files int     `json:"files"`
}

// Stats is the file's content.
type Stats struct {
	Version int       `json:"version"`
	Days    []DayStat `json:"days"` // oldest first
}

const statsKeepDays = 365

var statsMu sync.Mutex

func statsFile() string { return filepath.Join(config.Dir(), "stats.json") }

// LoadStats reads stats.json (empty when missing or unreadable).
func LoadStats() Stats {
	var s Stats
	if b, err := os.ReadFile(statsFile()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// add puts one turn into its day and trims to the last 365 days before now.
func (s *Stats) add(now time.Time, took time.Duration, steps, files int) {
	day := now.Format("2006-01-02")
	found := false
	for i := range s.Days {
		if s.Days[i].Date == day {
			d := &s.Days[i]
			d.Tasks++
			d.Secs += took.Seconds()
			d.Steps += steps
			d.Files += files
			found = true
			break
		}
	}
	if !found {
		s.Days = append(s.Days, DayStat{Date: day, Tasks: 1, Secs: took.Seconds(), Steps: steps, Files: files})
	}
	sort.Slice(s.Days, func(i, j int) bool { return s.Days[i].Date < s.Days[j].Date })
	cut := now.AddDate(0, 0, -(statsKeepDays - 1)).Format("2006-01-02")
	k := 0
	for _, d := range s.Days {
		if d.Date >= cut {
			s.Days[k] = d
			k++
		}
	}
	s.Days = s.Days[:k]
	s.Version = 1
}

// RecordTurn adds a finished turn (how long it took, its steps and changed files)
// to today's stats. Errors are ignored: stats never get in the way.
func RecordTurn(took time.Duration, steps, files int) {
	statsMu.Lock()
	defer statsMu.Unlock()
	s := LoadStats()
	s.add(time.Now(), took, steps, files)
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	if os.MkdirAll(config.Dir(), 0o700) != nil {
		return
	}
	_ = writeAtomic(statsFile(), b)
}

func (s Stats) byDate() map[string]DayStat {
	m := make(map[string]DayStat, len(s.Days))
	for _, d := range s.Days {
		m[d.Date] = d
	}
	return m
}

// Streak counts consecutive days with at least one task, ending today, or ending
// yesterday when nothing has been done yet today (the streak is still alive).
func (s Stats) Streak(now time.Time) int {
	m := s.byDate()
	d := now
	if m[d.Format("2006-01-02")].Tasks == 0 {
		d = d.AddDate(0, 0, -1)
	}
	n := 0
	for m[d.Format("2006-01-02")].Tasks > 0 {
		n++
		d = d.AddDate(0, 0, -1)
	}
	return n
}

// Week sums this calendar week (Monday through today).
func (s Stats) Week(now time.Time) (tasks int, secs float64, files int) {
	back := (int(now.Weekday()) + 6) % 7 // days since Monday
	m := s.byDate()
	for i := 0; i <= back; i++ {
		d := m[now.AddDate(0, 0, -i).Format("2006-01-02")]
		tasks += d.Tasks
		secs += d.Secs
		files += d.Files
	}
	return
}

// Last7 is the task count of each of the last 7 days, oldest first (today last).
func (s Stats) Last7(now time.Time) [7]int {
	m := s.byDate()
	var out [7]int
	for i := 0; i < 7; i++ {
		out[6-i] = m[now.AddDate(0, 0, -i).Format("2006-01-02")].Tasks
	}
	return out
}

// Totals over everything kept.
func (s Stats) Totals() (tasks, days int, secs float64) {
	for _, d := range s.Days {
		if d.Tasks > 0 {
			tasks += d.Tasks
			days++
			secs += d.Secs
		}
	}
	return
}

var sparkRunes = []rune("▁▂▃▅▇")

// sparkline maps counts to levels of ▁▂▃▅▇ (scaled to the busiest day; a day with
// work is at least ▂). Days with nothing come back as -1 so the caller can draw them as a hairline.
func sparkline(v []int) []int {
	top := 0
	for _, x := range v {
		top = max(top, x)
	}
	out := make([]int, len(v))
	for i, x := range v {
		switch {
		case x <= 0:
			out[i] = -1
		case top <= 1:
			out[i] = len(sparkRunes) - 1
		default:
			// ceil, so any day with work sits above an empty day's ▁
			out[i] = (x*(len(sparkRunes)-1) + top - 1) / top
		}
	}
	return out
}

// SparkText is the plain sparkline ("▁" for empty days), for tests and logs.
func SparkText(v []int) string {
	var b strings.Builder
	for _, l := range sparkline(v) {
		if l < 0 {
			b.WriteRune('▁')
		} else {
			b.WriteRune(sparkRunes[l])
		}
	}
	return b.String()
}

// StatsCard prints the /stats card.
func StatsCard() { statsCard(LoadStats(), time.Now()) }

func statsCard(s Stats, now time.Time) {
	streak := s.Streak(now)
	wt, ws, wf := s.Week(now)
	tt, td, ts := s.Totals()
	last := s.Last7(now)

	var spark, days strings.Builder
	for i, l := range sparkline(last[:]) {
		if l < 0 {
			spark.WriteString(hair("▁") + " ")
		} else {
			spark.WriteString(Accent(string(sparkRunes[l])) + " ")
		}
		wd := now.AddDate(0, 0, i-6).Weekday().String()[:1]
		if i == 6 {
			days.WriteString(Ink(wd) + " ")
		} else {
			days.WriteString(Weak(wd) + " ")
		}
	}

	streakLine := Accent("■") + " " + Ink(fmt.Sprintf("%d day%s", streak, plural(streak))) + Weak(" in a row")
	if streak == 0 {
		streakLine = Ink("□") + " " + Sub("no streak yet") + Weak(" · finish a task today to start one")
	}
	lines := []string{
		Field("streak", 19) + streakLine,
		Field("this week", 19) + Ink(fmt.Sprintf("%d task%s", wt, plural(wt))) + Weak(" · ") + Body(humanDur(time.Duration(ws*float64(time.Second)))+" worked") + Weak(fmt.Sprintf(" · %d file%s", wf, plural(wf))),
		Field("all time", 19) + Body(fmt.Sprintf("%d task%s", tt, plural(tt))) + Weak(fmt.Sprintf(" · %d day%s · ", td, plural(td))) + Body(humanDur(time.Duration(ts*float64(time.Second)))),
		"",
		Field("daily", 19) + spark.String(),
		strings.Repeat(" ", width(Field("daily", 19))) + days.String(),
	}
	Box(AccentLabel("stats"), lines)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
