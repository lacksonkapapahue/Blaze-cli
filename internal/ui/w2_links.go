package ui

// OSC 8 hyperlinks: markdown links, bare http(s) URLs and file:line paths
// become clickable in terminals that support it (others just show the text).
// Off with BLAZE_NO_LINKS=1 or when colour is off.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ansiAll matches SGR colour codes and OSC 8 hyperlink open/close sequences.
// ui.go's `ansi` only knows SGR; visW uses this so links never count as width.
var ansiAll = regexp.MustCompile(`\x1b\[[0-9;]*m|\x1b\]8;[^\x07\x1b]*(?:\x1b\\|\x07)`)

// stripAll removes every styling and hyperlink escape.
func stripEsc(s string) string { return ansiAll.ReplaceAllString(s, "") }

// visW is the display width of a styled string (wide runes count 2).
func escWidth(s string) int { return strWidth(stripEsc(s)) }

func linksOn() bool { return color && os.Getenv("BLAZE_NO_LINKS") == "" }

// osc8 wraps text in a hyperlink to url.
func osc8(url, text string) string {
	if !linksOn() || url == "" {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

func ulText(s string) string {
	if !color {
		return s
	}
	return "\x1b[4m" + s + "\x1b[24m"
}

var (
	reURL = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+`)
	// a path with an extension followed by :line (and maybe :col)
	rePathLine = regexp.MustCompile(`(?:~|\.{1,2})?/?(?:[\w@.+-]+/)*[\w@+-][\w@.+-]*\.[A-Za-z0-9]{1,10}:\d+(?::\d+)?`)
)

// trimURL drops trailing punctuation that belongs to the sentence, keeping a
// closing paren when the URL itself opened one.
func trimURL(u string) string {
	for len(u) > 0 {
		c := u[len(u)-1]
		if strings.IndexByte(".,;:!?'\"", c) >= 0 {
			u = u[:len(u)-1]
			continue
		}
		if c == ')' && strings.Count(u, "(") < strings.Count(u, ")") {
			u = u[:len(u)-1]
			continue
		}
		break
	}
	return u
}

// fileURL turns a path (relative to the working directory) into a file://
// URL, or "" when the file does not exist.
func fileURL(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[2:])
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
		return ""
	}
	s := filepath.ToSlash(abs)
	if !strings.HasPrefix(s, "/") {
		s = "/" + s // C:/x → /C:/x
	}
	return "file://" + strings.ReplaceAll(s, " ", "%20")
}

// pathTarget splits "a/b.go:12:3" into its file part and returns its URL.
func pathTarget(m string) string {
	file := m
	for k := 0; k < 2; k++ {
		if i := strings.LastIndexByte(file, ':'); i > 0 && digitsOnly(file[i+1:]) {
			file = file[:i]
		}
	}
	return fileURL(file)
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// linkify styles bare URLs and file:line paths inside plain text. style is
// applied to ordinary text, link to the visible text of a link.
func linkify(s string, style, link func(string) string) string {
	if !linksOn() {
		return style(s)
	}
	type span struct {
		a, b int
		url  string
	}
	var spans []span
	for _, ix := range reURL.FindAllStringIndex(s, -1) {
		u := trimURL(s[ix[0]:ix[1]])
		spans = append(spans, span{ix[0], ix[0] + len(u), u})
	}
	for _, ix := range rePathLine.FindAllStringIndex(s, -1) {
		overlap := false
		for _, sp := range spans {
			if ix[0] < sp.b && ix[1] > sp.a {
				overlap = true
			}
		}
		if overlap || ix[0] > 0 && (s[ix[0]-1] == ':' || wordByte(s[ix[0]-1])) {
			continue
		}
		if u := pathTarget(s[ix[0]:ix[1]]); u != "" {
			spans = append(spans, span{ix[0], ix[1], u})
		}
	}
	if len(spans) == 0 {
		return style(s)
	}
	// order by start
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j].a < spans[j-1].a; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
	var b strings.Builder
	at := 0
	for _, sp := range spans {
		if at < sp.a {
			b.WriteString(style(s[at:sp.a]))
		}
		b.WriteString(osc8(sp.url, link(s[sp.a:sp.b])))
		at = sp.b
	}
	if at < len(s) {
		b.WriteString(style(s[at:]))
	}
	return b.String()
}

func wordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
