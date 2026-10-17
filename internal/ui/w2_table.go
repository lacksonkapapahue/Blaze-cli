package ui

// Markdown tables as square hairline grids: header row in spaced Label caps,
// columns fitted to their content and shrunk to the terminal width.

import (
	"regexp"
	"strings"
)

var reTableSep = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?$`)

// isTableRow reports whether a trimmed line looks like a | row |.
func isTableRow(t string) bool {
	return strings.HasPrefix(t, "|") && strings.Count(t, "|") >= 2
}

// splitRow splits "| a | b |" into trimmed cells, honouring \| escapes and
// pipes inside `code`.
func splitRow(t string) []string {
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "|")
	if strings.HasSuffix(t, "|") && !strings.HasSuffix(t, `\|`) {
		t = t[:len(t)-1]
	}
	var cells []string
	var cur strings.Builder
	code := false
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case c == '\\' && i+1 < len(t) && t[i+1] == '|':
			cur.WriteByte('|')
			i++
		case c == '`':
			code = !code
			cur.WriteByte(c)
		case c == '|' && !code:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

// spaced is the plain text of Label(s): upper case, letter-spaced.
func spacedCaps(s string) string {
	r := []rune(strings.ToUpper(s))
	parts := make([]string, len(r))
	for i, c := range r {
		parts[i] = string(c)
	}
	return strings.Join(parts, " ")
}

// stripMD removes inline markdown markers for plain measurements and labels.
func stripMD(s string) string {
	s = reLink.ReplaceAllString(s, "$1")
	s = reBold.ReplaceAllString(s, "$1")
	s = reCode.ReplaceAllString(s, "$1")
	return s
}

// padTo pads a styled string with spaces to w columns, aligned per a.
func padCell(s string, w int, a byte) string {
	n := w - escWidth(s)
	if n <= 0 {
		return s
	}
	switch a {
	case 'r':
		return strings.Repeat(" ", n) + s
	case 'c':
		return strings.Repeat(" ", n/2) + s + strings.Repeat(" ", n-n/2)
	}
	return s + strings.Repeat(" ", n)
}

// renderTable turns buffered | rows | into styled lines that fit in maxW
// columns (including the two-space indent). Without a separator row the
// lines are not a table and render as plain text.
func renderTable(rows []string, maxW int) []string {
	if len(rows) < 2 || !reTableSep.MatchString(strings.TrimSpace(rows[1])) {
		var out []string
		for _, r := range rows {
			out = append(out, "  "+inline(r))
		}
		return out
	}
	head := splitRow(rows[0])
	aligns := splitRow(rows[1])
	var body [][]string
	for _, r := range rows[2:] {
		body = append(body, splitRow(r))
	}
	n := len(head)
	for _, b := range body {
		n = max(n, len(b))
	}
	al := make([]byte, n)
	for i := range al {
		al[i] = 'l'
		if i < len(aligns) {
			a := aligns[i]
			l, r := strings.HasPrefix(a, ":"), strings.HasSuffix(a, ":")
			switch {
			case l && r:
				al[i] = 'c'
			case r:
				al[i] = 'r'
			}
		}
	}
	cell := func(row []string, i int) string {
		if i < len(row) {
			return row[i]
		}
		return ""
	}
	// natural widths: the header as a spaced label, cells as plain text
	w := make([]int, n)
	for i := 0; i < n; i++ {
		w[i] = max(1, strWidth(spacedCaps(stripMD(cell(head, i)))))
		for _, b := range body {
			w[i] = max(w[i], strWidth(stripMD(cell(b, i))))
		}
	}
	total := func() int {
		t := 2 + 1 // indent + left border
		for _, x := range w {
			t += x + 3
		}
		return t
	}
	// shrink the widest column until the grid fits
	for total() > maxW {
		wi := 0
		for i := range w {
			if w[i] > w[wi] {
				wi = i
			}
		}
		if w[wi] <= 3 {
			break
		}
		w[wi]--
	}
	if total() > maxW {
		// too narrow for a grid: one row per line, cells joined by ·
		var out []string
		for _, r := range append([][]string{head}, body...) {
			var parts []string
			for _, c := range r {
				parts = append(parts, stripMD(c))
			}
			out = append(out, "  "+clip(strings.Join(parts, " · "), max(1, maxW-2)))
		}
		return out
	}
	rule := func(l, m, r string) string {
		parts := make([]string, n)
		for i := range w {
			parts[i] = strings.Repeat("─", w[i]+2)
		}
		return "  " + hair(l+strings.Join(parts, m)+r)
	}
	fit := func(c string, i int, headRow bool) string {
		if headRow {
			p := stripMD(c)
			if strWidth(spacedCaps(p)) <= w[i] {
				return Label(p)
			}
			return Sub(clipCols(strings.ToUpper(p), w[i]))
		}
		if strWidth(stripMD(c)) <= w[i] {
			return inline(c)
		}
		return Body(clipCols(stripMD(c), w[i]))
	}
	line := func(r []string, headRow bool) string {
		var b strings.Builder
		b.WriteString("  " + hair("│"))
		for i := 0; i < n; i++ {
			b.WriteString(" " + padCell(fit(cell(r, i), i, headRow), w[i], al[i]) + " " + hair("│"))
		}
		return b.String()
	}
	out := []string{rule("┌", "┬", "┐"), line(head, true), rule("├", "┼", "┤")}
	for _, b := range body {
		out = append(out, line(b, false))
	}
	return append(out, rule("└", "┴", "┘"))
}

// clipW clips plain text to n display columns with an ellipsis.
func clipCols(s string, n int) string {
	if strWidth(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		_, rw := dispRune(r)
		if w+rw > n-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}
