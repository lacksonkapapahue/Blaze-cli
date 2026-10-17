package ui

// In-palette syntax highlighting for fenced code blocks. Only the palette's
// weights are used: keywords in the accent, strings in body, comments weak,
// numbers ink and everything else sub. A small hand-written tokenizer, one
// line at a time, with just enough state to carry block comments and
// multi-line strings across lines.

import (
	"strings"
	"unicode"
)

type langSpec struct {
	name       string
	kw         map[string]bool
	line       []string // line-comment prefixes
	open, shut string   // block comment delimiters
	quotes     string   // characters that open a string
	multi      string   // quote chars whose strings may span lines (`)
	triple     bool     // python ''' / """
	hashWord   bool     // '#' only starts a comment at a word boundary (shell)
	rustChar   bool     // ' is a char literal or a lifetime
	plain      bool     // no highlighting at all
	diff       bool     // +/- lines
}

// hlState is what carries from one line of a block to the next.
type hlState struct {
	inBlock bool
	inStr   string // the delimiter of an open multi-line string
}

func kwSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

var (
	kwGo = kwSet(`break case chan const continue default defer else fallthrough for func go goto if import
		interface map package range return select struct switch type var nil true false iota`)
	kwPy = kwSet(`False None True and as assert async await break class continue def del elif else except
		finally for from global if import in is lambda nonlocal not or pass raise return try while with yield match case self`)
	kwJS = kwSet(`async await break case catch class const continue debugger default delete do else enum export
		extends false finally for from function if implements import in instanceof interface let new null of
		private protected public return static super switch this throw true try type typeof undefined var void
		while with yield as readonly keyof declare namespace abstract`)
	kwSh = kwSet(`if then else elif fi case esac for while until do done in function return local export
		readonly declare unset set shift exit break continue source alias echo cd true false`)
	kwJSON = kwSet(`true false null`)
	kwRust = kwSet(`as async await break const continue crate dyn else enum extern false fn for if impl in let
		loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while
		Some None Ok Err`)
	kwC = kwSet(`auto break case char const continue default do double else enum extern float for goto if int
		long register return short signed sizeof static struct switch typedef union unsigned void volatile while
		class public private protected new delete this throw try catch namespace using template typename virtual
		bool true false null nullptr package import extends implements final abstract interface fun val var when
		override let func guard struct`)
	kwGeneric = kwSet(`if else elif for while do return break continue func function def fn class struct import
		from export const let var true false null nil None True False new try catch throw in and or not`)
)

func specFor(lang string) *langSpec {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if f := strings.Fields(lang); len(f) > 0 {
		lang = f[0]
	}
	lang = strings.TrimPrefix(lang, "{.")
	lang = strings.TrimSuffix(lang, "}")
	cLine, cOpen, cShut := []string{"//"}, "/*", "*/"
	switch lang {
	case "go", "golang":
		return &langSpec{name: "go", kw: kwGo, line: cLine, open: cOpen, shut: cShut, quotes: "\"'`", multi: "`"}
	case "py", "python", "python3":
		return &langSpec{name: "python", kw: kwPy, line: []string{"#"}, quotes: "\"'", triple: true}
	case "js", "javascript", "jsx", "mjs", "cjs", "ts", "typescript", "tsx":
		return &langSpec{name: "js", kw: kwJS, line: cLine, open: cOpen, shut: cShut, quotes: "\"'`", multi: "`"}
	case "sh", "bash", "zsh", "shell", "console", "ps1", "powershell", "fish":
		return &langSpec{name: "sh", kw: kwSh, line: []string{"#"}, quotes: "\"'", hashWord: true}
	case "json", "jsonc", "json5":
		return &langSpec{name: "json", kw: kwJSON, line: cLine, open: cOpen, shut: cShut, quotes: "\""}
	case "rs", "rust":
		return &langSpec{name: "rust", kw: kwRust, line: cLine, open: cOpen, shut: cShut, quotes: "\"'", rustChar: true}
	case "c", "h", "cpp", "c++", "cc", "hpp", "java", "cs", "csharp", "kotlin", "kt", "swift", "scala", "dart":
		return &langSpec{name: "c", kw: kwC, line: cLine, open: cOpen, shut: cShut, quotes: "\"'"}
	case "text", "txt", "plain", "plaintext", "output", "log", "md", "markdown":
		return &langSpec{name: "text", plain: true}
	case "diff", "patch":
		return &langSpec{name: "diff", diff: true}
	}
	return &langSpec{name: "generic", kw: kwGeneric, line: []string{"//", "#"}, open: cOpen, shut: cShut, quotes: "\"'`", hashWord: true}
}

// token classes
const (
	tkRest = iota
	tkKw
	tkStr
	tkCom
	tkNum
)

type codeTok struct {
	kind int
	text string
}

