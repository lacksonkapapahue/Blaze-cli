package ui

// v0.12.0 composer box (W3 #15-#17): the context gauge in the footer, the mode
// label drawn into the top border, and the rotating placeholder tips.

import (
	"strconv"
	"strings"
)

var (
	// ContextUsage reports how much of the model's context window the chat uses
	// (tokens used, window size). nil or max <= 0 hides the gauge. Set by the host.
	ContextUsage func() (used, max int)
	// ModeLabel is the mode named on the composer's top border ("build · auto").
	// nil or "" keeps the plain border. Set by the host.
	ModeLabel func() string
)

const (
	gaugeCells = 8  // bar length
	gaugeHot   = 85 // percent above which the gauge asks for /compact
)

// gaugePct is used/max as a whole percent, clamped to 0..100.
func gaugePct(used, mx int) int {
	if mx <= 0 || used <= 0 {
		return 0
	}
	p := int(float64(used)*100/float64(mx) + 0.5)
	return min(max(p, 0), 100)
}

// gaugeText draws "ctx ━━━━──── 41%" for used of mx; compact drops the bar
// ("ctx 41%") for narrow terminals. "" when mx <= 0.
func gaugeText(used, mx int, compact bool) string {
	if mx <= 0 {
		return ""
	}
	p := gaugePct(used, mx)
	hot := p > gaugeHot
	pct := strconv.Itoa(p) + "%"
	var b strings.Builder
	b.WriteString(Weak("ctx "))
	if !compact {
		on := min(gaugeCells, (p*gaugeCells+50)/100)
		if p > 0 && on == 0 {
			on = 1 // anything used shows
		}
		fill := strings.Repeat("━", on)
		if hot {
			b.WriteString(bold() + Ink(fill))
		} else {
			b.WriteString(Accent(fill))
		}
		b.WriteString(hair(strings.Repeat("─", gaugeCells-on)) + " ")
	}
	if hot {
		b.WriteString(Bold(pct) + Weak(" · ") + Accent("/compact"))
	} else {
		b.WriteString(Sub(pct))
	}
	return b.String()
}

// gauge is the gauge for this terminal width ("" without a ContextUsage hook).
func gauge(W int) string {
	if ContextUsage == nil {
		return ""
	}
	used, mx := ContextUsage()
	return gaugeText(used, mx, W < 60)
}

// withGauge puts g at the right end of the footer f within n columns, clipping
// f first; without room for both, f wins. The result is one line.
func withGauge(f, g string, n int) string {
	if g == "" {
		return clipStyled(f, n)
	}
	gw := width(g)
	if f == "" {
		if gw+2 > n {
			return ""
		}
		return strings.Repeat(" ", max(2, n-gw)) + g
	}
	room := n - gw - 2 // at least two spaces between them
	if room < 20 {
		return clipStyled(f, n)
	}
	f = clipStyled(f, room)
	return f + strings.Repeat(" ", max(2, n-width(f)-gw)) + g
}

// spacedLabel spaces each word's letters (B U I L D) and keeps " · " between
// the parts of a label like "build · auto". The first part is in the accent.
func spacedLabel(s string) string {
	var parts []string
	for i, p := range strings.Split(s, "·") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if i == 0 {
			parts = append(parts, AccentLabel(p))
		} else {
			parts = append(parts, Label(p))
		}
	}
	return strings.Join(parts, Weak(" · "))
}

// topBorder is the box's top edge bw columns wide, with label (when it fits)
// drawn into it: ┌─ B U I L D · A U T O ───┐. The width never changes.
func topBorder(bw int, label string) string {
	plain := hair("┌" + strings.Repeat("─", max(0, bw-2)) + "┐")
	label = strings.TrimSpace(label)
	if label == "" || bw < 16 {
		return plain
	}
	lab := spacedLabel(label)
	if width(lab) > bw-8 { // too long spaced out: plain capitals
		lab = Sub(clip(strings.ToUpper(label), bw-8))
	}
	if width(lab) == 0 {
		return plain
	}
	return hair("┌─ ") + lab + " " + hair(strings.Repeat("─", max(0, bw-width(lab)-5))+"┐")
}

// boxTop is the composer's top border for bw, using the ModeLabel hook.
func boxTop(bw int) string {
	if ModeLabel == nil {
		return topBorder(bw, "")
	}
	return topBorder(bw, ModeLabel())
}

// Tips are the hints the empty prompt rotates through, one per prompt, after the
// first prompt (which shows Editor.Placeholder). The host may replace them.
var Tips = []string{
	"@ to attach a file",
	"! runs a shell command",
	"# saves a memory",
	"shift+tab changes mode",
	"/rewind undoes a turn",
	"ctrl+o shows the last steps",
}

// placeholderFor is the hint for the n-th prompt (0 = the first).
func placeholderFor(first string, n int, tips []string) string {
	if (n == 0 && first != "") || len(tips) == 0 {
		return first
	}
	if first != "" {
		n--
	}
	return tips[n%len(tips)]
}
