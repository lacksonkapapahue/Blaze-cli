package ui

import (
	"regexp"
	"strings"
)

// MD renders streamed Markdown line by line in Blaze's style: headings in the
// accent, bullets as hairline dots, numbered lists as 01/02, code in a ruled block.
type MD struct {
	buf    strings.Builder
	inCode bool
	any    bool
	// Pre runs before a batch of finished lines is printed (stop the spinner),
	// Post after it while more text is still streaming (start it again).
	Pre, Post func()
}

var (
	reBold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reCode = regexp.MustCompile("`([^`]+)`")
	reNum  = regexp.MustCompile(`^(\s*)(\d+)[.)]\s+(.*)$`)
	reLink = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)]+)\)`)
)

func inline(s string) string {
	s = reLink.ReplaceAllString(s, "$1 "+"\x00$2\x01")
	s = reCode.ReplaceAllStringFunc(s, func(m string) string { return Accent(m[1:len(m)-1]) + fg(pal.body) })
	s = reBold.ReplaceAllStringFunc(s, func(m string) string { return Bold(m[2:len(m)-2]) + fg(pal.body) })
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\x00", fg(pal.weak)+"("), "\x01", ")"+fg(pal.body))
	return fg(pal.body) + s + reset()
}

func (m *MD) line(l string) {
	t := strings.TrimSpace(l)
	if strings.HasPrefix(t, "```") {
		if !m.inCode {
			lang := strings.TrimPrefix(t, "```")
			Println("  " + hair("┌─ ") + Label(nonEmpty(lang, "code")))
		} else {
			Println("  " + hair("└─"))
		}
		m.inCode = !m.inCode
		return
	}
	if m.inCode {
		Println("  " + hair("│ ") + Ink(l))
		return
	}
	switch {
	case t == "":
		Println("")
	case strings.HasPrefix(t, "#"):
		h := strings.TrimSpace(strings.TrimLeft(t, "#"))
		Println("  " + Accent("■ ") + Bold(h))
	case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "• "):
		indent := strings.Repeat("  ", (len(l)-len(strings.TrimLeft(l, " ")))/2)
		Println("  " + indent + Weak("·") + " " + inline(t[2:]))
	case reNum.MatchString(l):
		g := reNum.FindStringSubmatch(l)
		n := g[2]
		if len(n) == 1 {
			n = "0" + n
		}
		Println("  " + g[1] + Weak(n) + "  " + inline(g[3]))
	case strings.HasPrefix(t, "> "):
		Println("  " + Accent("│ ") + Sub(t[2:]))
	case t == "---" || t == "***":
		Println("  " + hair(strings.Repeat("─", 48)))
	default:
		Println("  " + inline(l))
	}
}

// Write takes a streamed chunk; complete lines are rendered right away.
func (m *MD) Write(s string) {
	m.any = true
	m.buf.WriteString(s)
	printed := false
	defer func() {
		if printed && m.Post != nil {
			m.Post()
		}
	}()
	for {
		b := m.buf.String()
		i := strings.IndexByte(b, '\n')
		if i < 0 {
			return
		}
		if !printed && m.Pre != nil {
			m.Pre()
		}
		printed = true
		m.line(strings.TrimRight(b[:i], "\r"))
		m.buf.Reset()
		m.buf.WriteString(b[i+1:])
	}
}

// Flush renders whatever is left (the last line has no newline).
func (m *MD) Flush() {
	if m.Pre != nil && (m.buf.Len() > 0 || m.inCode) {
		m.Pre()
	}
	if rest := m.buf.String(); rest != "" {
		m.line(rest)
		m.buf.Reset()
	}
	if m.inCode {
		Println("  " + hair("└─"))
		m.inCode = false
	}
}

func (m *MD) Started() bool { return m.any }

func nonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	return a
}
