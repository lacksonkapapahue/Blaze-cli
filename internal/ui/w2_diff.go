package ui

// Word-level highlight for diffs: a removed line followed by its added
// replacement is compared word by word, and the words that changed are drawn
// in bold underline (accent on the added side) instead of a new colour.

import (
	"strings"
	"unicode"
)

type wtok struct {
	text    string
	changed bool
}

// wordToks splits a line into words, runs of spaces and single punctuation.
func wordToks(s string) []string {
	var out []string
	r := []rune(s)
	for i := 0; i < len(r); {
		j := i + 1
		switch {
		case isIdentRune(r[i]):
			for j < len(r) && isIdentRune(r[j]) {
				j++
			}
		case unicode.IsSpace(r[i]):
			for j < len(r) && unicode.IsSpace(r[j]) {
				j++
			}
		}
		out = append(out, string(r[i:j]))
		i = j
	}
	return out
}

// wordDiff marks the tokens of a and b that are not in their longest common
// subsequence. It returns nil, nil when the lines share too little for a
// word highlight to help (the whole line changed).
func wordDiff(a, b string) ([]wtok, []wtok) {
	A, B := wordToks(a), wordToks(b)
	if len(A)*len(B) > 40000 || len(A) == 0 || len(B) == 0 {
		return nil, nil
	}
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
	ta, tb := make([]wtok, len(A)), make([]wtok, len(B))
	for i := range A {
		ta[i] = wtok{A[i], true}
	}
	for j := range B {
		tb[j] = wtok{B[j], true}
	}
	common := 0
	for i, j := 0, 0; i < len(A) && j < len(B); {
		switch {
		case A[i] == B[j]:
			ta[i].changed, tb[j].changed = false, false
			if strings.TrimSpace(A[i]) != "" {
				common++
			}
			i++
			j++
		case L[i+1][j] >= L[i][j+1]:
			i++
		default:
			j++
		}
	}
	nonSpace := func(ts []string) int {
		n := 0
		for _, t := range ts {
			if strings.TrimSpace(t) != "" {
				n++
			}
		}
		return n
	}
	most := max(nonSpace(A), nonSpace(B))
	if common == 0 || common*10 < most*3 {
		return nil, nil
	}
	// spaces never carry the highlight on their own
	for _, ts := range [][]wtok{ta, tb} {
		for k := range ts {
			if strings.TrimSpace(ts[k].text) == "" {
				ts[k].changed = false
			}
		}
	}
	return ta, tb
}

// pairWordMarks pairs each run of removed lines with the added run right
// after it and returns the word marks per diff index.
func pairWordMarks(d []dline) map[int][]wtok {
	marks := map[int][]wtok{}
	for i := 0; i < len(d); {
		if d[i].op != '-' {
			i++
			continue
		}
		j := i
		for j < len(d) && d[j].op == '-' {
			j++
		}
		k := j
		for k < len(d) && d[k].op == '+' {
			k++
		}
		for p := 0; p < min(j-i, k-j); p++ {
			a := strings.ReplaceAll(d[i+p].text, "\t", "  ")
			b := strings.ReplaceAll(d[j+p].text, "\t", "  ")
			if ta, tb := wordDiff(a, b); ta != nil {
				marks[i+p], marks[j+p] = ta, tb
			}
		}
		i = k
	}
	return marks
}

// wordMarked renders txt clipped to room columns, with changed words in bold
// underline drawn in hi; base is the line's own colour. It never resets, so a
// background tint already set stays on.
func wordMarked(txt string, toks []wtok, room int, hi, base string) string {
	if toks == nil || !color {
		return clip(txt, room)
	}
	var b strings.Builder
	w := 0
	for _, t := range toks {
		tw := strWidth(t.text)
		s := t.text
		cut := false
		if w+tw > room {
			s = clipCols(t.text, max(1, room-w))
			cut = true
		}
		if t.changed {
			b.WriteString("\x1b[1;4m" + fg(hi) + s + "\x1b[22;24m" + fg(base))
		} else {
			b.WriteString(s)
		}
		w += tw
		if cut {
			break
		}
	}
	return b.String()
}
