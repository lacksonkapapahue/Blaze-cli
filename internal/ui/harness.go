package ui

// Harness views: the task checklist and file diffs, in Blaze's palette.

import (
	"fmt"
	"strings"
)

// Todo row for the checklist card.
type TodoRow struct {
	Text   string
	Status string // done | doing | todo
}

// Todos draws the checklist the model keeps: ■ done, ▸ doing, □ to do.
func Todos(rows []TodoRow) {
	if len(rows) == 0 {
		return
	}
	done := 0
	for _, r := range rows {
		if r.Status == "done" {
			done++
		}
	}
	W := min(termWidth()-4, 88)
	var lines []string
	for _, r := range rows {
		t := clip(r.Text, W-8)
		switch r.Status {
		case "done":
			lines = append(lines, Accent("■")+" "+Weak(strike(t)))
		case "doing":
			lines = append(lines, Accent("▸")+" "+Bold(t))
		default:
			lines = append(lines, Weak("□")+" "+Body(t))
		}
	}
	Box(AccentLabel("plan")+" "+Weak(fmt.Sprintf("%d/%d", done, len(rows))), lines)
}

func strike(s string) string {
	if !color {
		return s
	}
	return "\x1b[9m" + s + "\x1b[29m"
}

// ---------- diffs ----------

type dline struct {
	op   byte // ' ', '-', '+'
	text string
	a, b int // line numbers (1-based) in before / after
}

func diffLines(a, b []string) []dline {
	n, m := len(a), len(b)
	// trim the common head and tail so the LCS table stays small
	pre := 0
	for pre < n && pre < m && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < n-pre && suf < m-pre && a[n-1-suf] == b[m-1-suf] {
		suf++
	}
	A, B := a[pre:n-suf], b[pre:m-suf]
	var out []dline
	for i := 0; i < pre; i++ {
		out = append(out, dline{' ', a[i], i + 1, i + 1})
	}
	if len(A)*len(B) > 250000 {
		for i, l := range A {
			out = append(out, dline{'-', l, pre + i + 1, 0})
		}
		for j, l := range B {
			out = append(out, dline{'+', l, 0, pre + j + 1})
		}
	} else {
		L := make([][]int, len(A)+1)
		for i := range L {
			L[i] = make([]int, len(B)+1)
		}
		for i := len(A) - 1; i >= 0; i-- {
			for j := len(B) - 1; j >= 0; j-- {
				if A[i] == B[j] {
					L[i][j] = L[i+1][j+1] + 1
				} else {
					L[i][j] = max(L[i+1][j], L[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < len(A) || j < len(B) {
			switch {
			case i < len(A) && j < len(B) && A[i] == B[j]:
				out = append(out, dline{' ', A[i], pre + i + 1, pre + j + 1})
				i++
				j++
			case i < len(A) && (j == len(B) || L[i+1][j] >= L[i][j+1]):
				out = append(out, dline{'-', A[i], pre + i + 1, 0})
				i++
			default:
				out = append(out, dline{'+', B[j], 0, pre + j + 1})
				j++
			}
		}
	}
	for k := 0; k < suf; k++ {
		out = append(out, dline{' ', a[n-suf+k], n - suf + k + 1, m - suf + k + 1})
	}
	return out
}

const red = "232;110;110"

// Diff shows a file change like a reviewer would: counts, then the changed
// lines with one line of context, added in the accent and removed in red.
func Diff(path, before, after string) {
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimRight(s, "\n"), "\n")
	}
	d := diffLines(split(before), split(after))
	add, del := 0, 0
	for _, l := range d {
		switch l.op {
		case '+':
			add++
		case '-':
			del++
		}
	}
	verb := "Updated"
	if before == "" {
		verb = "Created"
	}
	Println("  " + Accent("■") + " " + Ink(verb+" "+path) + "  " + Accent(fmt.Sprintf("+%d", add)) + " " + fg(red) + fmt.Sprintf("-%d", del) + reset())
	keep := make([]bool, len(d))
	for i, l := range d {
		if l.op != ' ' {
			for k := max(0, i-1); k <= min(len(d)-1, i+1); k++ {
				keep[k] = true
			}
		}
	}
	W := termWidth()
	shown, limit := 0, 14
	gap := false
	for i, l := range d {
		if !keep[i] {
			gap = true
			continue
		}
		if shown >= limit {
			rest := 0
			for _, x := range d[i:] {
				if x.op != ' ' {
					rest++
				}
			}
			if rest > 0 {
				Println("    " + Weak(fmt.Sprintf("… %d more changed lines", rest)))
			}
			break
		}
		if gap && shown > 0 {
			Println("    " + hair("┄"))
		}
		gap = false
		num := l.b
		if l.op == '-' {
			num = l.a
		}
		txt := clip(strings.ReplaceAll(l.text, "\t", "  "), W-14)
		ln := Weak(fmt.Sprintf("%4d ", num))
		switch l.op {
		case '+':
			Println("  " + ln + bgTint(pal.accent) + fg(pal.accent) + "+ " + fg(pal.ink) + txt + " " + reset())
		case '-':
			Println("  " + ln + bgTint(red) + fg(red) + "- " + fg(pal.sub) + txt + " " + reset())
		default:
			Println("  " + ln + Weak("  "+txt))
		}
		shown++
	}
}

// bgTint is a faint wash of a colour over the terminal background.
func bgTint(c string) string {
	if !color {
		return ""
	}
	base := "38;38;42"
	if pal == light {
		base = "252;252;252"
	}
	return "\x1b[48;2;" + mix(base, c, 0.16) + "m"
}
