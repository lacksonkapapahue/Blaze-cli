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

	"github.com/lacksonkapapahue/blaze-cli/internal/steps"
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
	phase string // thinking / reading / searching / editing / running (w1_turn.go)
}

// sparks: braille dots that rise through two cells, like embers off a blaze.
var sparkBits = [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

func sparks(i int) string {
	// four embers on fixed lanes and speeds, phase-shifted so they never sync
	lanes := [][3]int{{0, 0, 1}, {1, 2, 1}, {2, 1, 2}, {3, 3, 1}}
	var cells [2]rune
	for _, l := range lanes {
		x, phase, speed := l[0], l[1], l[2]
		y := 3 - ((i/speed + phase) % 5)
		if y < 0 {
			continue
		}
		cells[x/2] |= sparkBits[y][x%2]
	}
	return string(0x2800+cells[0]) + string(0x2800+cells[1])
}

func (s *Spinner) Start(text string) { s.StartPhase("", text) }

// StartPhase is Start with the phase given ("" works it out from the words).
func (s *Spinner) StartPhase(phase, text string) {
	if text == steps.Writing {
		w1BreakSteps() // the reply streams between the step marks
	}
	if !color || !IsTTY() {
		return
	}
	if phase == "" {
		phase = steps.PhaseOf(text)
	}
	s.mu.Lock()
	s.text, s.phase = text, phase
	if s.on {
		s.mu.Unlock()
		return
	}
	s.on = true
	spinners.Add(1)
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
			txt, since, phase := s.text, s.since, s.phase
			s.mu.Unlock()
			drawLive(spinLinePhase(i, phase, txt, time.Since(since)))
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
}

// spinLinePhase draws the working line: embers, the phrase in its phase's motion,
// then (as room allows) the goal-gradient rail, the step count, the turn's elapsed
// time, the stop hint and the host's status. On a narrow terminal the extras drop
// off from the least useful end.
func spinLinePhase(i int, phase, txt string, el time.Duration) string {
	W := termWidth()
	tv := w1TurnSnapshot()
	if tv.active {
		el = tv.el
	}
	var extras []string // in order of importance
	if r := w1Rail(tv.done, tv.total); r != "" {
		extras = append(extras, r)
	}
	if tv.active && tv.step > 0 {
		extras = append(extras, Weak(fmt.Sprintf("step %d", tv.step)))
	}
	if el >= 2*time.Second {
		extras = append(extras, Weak(w1Clock(el)))
		if el >= 4*time.Second {
			extras = append(extras, Weak("ctrl-c to stop"))
		}
	}
	status := ""
	if StatusLine != nil {
		if st := StatusLine(); st != "" {
			status = Weak("  ·  ") + st
		}
	}
	ta := typeaheadTail()
	build := func(n int) string {
		if n == 0 {
			return ""
		}
		return "  " + strings.Join(extras[:n], Weak(" · "))
	}
	n := len(extras)
	tail := build(n) + status + ta
	room := W - 6 - width(tail)
	for room < 16 && (n > 0 || status != "") {
		if status != "" {
			status = ""
		} else {
			n--
		}
		tail = build(n) + status + ta
		room = W - 6 - width(tail)
	}
	if room < 8 {
		tail, room = "", W-6
	}
	r := []rune(clip(txt, room))
	var b strings.Builder
	for k, c := range r {
		b.WriteString(fg(w1Shimmer(phase, i, k, len(r))) + string(c))
	}
	b.WriteString(reset())
	pulse := mix(pal.accent, pal.hair, 0.35*float64((i/2)%2))
	tick := i
	if phase == PhaseThinking {
		// thinking: the embers rise at half speed and glow softer
		tick = i / 2
		pulse = mix(pal.accent, pal.hair, 0.3+0.25*float64((i/4)%2))
	}
	return "  " + fg(pulse) + sparks(tick) + reset() + " " + b.String() + tail
}

func (s *Spinner) Set(text string) {
	if text == steps.Writing {
		w1BreakSteps()
	}
	s.mu.Lock()
	s.text, s.phase = text, steps.PhaseOf(text)
	s.mu.Unlock()
}

func (s *Spinner) Stop() {
	s.mu.Lock()
	if !s.on {
		s.mu.Unlock()
		return
	}
	s.on = false
	spinners.Add(-1)
	close(s.stop)
	done := s.done
	s.mu.Unlock()
	<-done
	clearLive()
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
	if w1StepGutter() {
		// a hairline joins this mark to the one above it (a step timeline)
		Println("  " + hair("│"))
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
	st := SessionSoFar()
	if rc := RecapText(st.Tasks, st.Files, time.Since(st.Started)); rc != "" {
		Println("  " + hair("──") + " " + Weak(clip(rc, max(10, termWidth()-8))))
	}
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

// ---------- progress bar ----------

// Progress is a hairline bar that fills in the accent: "━━━━────  42%  3.1 MB".
type Progress struct {
	label      string
	total, got int64
	last       time.Time
}

func NewProgress(label string, total int64) *Progress {
	p := &Progress{label: label, total: total}
	hideCursor()
	p.draw()
	return p
}

func (p *Progress) Write(b []byte) (int, error) {
	p.got += int64(len(b))
	if time.Since(p.last) > 50*time.Millisecond {
		p.draw()
	}
	return len(b), nil
}

func (p *Progress) draw() {
	p.last = time.Now()
	n := 28
	frac := 0.0
	if p.total > 0 {
		frac = float64(p.got) / float64(p.total)
	}
	fill := int(frac * float64(n))
	if fill > n {
		fill = n
	}
	bar := Accent(strings.Repeat("━", fill)) + hair(strings.Repeat("─", n-fill))
	Printf("\r\x1b[2K  %s  %s  %s  %s", Sub(p.label), bar, Ink(fmt.Sprintf("%3.0f%%", frac*100)), Weak(fmt.Sprintf("%.1f MB", float64(p.got)/1e6)))
}

// Done fills the bar and ends the line.
func (p *Progress) Done() {
	if p.total > 0 && p.got >= p.total {
		p.got = p.total
	}
	p.draw()
	Printf("\r\n")
	ShowCursor()
}

// ---------- welcome card ----------

// The site's brand mark: an ink square with a paper "B", drawn in half blocks.
var bGlyph = []string{
	"         ",
	"  █▀▀▀▄  ",
	"  █▄▄▄▀  ",
	"  █   █  ",
	"  ▀▀▀▀   ",
	"         ",
}

func paperRGB() string {
	if pal == light {
		return "252;252;252"
	}
	return "38;38;42"
}

type particle struct {
	x, y float64
	v    float64
	ch   string
}

// Welcome is the boxed start screen: the B mark with particles floating over
// it (like the site's buttons), a glitching eyebrow, the headline and context.
func Welcome(version, model, where, tips string) { welcome(version, model, where, tips, Animate()) }

// Redraw wipes the screen and the terminal's scrollback and draws the start screen
// again, without the intro animation, so /clear leaves nothing of the old chat behind.
func Redraw(version, model, where, tips string) {
	ClearScreen()
	welcome(version, model, where, tips, false)
}

// ClearScreen erases the visible screen and the scrollback and homes the cursor.
// The last turn's Ctrl+O details are dropped too: they belong to the old screen.
func ClearScreen() {
	if !IsTTY() {
		return
	}
	Printf("\x1b[H\x1b[2J\x1b[3J")
	SetActivity(nil)
}

func welcome(version, model, where, tips string, anim bool) {
	W := termWidth()
	if W < 58 {
		BannerAnimated(version, where)
		return
	}
	bw := min(W-3, 110) // box width, same as the input box
	inner := bw - 2
	eyebrow := "AUTOMATION · CODE · YOUR TERMINAL"
	right := []string{
		"",
		"E",
		"",
		"H",
		Sub("Blaze "+version+" · ") + Ink(model),
		Weak(where),
	}
	// the headline greets by the time of day ("Good morning."), the old line as a fallback
	name, resume := w1WelcomeExtras()
	head := TimeGreeting(time.Now().Hour(), name)
	if os.Getenv("BLAZE_NO_GREETING") != "" {
		head = "Made for you."
	}
	markW := 9
	rowsN := len(bGlyph)

	rr := ""
	if r := w1ResumeRow(resume, inner-4); r != "" {
		rr = "   " + r
	}
	frame := func(t int, ps []particle, glitch map[int]bool, typed int) string {
		var sb strings.Builder
		sb.WriteString("  " + hair("┌"+strings.Repeat("─", inner)+"┐") + "\r\n")
		sb.WriteString("  " + hair("│") + strings.Repeat(" ", inner) + hair("│") + "\r\n")
		for r := 0; r < rowsN; r++ {
			// the mark, with particles riding over the ink
			gl := []rune(bGlyph[r])
			var mk strings.Builder
			if !color {
				// no colour: the glyph alone (no raw escapes on a plain terminal)
				mk.WriteString(string(gl[:markW]))
			} else {
				mk.WriteString(bgEsc(pal.ink))
			}
			for c := 0; c < markW && color; c++ {
				ch := string(gl[c])
				col := paperRGB()
				if ch == " " {
					for _, p := range ps {
						if int(p.x) == c && int(p.y) == r {
							ch = p.ch
							col = mix(pal.ink, paperRGB(), 0.55+0.45*(p.y/float64(rowsN)))
						}
					}
				}
				mk.WriteString(fg(col) + ch)
			}
			mk.WriteString(reset())
			var txt string
			switch right[r] {
			case "E":
				words := strings.Split(eyebrow, " ")
				for i, w := range words {
					if glitch[i] && w != "·" {
						w = noise(len([]rune(w)), t+i)
						words[i] = fg(pal.accent) + w + fg(pal.sub)
					}
				}
				txt = Sub(strings.Join(words, " "))
			case "H":
				h := []rune(head)
				txt = Bold(string(h[:min(typed, len(h))]))
				if typed < len(h) && Animate() {
					txt += Accent("▍")
				}
			default:
				txt = right[r]
			}
			txt = clipStyled(txt, inner-markW-7)
			line := "   " + mk.String() + "   " + txt
			sb.WriteString("  " + hair("│") + line + strings.Repeat(" ", max(0, inner-width(line))) + hair("│") + "\r\n")
		}
		sb.WriteString("  " + hair("│") + strings.Repeat(" ", inner) + hair("│") + "\r\n")
		if rr != "" {
			// pick up where you left off: the last unfinished prompt and how to reload it
			sb.WriteString("  " + hair("│") + rr + strings.Repeat(" ", max(0, inner-width(rr))) + hair("│") + "\r\n")
		}
		tl := "   " + clipStyled(tips, inner-4)
		sb.WriteString("  " + hair("│") + tl + strings.Repeat(" ", max(0, inner-width(tl))) + hair("│") + "\r\n")
		sb.WriteString("  " + hair("└"+strings.Repeat("─", inner)+"┘") + "\r\n")
		return sb.String()
	}
	height := rowsN + 5
	if rr != "" {
		height++
	}

	Println("")
	if !anim {
		Printf("%s", frame(0, nil, nil, 99))
		return
	}
	hideCursor()
	defer ShowCursor()
	var ps []particle
	seed := uint32(time.Now().UnixNano())
	rnd := func() float64 {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		return float64(seed%10000) / 10000
	}
	dots := []string{"·", "∙", "•", "˙"}
	const frames = 30
	for t := 0; t <= frames; t++ {
		// spawn at the bottom of the mark, drift up, fade out at the top
		if t < frames-6 && rnd() < 0.85 {
			ps = append(ps, particle{x: 1 + rnd()*7, y: float64(rowsN) - 0.01, v: 0.25 + rnd()*0.35, ch: dots[int(rnd()*4)%4]})
		}
		live := ps[:0]
		for _, p := range ps {
			p.y -= p.v
			p.x += (rnd() - 0.5) * 0.4
			if p.y >= 0 && p.x >= 0 && p.x < float64(markW) {
				live = append(live, p)
			}
		}
		ps = live
		glitch := map[int]bool{}
		if t > 4 && t < frames-3 && rnd() < 0.35 {
			glitch[int(rnd()*5)%5*2] = true
		}
		if t > 0 {
			Printf("\x1b[%dA", height)
		}
		typed := max(0, (t-6)*2)
		Printf("%s", frame(t, ps, glitch, typed))
		sleep(45)
	}
	Printf("\x1b[%dA%s", height, frame(0, nil, nil, 99))
}

func noise(n, t int) string {
	g := []rune("▓▒░#%&@$/\\<>")
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteRune(g[(t*7+i*3)%len(g)])
	}
	return b.String()
}

// clipStyled clips a coloured string to n visible columns (drops styling if cut).
func clipStyled(s string, n int) string {
	if width(s) <= n {
		return s
	}
	return Weak(clip(ansi.ReplaceAllString(s, ""), n))
}