func isIdentStartRune(r rune) bool { return r == '_' || unicode.IsLetter(r) }
func isIdentRune(r rune) bool      { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func runesAt(r []rune, i int, s string) bool {
	if s == "" {
		return false
	}
	rs := []rune(s)
	if i+len(rs) > len(r) {
		return false
	}
	for k, c := range rs {
		if r[i+k] != c {
			return false
		}
	}
	return true
}

// codeTokens splits one line of code into classed tokens, updating st.
func codeTokens(sp *langSpec, st *hlState, line string) []codeTok {
	r := []rune(line)
	var out []codeTok
	add := func(k int, s string) {
		if s == "" {
			return
		}
		if n := len(out); n > 0 && out[n-1].kind == k {
			out[n-1].text += s
			return
		}
		out = append(out, codeTok{k, s})
	}
	if sp.plain {
		add(tkRest, line)
		return out
	}
	if sp.diff {
		t := strings.TrimLeft(line, " ")
		switch {
		case strings.HasPrefix(t, "+++") || strings.HasPrefix(t, "---") || strings.HasPrefix(t, "@@"):
			add(tkCom, line)
		case strings.HasPrefix(t, "+"):
			add(tkKw, line)
		case strings.HasPrefix(t, "-"):
			add(tkCom, line)
		default:
			add(tkRest, line)
		}
		return out
	}
	i := 0
	// readStr consumes a string body from i to its closing delimiter q.
	readStr := func(q string, escapes bool) (end int, closed bool) {
		j := i
		for j < len(r) {
			if escapes && r[j] == '\\' {
				j += 2
				continue
			}
			if runesAt(r, j, q) {
				return j + len([]rune(q)), true
			}
			j++
		}
		return len(r), false
	}
	for i < len(r) {
		if st.inBlock {
			j := i
			for j < len(r) && !runesAt(r, j, sp.shut) {
				j++
			}
			if j < len(r) {
				j += len([]rune(sp.shut))
				st.inBlock = false
			}
			add(tkCom, string(r[i:j]))
			i = j
			continue
		}
		if st.inStr != "" {
			end, closed := readStr(st.inStr, st.inStr != "`" || sp.name == "js")
			add(tkStr, string(r[i:end]))
			i = end
			if closed {
				st.inStr = ""
			}
			continue
		}
		c := r[i]
		// line comments
		lc := false
		for _, p := range sp.line {
			if runesAt(r, i, p) {
				if p == "#" && sp.hashWord && i > 0 && !unicode.IsSpace(r[i-1]) {
					continue
				}
				lc = true
				break
			}
		}
		if lc {
			add(tkCom, string(r[i:]))
			return out
		}
		if runesAt(r, i, sp.open) {
			st.inBlock = true
			j := i + len([]rune(sp.open))
			add(tkCom, sp.open)
			i = j
			continue
		}
		if sp.triple && (runesAt(r, i, `"""`) || runesAt(r, i, `'''`)) {
			q := string(r[i : i+3])
			start := i
			i += 3
			end, closed := readStr(q, true)
			add(tkStr, string(r[start:end]))
			i = end
			if !closed {
				st.inStr = q
			}
			continue
		}
		if strings.ContainsRune(sp.quotes, c) {
			if c == '\'' && sp.rustChar {
				// 'a' or '\n' is a char; 'a (no close soon) is a lifetime
				if !(i+2 < len(r) && (r[i+2] == '\'' || (r[i+1] == '\\' && i+3 < len(r)))) {
					j := i + 1
					for j < len(r) && isIdentRune(r[j]) {
						j++
					}
					add(tkRest, string(r[i:j]))
					i = j
					continue
				}
			}
			if c == '\'' && sp.name == "generic" && i > 0 && isIdentRune(r[i-1]) {
				// an apostrophe inside a word (don't) is just text
				add(tkRest, "'")
				i++
				continue
			}
			start := i
			i++
			q := string(c)
			end, closed := readStr(q, !(c == '\'' && sp.name == "sh") && !(c == '`' && sp.name == "go"))
			add(tkStr, string(r[start:end]))
			i = end
			if !closed && strings.ContainsRune(sp.multi, c) {
				st.inStr = q
			}
			continue
		}
		if unicode.IsDigit(c) && (i == 0 || !isIdentRune(r[i-1])) {
			j := i + 1
			for j < len(r) && (isIdentRune(r[j]) || r[j] == '.' && j+1 < len(r) && unicode.IsDigit(r[j+1])) {
				j++
			}
			add(tkNum, string(r[i:j]))
			i = j
			continue
		}
		if isIdentStartRune(c) {
			j := i + 1
			for j < len(r) && isIdentRune(r[j]) {
				j++
			}
			w := string(r[i:j])
			if sp.kw[w] && !(sp.name == "sh" && i > 0 && (r[i-1] == '-' || r[i-1] == '$' || r[i-1] == '/')) {
				add(tkKw, w)
			} else {
				add(tkRest, w)
			}
			i = j
			continue
		}
		add(tkRest, string(c))
		i++
	}
	return out
}

// codeHL renders one code line in the palette. Without colour it returns
// the line as is.
func codeHL(sp *langSpec, st *hlState, line string) string {
	toks := codeTokens(sp, st, line)
	if !color {
		return line
	}
	var b strings.Builder
	for _, t := range toks {
		var c string
		switch t.kind {
		case tkKw:
			c = pal.accent
		case tkStr:
			c = pal.body
		case tkCom:
			c = pal.weak
		case tkNum:
			c = pal.ink
		default:
			c = pal.sub
			if sp.plain {
				c = pal.body
			}
		}
		b.WriteString(fg(c) + t.text)
	}
	b.WriteString(reset())
	return b.String()
}
