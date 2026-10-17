package ui

import "strings"

// Chat layout: what you send sits on the right, Blaze's replies on the left
// (Markdown is rendered from column 2, see md.go). UserLines lays out one of
// your messages for the scrollback.

// userMaxFrac is how much of the terminal a message of yours may take.
const userMaxFrac = 0.7

// UserLines lays out a message you sent, right-aligned like a chat bubble:
// wrapped to at most ~70% of the terminal, the block's widest line ending one
// column before the right margin, the first line marked with the accent ‹. note (optional) is
// a dim line under it ("sent while Blaze worked"). On a narrow or plain
// terminal it falls back to the old left-hand "› text" record.
func UserLines(text, note string) []string {
	W := termWidth()
	text = strings.TrimRight(text, "\r\n")
	if !color || W < 30 {
		out := []string{Accent("›") + " " + Ink(text)}
		if note != "" {
			out[0] += Weak("  · " + note)
		}
		return out
	}
	const mark = 2   // "‹ " / "  " in front of each line
	const margin = 1 // free column at the right edge (avoids auto-wrap)
	maxw := int(float64(W) * userMaxFrac)
	if maxw < 20 {
		maxw = 20
	}
	if maxw > W-margin-mark-2 {
		maxw = W - margin - mark - 2
	}
	// The block sits flush right; inside it the text reads left to right as usual.
	lines := wrapText(text, maxw)
	block := 0
	for _, l := range lines {
		block = max(block, strWidth(l))
	}
	pad := max(0, W-margin-mark-block)
	var out []string
	for i, l := range lines {
		lead := "  "
		if i == 0 {
			lead = Accent("‹") + " "
		}
		out = append(out, strings.Repeat(" ", pad)+lead+Ink(l))
	}
	if note != "" {
		n := "· " + note
		if w := strWidth(n); w > maxw {
			n = clip(n, maxw)
		}
		out = append(out, strings.Repeat(" ", max(0, W-margin-strWidth(n)))+Weak(n))
	}
	return out
}

// strWidth is the display width of plain text (wide runes count 2, tabs 1).
func strWidth(s string) int {
	w := 0
	for _, r := range s {
		_, rw := dispRune(r)
		w += rw
	}
	return w
}

// wrapText wraps plain text to n columns, breaking at spaces where it can and
// mid-word when a word is longer than a line. Newlines start a new line.
func wrapText(s string, n int) []string {
	if n < 1 {
		n = 1
	}
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(s, "\t", " "), "\n") {
		para = strings.TrimRight(para, " \r")
		if para == "" {
			out = append(out, "")
			continue
		}
		var line strings.Builder
		lw := 0
		flush := func() {
			out = append(out, line.String())
			line.Reset()
			lw = 0
		}
		for _, word := range strings.Fields(para) {
			ww := strWidth(word)
			if lw > 0 && lw+1+ww > n {
				flush()
			}
			if ww > n { // longer than a whole line: hard-break it
				var part strings.Builder
				pw := 0
				for _, r := range word {
					_, rw := dispRune(r)
					if pw+rw > n && pw > 0 {
						out = append(out, part.String())
						part.Reset()
						pw = 0
					}
					part.WriteRune(r)
					pw += rw
				}
				word, ww = part.String(), pw
			}
			if lw > 0 {
				line.WriteByte(' ')
				lw++
			}
			line.WriteString(word)
			lw += ww
		}
		if lw > 0 || line.Len() > 0 {
			flush()
		}
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}
