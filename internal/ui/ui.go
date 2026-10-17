// Package ui draws Blaze's signature look in the terminal: ink on paper,
// one blue accent, square hairline boxes, spaced-out mono labels and 01/02 lists.
package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// Palette lifted from the Android app's Theme.kt (dark and light). The accent is
// swapped by the theme presets and the greys by high contrast (w4_theme.go).
type palette struct{ ink, body, sub, weak, hair, accent string }

var dark = baseDark
var light = baseLight

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
	if !enableVT() {
		color = false
	}
	initColorDepth()
	initTheme() // presets, light/dark (BLAZE_THEME, OSC 11, COLORFGBG), contrast (w4_theme.go)
}

// fg is the foreground escape for an "r;g;b" colour (24-bit, or the nearest of
// 256 colours on terminals without truecolor).
func fg(rgb string) string { return fgEsc(rgb) }

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

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m|\x1b\]8;[^\x07\x1b]*(?:\x1b\\|\x07)`)

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
	Println("  " + top)
	for _, l := range lines {
		pad := w - width(l) - 3
		if pad < 0 {
			pad = 0
		}
		Println("  " + hair("│ ") + l + strings.Repeat(" ", pad) + hair("│"))
	}
	Println("  " + hair("└"+strings.Repeat("─", w-2)+"┘"))
}

// Rule is a full hairline.
func Rule() { Println(hair(strings.Repeat("─", 64))) }

// Numbered prints an 01 / 02 list like the app's settings pages.
func Numbered(items []string) {
	for i, it := range items {
		Println("  " + Weak(fmt.Sprintf("%02d", i+1)) + "  " + it)
	}
}

// Banner is the static header (help, doctor). BannerAnimated plays the intro.
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

// Step prints a finished-step line: "  · Running: npm test".
func Step(s string) { Println("  " + Weak("·") + " " + Sub(s)) }

// ---------- input ----------

func ReadLine(prompt string) (string, error) {
	defer holdKeys()()
	Printf("%s", prompt)
	if quietSession() {
		return readEchoed()
	}
	s, err := In.ReadString('\n')
	return strings.TrimRight(s, "\r\n"), err
}

func ReadLineContext(ctx context.Context, prompt string) (string, error) {
	if !StdinTTY() || !IsTTY() {
		return "", errors.New("pasting a sign-in address requires an interactive terminal")
	}
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var e Editor
		line, err := e.Read(prompt)
		done <- result{line: line, err: err}
	}()
	select {
	case r := <-done:
		return r.line, r.err
	case <-ctx.Done():
		Wake()
		<-done
		return "", ctx.Err()
	}
}

func Prompt() string { return "\n" + Accent("›") + " " }

// Confirm shows the app's confirm card, then an arrow-key Yes / No.
func Confirm(title, detail string) bool {
	Box(AccentLabel(title), wrapLines(detail, []string{Ink(detail)}))
	return Choose("  "+Accent("?")+" ", []string{"Yes", "No"}, 1, 1) == 0
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
	return out
}

// Secret reads a line without echo when possible (API keys).
func Secret(prompt string) string {
	defer holdKeys()()
	Printf("%s", prompt)
	if s, ok := readHidden(); ok {
		Println("")
		return strings.TrimSpace(s)
	}
	s, _ := In.ReadString('\n')
	return strings.TrimSpace(s)
}
