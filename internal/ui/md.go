package ui

import (
	"regexp"
	"strings"
)

// MD renders streamed Markdown line by line in Blaze's style: headings in the
// accent, bullets as hairline dots, numbered lists as 01/02, code in a ruled,
// highlighted block, tables as hairline grids and callouts as accent-ruled
// blocks. Table rows are held until the table ends (or Flush).
type MD struct {
	buf    strings.Builder
	inCode bool
	any    bool
	// Pre runs before a batch of finished lines is printed (stop the spinner),
	// Post after it while more text is still streaming (start it again).
	Pre, Post func()
	// Out, when set, takes each rendered line instead of printing it.
	Out func(string)

	lang    *langSpec // the open code block's language
	hl      hlState
	table   []string // buffered | rows |
	call    string   // open callout kind
	callQ   bool     // the open callout is the "> [!KIND]" form
	batch   bool     // inside Write/Flush: Pre runs before the first emit
	printed bool
}

func (m *MD) emit(s string) {
	if m.batch && !m.printed {
		m.printed = true
		if m.Pre != nil {
			m.Pre()
		}
	}
	if m.Out != nil {
		m.Out(s)
		return
	}
	Println(s)
}

// RenderMD renders a whole Markdown text into styled lines (for Above).
func RenderMD(text string) []string {
	var out []string
	m := &MD{Out: func(s string) { out = append(out, s) }}
	m.Write(text)
	m.Flush()
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

var (
	reBold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reCode = regexp.MustCompile("`([^`]+)`")
	reNum  = regexp.MustCompile(`^(\s*)(\d+)[.)]\s+(.*)$`)
	reLink = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	reSpan = regexp.MustCompile("`[^`]+`|\\[([^\\]]+)\\]\\((https?://[^)\\s]+)\\)")
)

func inline(s string) string { return inlineIn(s, pal.body) }

// inlineIn renders inline markdown (code, bold, links, bare URLs and
// file:line paths) over a base colour.
func inlineIn(s, base string) string {
	b := fg(base)
	same := func(t string) string { return t }
	var out strings.Builder
	out.WriteString(b)
	text := func(t string) {
		at := 0
		for _, ix := range reBold.FindAllStringSubmatchIndex(t, -1) {
			out.WriteString(linkify(t[at:ix[0]], same, ulText))
			out.WriteString(bold() + fg(pal.ink) + linkify(t[ix[2]:ix[3]], same, ulText) + reset() + b)
			at = ix[1]
		}
		out.WriteString(linkify(t[at:], same, ulText))
	}
	at := 0
	for _, ix := range reSpan.FindAllStringSubmatchIndex(s, -1) {
		text(s[at:ix[0]])
		at = ix[1]
		if ix[2] < 0 { // `code`
			out.WriteString(fg(pal.accent) + linkify(s[ix[0]+1:ix[1]-1], same, ulText) + b)
			continue
		}
		label, url := s[ix[2]:ix[3]], s[ix[4]:ix[5]]
		if linksOn() {
			out.WriteString(osc8(url, fg(pal.accent)+ulText(label)) + b)
		} else {
			out.WriteString(label + " " + fg(pal.weak) + "(" + url + ")" + b)
		}
	}
	text(s[at:])
	out.WriteString(reset())
	return out.String()
}

func (m *MD) flushTable() {
	if len(m.table) == 0 {
		return
	}
	rows := m.table
	m.table = nil
	for _, l := range renderTable(rows, termWidth()-1) {
		m.emit(l)
	}
}

func (m *MD) line(l string) {
	t := strings.TrimSpace(l)
	if !m.inCode {
		if len(m.table) > 0 {
			if isTableRow(t) {
				m.table = append(m.table, t)
				return
			}
			m.flushTable()
		}
		if m.call != "" {
			switch {
			case m.callQ && strings.HasPrefix(t, ">"):
				if body := strings.TrimSpace(t[1:]); body != "" {
					m.emit(calloutRule() + inline(body))
				} else {
					m.emit("  " + Accent("│"))
				}
				return
			case !m.callQ && t != "" && !blockStart(t):
				m.emit(calloutRule() + inline(t))
				return
			}
			m.call = ""
		}
	}
	if strings.HasPrefix(t, "```") {
		if !m.inCode {
			lang := strings.TrimPrefix(t, "```")
			m.lang, m.hl = specFor(lang), hlState{}
			lab := strings.Fields(lang)
			m.emit("  " + hair("┌─ ") + Label(nonEmpty(strings.Join(lab[:min(1, len(lab))], ""), "code")))
		} else {
			m.emit("  " + hair("└─"))
		}
		m.inCode = !m.inCode
		return
	}
	if m.inCode {
		code := strings.ReplaceAll(l, "\t", "    ")
		m.emit("  " + hair("│ ") + codeHL(m.lang, &m.hl, code))
		return
	}
	if kind, rest, q, ok := calloutStart(t); ok {
		m.call, m.callQ = kind, q
		m.emit(calloutHead(kind))
		if rest != "" {
			m.emit(calloutRule() + inline(rest))
		}
		return
	}
	switch {
	case t == "":
		m.emit("")
	case isTableRow(t):
		m.table = append(m.table, t)
	case reHeading.MatchString(t):
		g := reHeading.FindStringSubmatch(t)
		for _, s := range mdHeading(len(g[1]), g[2]) {
			m.emit(s)
		}
	case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "• "):
		indent := strings.Repeat("  ", (len(l)-len(strings.TrimLeft(l, " ")))/2)
		_, after, _ := strings.Cut(t, " ")
		m.emit("  " + indent + Weak("·") + " " + inline(after))
	case reNum.MatchString(l):
		g := reNum.FindStringSubmatch(l)
		n := g[2]
		if len(n) == 1 {
			n = "0" + n
		}
		m.emit("  " + g[1] + Weak(n) + "  " + inline(g[3]))
	case t == ">" || strings.HasPrefix(t, "> "):
		// a plain quote gets a hairline rule; the accent rule is for callouts
		m.emit("  " + hair("│ ") + inlineIn(strings.TrimSpace(t[1:]), pal.sub))
	case t == "---" || t == "***":
		m.emit("  " + hair(strings.Repeat("─", min(48, termWidth()-4))))
	default:
		m.emit("  " + inline(l))
	}
}

// Write takes a streamed chunk; complete lines are rendered right away.
func (m *MD) Write(s string) {
	m.any = true
	m.buf.WriteString(s)
	m.batch, m.printed = true, false
	defer func() {
		m.batch = false
		if m.printed && m.Post != nil {
			m.Post()
		}
	}()
	for {
		b := m.buf.String()
		i := strings.IndexByte(b, '\n')
		if i < 0 {
			return
		}
		m.line(strings.TrimRight(b[:i], "\r"))
		m.buf.Reset()
		m.buf.WriteString(b[i+1:])
	}
}

// Flush renders whatever is left (the last line has no newline), any held
// table rows, and closes an open code block.
func (m *MD) Flush() {
	m.batch, m.printed = true, false
	defer func() { m.batch = false }()
	if rest := m.buf.String(); rest != "" {
		m.line(rest)
		m.buf.Reset()
	}
	m.flushTable()
	m.call = ""
	if m.inCode {
		m.emit("  " + hair("└─"))
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
