package ui

// Colour plumbing for the theme work: "r;g;b" parsing, WCAG contrast, and the
// 256-colour fallback for terminals without truecolor.

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// truecolor is false when the terminal only does 256 colours; fg() then sends the
// nearest xterm-256 colour instead of a 24-bit one.
var truecolor = true

// detectTruecolor reads the usual hints. BLAZE_COLOR=256|truecolor overrides them.
func detectTruecolor(getenv func(string) string, goos string) bool {
	switch strings.ToLower(getenv("BLAZE_COLOR")) {
	case "256", "8bit", "ansi256":
		return false
	case "truecolor", "24bit", "true", "16m":
		return true
	}
	switch strings.ToLower(getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return true
	}
	t := strings.ToLower(getenv("TERM"))
	if strings.Contains(t, "direct") || strings.Contains(t, "truecolor") {
		return true
	}
	if getenv("WT_SESSION") != "" {
		return true
	}
	// Windows consoles that accept VT sequences (enableVT) take 24-bit colour too.
	return goos == "windows"
}

func initColorDepth() { truecolor = detectTruecolor(os.Getenv, runtime.GOOS) }

// parseRGB reads "r;g;b". Bad input reads as black.
func parseRGB(s string) (r, g, b int) {
	p := strings.Split(s, ";")
	if len(p) != 3 {
		return 0, 0, 0
	}
	v := [3]int{}
	for i := range p {
		n, err := strconv.Atoi(strings.TrimSpace(p[i]))
		if err != nil {
			return 0, 0, 0
		}
		v[i] = min(255, max(0, n))
	}
	return v[0], v[1], v[2]
}

// relLum is WCAG 2.x relative luminance of an "r;g;b" colour.
func relLum(c string) float64 {
	r, g, b := parseRGB(c)
	lin := func(v int) float64 {
		x := float64(v) / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// contrast is the WCAG contrast ratio between two "r;g;b" colours (1..21).
func contrast(a, b string) float64 {
	la, lb := relLum(a), relLum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// cubeLevels are the six steps of the xterm 6×6×6 colour cube.
var cubeLevels = [6]int{0, 95, 135, 175, 215, 255}

// to256 maps a 24-bit colour to the nearest xterm-256 index (16..255): the colour
// cube or the 24-step grey ramp, whichever is closer.
func to256(r, g, b int) int {
	near := func(v int) int {
		best, bd := 0, 1<<30
		for i, l := range cubeLevels {
			if d := (v - l) * (v - l); d < bd {
				best, bd = i, d
			}
		}
		return best
	}
	ri, gi, bi := near(r), near(g), near(b)
	cr, cg, cb := cubeLevels[ri], cubeLevels[gi], cubeLevels[bi]
	dist := func(x, y, z int) int { return (r-x)*(r-x) + (g-y)*(g-y) + (b-z)*(b-z) }
	cube := 16 + 36*ri + 6*gi + bi
	cd := dist(cr, cg, cb)

	avg := (r + g + b) / 3
	gi2 := (avg - 8 + 5) / 10 // grey ramp: 8, 18, …, 238
	gi2 = min(23, max(0, gi2))
	gv := 8 + 10*gi2
	if gd := dist(gv, gv, gv); gd < cd {
		return 232 + gi2
	}
	return cube
}

var fg256Cache sync.Map // "r;g;b" -> escape

// fgEsc is the foreground escape for an "r;g;b" colour at the terminal's depth.
func fgEsc(rgb string) string { return colorEsc(38, rgb) }

// bgEsc is the background escape for an "r;g;b" colour at the terminal's depth.
// Code that paints a background should use it rather than a raw 48;2 escape.
func bgEsc(rgb string) string { return colorEsc(48, rgb) }

func colorEsc(layer int, rgb string) string {
	if !color {
		return ""
	}
	if truecolor {
		return "\x1b[" + strconv.Itoa(layer) + ";2;" + rgb + "m"
	}
	key := strconv.Itoa(layer) + ":" + rgb
	if v, ok := fg256Cache.Load(key); ok {
		return v.(string)
	}
	r, g, b := parseRGB(rgb)
	e := "\x1b[" + strconv.Itoa(layer) + ";5;" + strconv.Itoa(to256(r, g, b)) + "m"
	fg256Cache.Store(key, e)
	return e
}
