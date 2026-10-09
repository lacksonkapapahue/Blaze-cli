// Package ui draws Blaze's signature look in the terminal: ink on paper,
// one blue accent, square hairline boxes, spaced-out mono labels and 01/02 lists.
package ui

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Palette lifted from the Android app's Theme.kt (dark and light).
type palette struct{ ink, body, sub, weak, hair, accent string }

var dark = palette{"240;240;238", "214;214;212", "160;160;158", "110;110;108", "70;70;74", "107;140;255"}
var light = palette{"17;17;16", "45;45;44", "100;100;98", "150;150;148", "205;205;203", "31;75;216"}

var (
	pal   = dark
	color = true
	Out   = bufio.NewWriter(os.Stdout)
	outMu sync.Mutex
)

func Init() {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		color = false
	}
	if t := strings.ToLower(os.Getenv("BLAZE_THEME")); t == "light" {
		pal = light
	} else if t == "" {
		// COLORFGBG="0;15" means dark text on a light background.
		if v := os.Getenv("COLORFGBG"); v != "" {
			parts := strings.Split(v, ";")
			if bg := parts[len(parts)-1]; bg == "15" || bg == "7" {
				pal = light
			}
		}
	}
	if !enableVT() {
		color = false
	}
}

func fg(rgb string) string {
	if !color {
		return ""
	}
	return "\x1b[38;2;" + rgb + "m"
}

func reset() string {
	if !color {
		return ""
	}
	return "\x1b[0m"
}

func bold() string {
	if !color {
		return ""
	}
	return "\x1b[1m"
}

func Ink(s string) string    { return fg(pal.ink) + s + reset() }
func Body(s string) string   { return fg(pal.body) + s + reset() }
func Sub(s string) string    { return fg(pal.sub) + s + reset() }
func Weak(s string) string   { return fg(pal.weak) + s + reset() }
func Accent(s string) string { return fg(pal.accent) + s + reset() }
func Bold(s string) string   { return bold() + fg(pal.ink) + s + reset() }
func hair(s string) string   { return fg(pal.hair) + s + reset() }

// Field is a spaced label padded to a fixed column, for aligned rows.
func Field(s string, col int) string {
	l := Label(s)
	return l + strings.Repeat(" ", max(1, col-width(l)))
}

// Label is the app's small uppercase, letter-spaced mono caption.
func Label(s string) string {
	r := []rune(strings.ToUpper(s))
	parts := make([]string, len(r))
	for i, c := range r {
		parts[i] = string(c)
	}
	return Sub(strings.Join(parts, " "))
}

func AccentLabel(s string) string {
	r := []rune(strings.ToUpper(s))
	parts := make([]string, len(r))
	for i, c := range r {
		parts[i] = string(c)
	}
	return Accent(strings.Join(parts, " "))
}

func Printf(format string, a ...any) {
	outMu.Lock()
	fmt.Fprintf(Out, format, a...)
	Out.Flush()
	outMu.Unlock()
}

func Println(s string) { Printf("%s\n", s) }

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func width(s string) int { return utf8.RuneCountInString(ansi.ReplaceAllString(s, "")) }

// Box draws a square hairline box (no rounded corners, like the app).
func Box(title string, lines []string) {
	w := width(title) + 4
	for _, l := range lines {
		if n := width(l) + 4; n > w {
			w = n
		}
	}
	if w > 92 {
		w = 92
	}
	top := hair("┌─ ") + title + " " + hair(strings.Repeat("─", max(0, w-width(title)-5))+"┐")
	if title == "" {
		top = hair("┌" + strings.Repeat("─", w-2) + "┐")
	}
	Println(top)
	for _, l := range lines {
		pad := w - width(l) - 3
		if pad < 0 {
			pad = 0
		}
		Println(hair("│ ") + l + strings.Repeat(" ", pad) + hair("│"))
	}
	Println(hair("└" + strings.Repeat("─", w-2) + "┘"))
}

// Rule is a full hairline.
func Rule() { Println(hair(strings.Repeat("─", 64))) }

// Numbered prints an 01 / 02 list like the app's settings pages.
func Numbered(items []string) {
	for i, it := range items {
		Println("  " + Weak(fmt.Sprintf("%02d", i+1)) + "  " + it)
	}
}

func Banner(version, sub string) {
	Println("")
	Println("  " + Accent("■") + "  " + Bold("B L A Z E") + "  " + Weak(version))
	if sub != "" {
		Println("     " + Sub(sub))
	}
	Println("")
}

func Dot(ok bool) string {
	if ok {
		return Accent("■")
	}
	return Ink("□")
}

// ---------- spinner / status ----------

type Spinner struct {
	mu   sync.Mutex
	text string
	stop chan struct{}
	on   bool
}

var frames = []string{"▖", "▘", "▝", "▗"}

func (s *Spinner) Start(text string) {
	if !color {
		return
	}
	s.mu.Lock()
	s.text = text
	if s.on {
		s.mu.Unlock()
		return
	}
	s.on = true
	s.stop = make(chan struct{})
	stop := s.stop
	s.mu.Unlock()
	go func() {
		t := time.NewTicker(110 * time.Millisecond)
		defer t.Stop()
		i := 0
		start := time.Now()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s.mu.Lock()
				txt := s.text
				s.mu.Unlock()
				el := time.Since(start).Round(time.Second)
				Printf("\r\x1b[2K  %s %s %s", Accent(frames[i%len(frames)]), Sub(txt), Weak(el.String()))
				i++
			}
		}
	}()
}

func (s *Spinner) Set(text string) { s.mu.Lock(); s.text = text; s.mu.Unlock() }

func (s *Spinner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.on {
		return
	}
	s.on = false
	close(s.stop)
	time.Sleep(15 * time.Millisecond)
	Printf("\r\x1b[2K")
}

// Step prints a finished-step line: "  · Running: npm test".
func Step(s string) { Println("  " + Weak("·") + " " + Sub(s)) }

// ---------- input ----------

var In = bufio.NewReader(os.Stdin)

func ReadLine(prompt string) (string, error) {
	Printf("%s", prompt)
	s, err := In.ReadString('\n')
	return strings.TrimRight(s, "\r\n"), err
}

func Prompt() string { return "\n" + Accent("›") + " " }

// Confirm shows the app's confirm card: a box with the action and Yes / No.
func Confirm(title, detail string) bool {
	lines := []string{Ink(detail), "", Accent("[y]") + Sub(" yes   ") + Ink("[n]") + Sub(" no")}
	Box(AccentLabel(title), wrapLines(detail, lines))
	ans, err := ReadLine("  " + Accent("?") + " ")
	if err != nil {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(ans))
	return a == "y" || a == "yes"
}

func wrapLines(detail string, lines []string) []string {
	if width(detail) <= 84 {
		return lines
	}
	var out []string
	for len(detail) > 84 {
		out = append(out, Ink(detail[:84]))
		detail = detail[84:]
	}
	out = append(out, Ink(detail))
	return append(out, lines[1:]...)
}

// Secret reads a line without echo when possible (API keys).
func Secret(prompt string) string {
	Printf("%s", prompt)
	if s, ok := readHidden(); ok {
		Println("")
		return strings.TrimSpace(s)
	}
	s, _ := In.ReadString('\n')
	return strings.TrimSpace(s)
}
