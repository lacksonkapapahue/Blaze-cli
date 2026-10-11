package ui

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// ---------- choosers ----------

// rawKeys runs fn for each key in raw mode until fn returns true.
// Keys are reported as names: "up", "down", "left", "right", "enter", "space",
// "tab", "esc", "ctrl-c", or the typed character.
func rawKeys(fn func(key string) bool) bool {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return false
	}
	defer term.Restore(fd, old)
	for {
		r, _, err := In.ReadRune()
		if err != nil {
			fn("ctrl-c")
			return true
		}
		k := string(r)
		switch r {
		case '\r', '\n':
			k = "enter"
		case ' ':
			k = "space"
		case '\t':
			k = "tab"
		case 3, 4:
			k = "ctrl-c"
		case 16:
			k = "up"
		case 14:
			k = "down"
		case 27:
			k = "esc"
			if In.Buffered() > 0 {
				n, _, _ := In.ReadRune()
				if n == '[' || n == 'O' {
					seq := ""
					for {
						c, _, err := In.ReadRune()
						if err != nil {
							break
						}
						seq += string(c)
						if c >= 0x40 && c <= 0x7e {
							break
						}
					}
					k = map[string]string{"A": "up", "B": "down", "C": "right", "D": "left"}[seq]
				}
			}
		}
		if k != "" && fn(k) {
			return true
		}
	}
}

// Choose shows options inline ("  ? ▸ Yes   No") and returns the picked index.
// ← → or Tab move, Enter picks, the first letter of an option picks it, Esc picks cancel.
func Choose(prefix string, options []string, def, cancel int) int {
	r := cancel
	withModal(func() { r = choose(prefix, options, def, cancel) })
	return r
}

func choose(prefix string, options []string, def, cancel int) int {
	if !StdinTTY() || !IsTTY() {
		ans, err := ReadLine(prefix + Weak("["+strings.Join(options, "/")+"] "))
		if err != nil {
			return cancel
		}
		a := strings.ToLower(strings.TrimSpace(ans))
		if a == "" {
			return def
		}
		for i, o := range options {
			if strings.HasPrefix(strings.ToLower(o), a[:1]) {
				return i
			}
		}
		return cancel
	}
	sel := def
	draw := func() {
		var parts []string
		for i, o := range options {
			if i == sel {
				parts = append(parts, Accent("▸ ")+Bold(o))
			} else {
				parts = append(parts, "  "+Weak(o))
			}
		}
		Printf("\r\x1b[2K%s%s  %s", prefix, strings.Join(parts, "  "), Weak("← → enter"))
	}
	Printf("\x1b[?25l")
	draw()
	rawKeys(func(k string) bool {
		switch k {
		case "left", "up":
			sel = (sel + len(options) - 1) % len(options)
		case "right", "down", "tab":
			sel = (sel + 1) % len(options)
		case "enter", "space":
			return true
		case "esc", "ctrl-c":
			sel = cancel
			return true
		default:
			for i, o := range options {
				if strings.EqualFold(k, o[:1]) {
					sel = i
					return true
				}
			}
		}
		draw()
		return false
	})
	// settle on the chosen answer only
	Printf("\r\x1b[2K%s%s\r\n\x1b[?25h", prefix, Accent("■ ")+Ink(options[sel]))
	return sel
}

// Checklist lets the user tick rows with ↑ ↓ and Space, Enter to confirm.
// It returns the indexes that are ticked.
func Checklist(rows []string, ticked []bool) []int {
	var r []int
	withModal(func() { r = checklist(rows, ticked) })
	return r
}

func checklist(rows []string, ticked []bool) []int {
	on := append([]bool(nil), ticked...)
	for len(on) < len(rows) {
		on = append(on, false)
	}
	if !StdinTTY() || !IsTTY() {
		Numbered(rows)
		ans, _ := ReadLine("  " + Ink("Which ones? ") + Weak("numbers like 1,2 · Enter = ticked "))
		ans = strings.TrimSpace(ans)
		if ans != "" {
			for i := range on {
				on[i] = false
			}
			for _, f := range strings.FieldsFunc(ans, func(r rune) bool { return r == ',' || r == ' ' }) {
				var n int
				if _, err := fmt.Sscan(f, &n); err == nil && n >= 1 && n <= len(rows) {
					on[n-1] = true
				}
			}
		}
		return picked(on)
	}
	cur := 0
	W := termWidth()
	draw := func(first bool) {
		if !first {
			Printf("\x1b[%dA", len(rows)+1)
		}
		for i, r := range rows {
			box := Weak("□")
			if on[i] {
				box = Accent("■")
			}
			ptr := "  "
			if i == cur {
				ptr = Accent("▸ ")
			}
			line := "  " + ptr + box + "  " + Weak(fmt.Sprintf("%02d", i+1)) + "  " + r
			if width(line) > W-1 {
				line = "  " + ptr + box + "  " + Weak(fmt.Sprintf("%02d", i+1)) + "  " + clip(ansi.ReplaceAllString(r, ""), W-12)
			}
			Printf("\r\x1b[2K%s\r\n", line)
		}
		Printf("\r\x1b[2K  %s\r\n", Weak("↑ ↓ move · space tick · enter done"))
	}
	Printf("\x1b[?25l")
	draw(true)
	rawKeys(func(k string) bool {
		switch k {
		case "up", "k":
			cur = (cur + len(rows) - 1) % len(rows)
		case "down", "j", "tab":
			cur = (cur + 1) % len(rows)
		case "space", "x":
			on[cur] = !on[cur]
		case "enter":
			return true
		case "esc", "ctrl-c":
			for i := range on {
				on[i] = false
			}
			return true
		default:
			var n int
			if _, err := fmt.Sscan(k, &n); err == nil && n >= 1 && n <= len(rows) {
				cur = n - 1
				on[cur] = !on[cur]
			}
		}
		draw(false)
		return false
	})
	draw(false)
	Printf("\x1b[1A\r\x1b[2K\x1b[?25h")
	return picked(on)
}

func picked(on []bool) []int {
	var out []int
	for i, v := range on {
		if v {
			out = append(out, i)
		}
	}
	return out
}
