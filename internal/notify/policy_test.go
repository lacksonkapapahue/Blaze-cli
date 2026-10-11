package notify

import (
	"testing"
	"time"
)

func at(h, m int) time.Time { return time.Date(2026, 10, 10, h, m, 0, 0, time.Local) }

func sug(t time.Time, clicked bool) Entry {
	return Entry{Note: Note{Kind: "suggest", Title: "s"}, At: t, Clicked: clicked}
}

func TestInQuiet(t *testing.T) {
	cases := []struct {
		h, m     int
		from, to string
		want     bool
	}{
		{23, 0, "22:00", "08:00", true},
		{3, 0, "22:00", "08:00", true},
		{7, 59, "22:00", "08:00", true},
		{8, 0, "22:00", "08:00", false},
		{12, 0, "22:00", "08:00", false},
		{21, 59, "22:00", "08:00", false},
		{13, 30, "13:00", "14:00", true},
		{14, 0, "13:00", "14:00", false},
		{23, 0, "", "", false},
		{23, 0, "bad", "08:00", false},
		{23, 0, "22:00", "22:00", false},
	}
	for _, c := range cases {
		if got := InQuiet(at(c.h, c.m), c.from, c.to); got != c.want {
			t.Errorf("%02d:%02d in %s–%s = %v, want %v", c.h, c.m, c.from, c.to, got, c.want)
		}
	}
}

func TestPolicyQuietHours(t *testing.T) {
	p := Policy{QuietFrom: "22:00", QuietTo: "08:00"}
	v := p.Check(at(23, 30), time.Time{}, nil, 0)
	if v.Run {
		t.Fatal("must not run in quiet hours")
	}
	if want := 8*time.Hour + 30*time.Minute; v.Wait != want {
		t.Fatalf("should sleep until 08:00 (%v), got %v", want, v.Wait)
	}
	if v := p.Check(at(9, 0), time.Time{}, nil, 0); !v.Run {
		t.Fatal("should run outside quiet hours")
	}
	if v := (Policy{}).Check(at(23, 30), time.Time{}, nil, 0); !v.Run {
		t.Fatal("no quiet hours set: runs at night")
	}
}

func TestPolicyInterval(t *testing.T) {
	p := Policy{Every: 10 * time.Minute}
	now := at(12, 0)
	v := p.Check(now, now.Add(-4*time.Minute), nil, 0)
	if v.Run || v.Wait != 6*time.Minute {
		t.Fatalf("checked 4 min ago: wait 6 min, got %+v", v)
	}
	if v := p.Check(now, now.Add(-10*time.Minute), nil, 0); !v.Run || v.Wait != 10*time.Minute {
		t.Fatalf("due: %+v", v)
	}
	if got := (Policy{Every: time.Minute}).Interval(0); got != 5*time.Minute {
		t.Fatalf("interval floor is 5 min, got %v", got)
	}
	if got := (Policy{}).Interval(0); got != 10*time.Minute {
		t.Fatalf("default interval 10 min, got %v", got)
	}
}

func TestPolicyBackoff(t *testing.T) {
	p := Policy{Every: 10 * time.Minute, MaxHour: 100, MaxDay: 100}
	now := at(12, 0)
	// two suggestions in a row went unused: 10 min x 4
	h := []Entry{sug(now.Add(-30*time.Minute), false), sug(now.Add(-50*time.Minute), false)}
	if v := p.Check(now, now.Add(-30*time.Minute), h, 0); v.Run || v.Wait != 10*time.Minute {
		t.Fatalf("backoff to 40 min after 2 ignored, 30 elapsed: %+v", v)
	}
	// clicking one resets the backoff
	h[0].Clicked = true
	if v := p.Check(now, now.Add(-30*time.Minute), h, 0); !v.Run {
		t.Fatalf("clicked suggestion resets the backoff: %+v", v)
	}
	if got := p.Interval(10); got != 80*time.Minute {
		t.Fatalf("backoff is capped at x8: %v", got)
	}
	// a suggestion only counts as ignored after 15 minutes
	fresh := []Entry{sug(now.Add(-2*time.Minute), false)}
	if IgnoredAt(fresh, now) != 0 {
		t.Fatal("a 2-minute-old suggestion isn't ignored yet")
	}
}

func TestPolicyRateLimit(t *testing.T) {
	p := Policy{Every: 5 * time.Minute, MaxHour: 3, MaxDay: 5}
	now := at(12, 0)
	h := []Entry{
		sug(now.Add(-10*time.Minute), true),
		{Note: Note{Kind: "task"}, At: now.Add(-15 * time.Minute)}, // other kinds don't count
		sug(now.Add(-20*time.Minute), true),
		sug(now.Add(-40*time.Minute), true),
	}
	v := p.Check(now, now.Add(-time.Hour), h, 0)
	if v.Run || v.Why != "Paused: hourly limit reached" {
		t.Fatalf("3 in the last hour: paused, got %+v", v)
	}
	if v.Wait != 20*time.Minute {
		t.Fatalf("a slot frees when the 40-min-old one turns an hour: wait 20 min, got %v", v.Wait)
	}
	// a failed send doesn't count
	h[3].Err = "no daemon"
	if v := p.Check(now, now.Add(-time.Hour), h, 0); !v.Run {
		t.Fatalf("failed sends don't count: %+v", v)
	}
	// daily cap
	var day []Entry
	for i := 0; i < 5; i++ {
		day = append(day, sug(now.Add(-time.Duration(2+i*3)*time.Hour), true))
	}
	if v := p.Check(now, now.Add(-time.Hour), day, 0); v.Run || v.Why != "Paused: daily limit reached" {
		t.Fatalf("5 today: paused, got %+v", v)
	}
}

func TestPolicyAway(t *testing.T) {
	p := Policy{}
	now := at(12, 0)
	if v := p.Check(now, time.Time{}, nil, 20*time.Minute); v.Run || v.Wait != 2*time.Minute {
		t.Fatalf("away: wait, got %+v", v)
	}
	if v := p.Check(now, time.Time{}, nil, -1); !v.Run {
		t.Fatal("unknown idle time doesn't block")
	}
}

func TestVerdictNeverZeroWait(t *testing.T) {
	p := Policy{QuietFrom: "12:00", QuietTo: "12:00"}
	for _, v := range []Verdict{
		p.Check(at(12, 0), time.Time{}, nil, 0),
		(Policy{QuietFrom: "11:00", QuietTo: "12:00"}).Check(at(11, 59), time.Time{}, nil, 0),
	} {
		if v.Wait <= 0 {
			t.Fatalf("wait must be positive so the watcher sleeps: %+v", v)
		}
	}
}
