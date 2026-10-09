package ui

// Motion for Blaze's look: the intro, a shimmering status line, step marks.
// Everything stays in the app's palette (ink, greys, one blue accent) and
// switches off with BLAZE_NO_ANIM=1, NO_COLOR, CI or when output is piped.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Animate is true when motion is welcome.
func Animate() bool {
	return color && IsTTY() && os.Getenv("BLAZE_NO_ANIM") == "" && os.Getenv("CI") == ""
}

func rgb(s string) (int, int, int) {
	p := strings.Split(s, ";")
	r, _ := strconv.Atoi(p[0])
	g, _ := strconv.Atoi(p[1])
	b, _ := strconv.Atoi(p[2])
	return r, g, b
}

// mix blends two palette colours, t from 0 (a) to 1 (b).
func mix(a, b string, t float64) string {
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	r1, g1, b1 := rgb(a)
	r2, g2, b2 := rgb(b)
	f := func(x, y int) int { return int(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return fmt.Sprintf("%d;%d;%d", f(r1, r2), f(g1, g2), f(b1, b2))
}

func sleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

func hideCursor() { Printf("\x1b[?25l") }

// ShowCursor puts the cursor back (also call it on exit).
func ShowCursor() {
	if color {
		Printf("\x1b[?25h")
	}
}

// BannerAnimated plays the intro: the square pulses in, the letters land one by
// one with a blue glint, then a glint sweeps the word and a hairline draws out.
func BannerAnimated(version, sub string) {
	if !Animate() {
		Banner(version, sub)
		return
	}
	hideCursor()
	defer ShowCursor()
	letters := []rune("BLAZE")
	word := func(n, glint int) string {
		var b strings.Builder
		for i := 0; i < len(letters); i++ {
			if i > 0 {
				b.WriteString(" ")
			}
			if i >= n {
				b.WriteString(" ")
				continue
			}
			c := pal.ink
			if d := glint - i; d >= 0 && d < 3 {
				c = mix(pal.accent, pal.ink, float64(d)/3)
			}
			b.WriteString(bold() + fg(c) + string(letters[i]) + reset())
		}
		return b.String()
	}
	Println("")
	// square pulses in
	for _, t := range []float64{0.15, 0.4, 0.7, 1} {
		Printf("\r  %s", fg(mix(pal.hair, pal.accent, t))+"■"+reset())
		sleep(35)
	}
	// letters land
	for n := 1; n <= len(letters); n++ {
		Printf("\r  %s  %s", Accent("■"), word(n, n-1))
		sleep(55)
	}
	// glint sweeps across
	for g := 0; g <= len(letters)+2; g++ {
		Printf("\r  %s  %s", Accent("■"), word(len(letters), g))
		sleep(40)
	}
	// version fades in
	for _, t := range []float64{0.3, 0.6, 1} {
		Printf("\r  %s  %s  %s", Accent("■"), word(len(letters), 99), fg(mix(pal.hair, pal.weak, t))+version+reset())
		sleep(40)
	}
	Printf("\r\n")
	if sub != "" {
		// subtitle types out quickly
		r := []rune(sub)
		step := max(1, len(r)/12)
		for i := step; i < len(r)+step; i += step {
			Printf("\r     %s", Sub(string(r[:min(i, len(r))])))
			sleep(14)
		}
		Printf("\r\n")
	}
	// hairline draws out from the left
	n := min(64, termWidth()-4)
	for i := 4; i <= n; i += 6 {
		Printf("\r  %s%s", hair(strings.Repeat("─", i-1)), Accent("─"))
		sleep(8)
	}
	Printf("\r  %s\r\n", hair(strings.Repeat("─", n)))
}

// ---------- spinner / status ----------

// Spinner is the live status line: a turning square in the accent, the status
// text with a glint gliding through it, and the elapsed time once it's slow.
type Spinner struct {
	mu    sync.Mutex
	text  string
	stop  chan struct{}
	done  chan struct{}
	on    bool
	since time.Time
}

var frames = []string{"◰", "◳", "◲", "◱"}

func (s *Spinner) Start(text string) {
	if !color || !IsTTY() {
		return
	}
	s.mu.Lock()
	s.text = text
	if s.on {
		s.mu.Unlock()
		return
	}
	s.on = true
	s.since = time.Now()
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	stop, done := s.stop, s.done
	s.mu.Unlock()
	hideCursor()
	go func() {
		defer close(done)
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			s.mu.Lock()
			txt, since := s.text, s.since
			s.mu.Unlock()
			Printf("\r\x1b[2K%s", spinLine(i, txt, time.Since(since)))
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
}

func spinLine(i int, txt string, el time.Duration) string {
	W := termWidth()
	tail := ""
	if el >= 2*time.Second {
		tail = "  " + Weak(el.Round(time.Second).String())
		if el >= 4*time.Second {
			tail += Weak(" · ctrl-c to stop")
		}
	}
	room := W - 6 - width(tail)
	if room < 8 {
		tail, room = "", W-6
	}
	r := []rune(clip(txt, room))
	// glint: a soft highlight that glides along the text and loops
	g := i % (len(r) + 8)
	var b strings.Builder
	for k, c := range r {
		d := k - g + 2
		if d < 0 {
			d = -d
		}
		col := pal.sub
		if d < 3 {
			col = mix(pal.ink, pal.sub, float64(d)/3)
		}
		b.WriteString(fg(col) + string(c))
	}
	b.WriteString(reset())
	pulse := mix(pal.accent, pal.hair, 0.35*float64((i/2)%2))
	return "  " + fg(pulse) + frames[i%len(frames)] + reset() + " " + b.String() + tail
}

func (s *Spinner) Set(text string) { s.mu.Lock(); s.text = text; s.mu.Unlock() }

func (s *Spinner) Stop() {
	s.mu.Lock()
	if !s.on {
		s.mu.Unlock()
		return
	}
	s.on = false
	close(s.stop)
	done := s.done
	s.mu.Unlock()
	<-done
	Printf("\r\x1b[2K")
	ShowCursor()
}

// ---------- step marks ----------

// StepDone prints a finished tool step: an accent square when it worked, a
// hollow one when it didn't, the action, a short result and how long it took.
func StepDone(ok bool, what, result string, took time.Duration) {
	mark := Accent("■")
	if !ok {
		mark = Ink("□")
	}
	d := ""
	if took >= 100*time.Millisecond {
		d = Weak(fmt.Sprintf(" · %.1fs", took.Seconds()))
	}
	W := termWidth()
	left := "  " + mark + " " + Ink(clip(what, max(10, W/2)))
	room := W - width(left) - width(d) - 3
	res := ""
	if room > 6 && result != "" {
		res = "  " + Weak(clip(result, room))
	}
	Println(left + res + d)
}

// Footer is the quiet line under an answer: which model answered and how long.
func Footer(route string, took time.Duration) {
	Println("\n  " + hair("──") + " " + Weak(fmt.Sprintf("%s · %.1fs", route, took.Seconds())))
}

// Goodbye fades the exit line out of the accent.
func Goodbye() {
	msg := "See you."
	if !Animate() {
		Println("  " + Weak(msg))
		return
	}
	for _, t := range []float64{0, 0.35, 0.7, 1} {
		Printf("\r  %s", fg(mix(pal.accent, pal.weak, t))+"■ "+msg+reset())
		sleep(45)
	}
	Printf("\r\n")
}
