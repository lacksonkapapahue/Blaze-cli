// Package refs expands @ context references in a prompt (Hermes style):
//
//	@diff          the git working tree diff
//	@staged        the git staged diff
//	@file:PATH     a file
//	@folder:PATH   a folder: a tree listing plus its small text files
//	@git:N         the last N commits with their patches
//	@url:URL       a web page as readable text (http and https only)
//	@image:PATH    an image, sent to the model as an image part
//	@PATH          a plain path (a file, a folder, or an image), as before
//
// Each reference becomes a delimited block appended to the prompt,
// <context ref="@diff">…</context>, with a cap per reference and one for the
// whole message. A reference that fails leaves a short note in its block and in
// the receipt instead of stopping the message.
package refs

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Defaults for Options fields left zero.
const (
	DefaultPerRef    = 48 * 1024  // bytes of one reference's block
	DefaultTotal     = 160 * 1024 // bytes of all blocks together
	DefaultTimeout   = 10 * time.Second
	DefaultMaxImage  = 4 * 1024 * 1024 // bytes of one image as sent (after downscaling)
	DefaultMaxImages = 4
	maxGitCommits    = 20
)

// Options tune Expand.
type Options struct {
	Cwd       string        // folder relative paths and git run in ("" = the process's)
	PerRef    int           // byte cap of one block
	Total     int           // byte cap of all blocks
	Timeout   time.Duration // per fetch / git command
	MaxImage  int           // byte cap of one image as sent
	MaxImages int           // images per message
	Client    *http.Client  // for @url (nil = a client with Timeout)
}

func (o Options) withDefaults() Options {
	if o.PerRef <= 0 {
		o.PerRef = DefaultPerRef
	}
	if o.Total <= 0 {
		o.Total = DefaultTotal
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxImage <= 0 {
		o.MaxImage = DefaultMaxImage
	}
	if o.MaxImages <= 0 {
		o.MaxImages = DefaultMaxImages
	}
	if o.Cwd == "" {
		o.Cwd, _ = os.Getwd()
	}
	return o
}

// Kind is a reference type.
type Kind string

const (
	Diff   Kind = "diff"
	Staged Kind = "staged"
	File   Kind = "file"
	Folder Kind = "folder"
	Git    Kind = "git"
	URL    Kind = "url"
	Image  Kind = "image"
	Path   Kind = "path" // a plain @path: file, folder or image, decided when expanded
)

// Ref is one reference found in a line.
type Ref struct {
	Kind  Kind
	Arg   string // the path, URL or commit count ("" for diff / staged)
	Token string // the reference exactly as typed
	Start int    // byte offset of Token in the line
}

// Types lists the reference types for the "@" menu, in menu order.
var Types = []struct{ Name, Desc string }{
	{"@diff", "Git working tree diff"},
	{"@staged", "Git staged diff"},
	{"@file:", "Attach a file"},
	{"@folder:", "Attach a folder"},
	{"@git:", "Git log with diffs (@git:5 = last 5 commits)"},
	{"@url:", "Fetch web content"},
	{"@image:", "Attach an image (png · jpg · gif)"},
}

// ImageExt reports whether p names an image Blaze can attach.
func ImageExt(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif":
		return true
	}
	return false
}

// Quote is how a path goes into a reference: bare when it has no spaces or
// quotes, else in double quotes ("@image:" + Quote(p)).
func Quote(p string) string {
	if p != "" && !strings.ContainsAny(p, " \t\n\"'") {
		return p
	}
	return strconv.Quote(p)
}

// Find returns the references in line, in order. A word counts when it starts
// with "@" at the start of the line or after whitespace. Plain @words that are
// not a known type are kept as Path refs; Expand ignores the ones that are not
// paths that exist (so "@someone" stays text).
func Find(line string) []Ref {
	var out []Ref
	for i := 0; i < len(line); {
		if line[i] != '@' || (i > 0 && !isSpace(line[i-1])) {
			i++
			continue
		}
		start := i
		body, end := word(line, i+1)
		i = end
		body = trimPunct(body)
		if body == "" {
			continue
		}
		r := Ref{Token: "@" + body, Start: start}
		name, arg, colon := strings.Cut(body, ":")
		switch k := Kind(strings.ToLower(name)); {
		case !colon && (k == Diff || k == Staged):
			r.Kind = k
		case !colon && k == Git:
			r.Kind, r.Arg = Git, "1"
		case colon && (k == File || k == Folder || k == Image || k == Git):
			r.Kind, r.Arg = k, unquote(arg)
		case colon && k == URL:
			r.Kind, r.Arg = URL, unquote(arg)
		case strings.HasPrefix(body, "http://") || strings.HasPrefix(body, "https://"):
			r.Kind, r.Arg = URL, body
		default:
			r.Kind, r.Arg = Path, unquote(body)
		}
		if r.Kind != Diff && r.Kind != Staged && r.Arg == "" {
			continue
		}
		out = append(out, r)
	}
	return out
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// word reads a reference body from line[i:]: up to whitespace, with a
// double-quoted part ("my file.txt") allowed to hold spaces.
func word(line string, i int) (string, int) {
	j := i
	for j < len(line) && !isSpace(line[j]) {
		if line[j] == '"' {
			k := j + 1
			for k < len(line) && line[k] != '"' {
				if line[k] == '\\' {
					k++
				}
				k++
			}
			if k < len(line) {
				j = k + 1
				continue
			}
		}
		j++
	}
	return line[i:j], j
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
		return s[1 : len(s)-1]
	}
	return s
}

// trimPunct drops sentence punctuation after a reference ("@diff," "@a.go.").
func trimPunct(s string) string {
	t := strings.TrimRight(s, ",.;)!?'")
	if strings.HasSuffix(t, ":") && !strings.Contains(t[:len(t)-1], ":") {
		return t // "@file:" typed so far
	}
	return strings.TrimRight(t, ",.;:)!?'")
}

// Receipt is one reference's line in the receipt under the message.
type Receipt struct {
	Ref    string // short name: "@diff", "@url example.com", "@main.go"
	Detail string // "214 lines", "3K", "not a git repository"
	OK     bool
}

func (r Receipt) String() string {
	if r.Detail == "" {
		return r.Ref
	}
	return r.Ref + " " + r.Detail
}

// Result is an expanded line.
type Result struct {
	Text     string    // the prompt: the line (image refs as [image N]) plus the context blocks
	Images   []string  // data URLs of attached images, in order
	Receipts []Receipt // one per reference that expanded or failed
}

// ReceiptLine is the receipt as plain text: "@diff 214 lines · @url example.com 3K".
func (r Result) ReceiptLine() string {
	parts := make([]string, len(r.Receipts))
	for i, x := range r.Receipts {
		parts[i] = x.String()
	}
	return strings.Join(parts, " · ")
}

// Has reports whether line holds a reference that Expand would act on without
// touching the disk or network beyond a stat (used to decide on a spinner).
func Has(line string) bool { return strings.Contains(line, "@") && len(Find(line)) > 0 }

// Slow reports whether line holds a reference that may take a moment (git, url).
func Slow(line string) bool {
	for _, r := range Find(line) {
		switch r.Kind {
		case Diff, Staged, Git, URL:
			return true
		}
	}
	return false
}

// Expand expands the references in line. It never fails: a reference that
// can't be read leaves a short note instead.
func Expand(ctx context.Context, line string, opt Options) Result {
	opt = opt.withDefaults()
	refs := Find(line)
	var res Result
	if len(refs) == 0 {
		res.Text = line
		return res
	}
	var blocks strings.Builder
	used := 0
	text := line
	shift := 0 // how far text has moved from line (image tokens replaced)
	seen := map[string]bool{}
	for _, r := range refs {
		key := string(r.Kind) + "\x00" + r.Arg
		kind := r.Kind
		path := ""
		if kind == Path || kind == File || kind == Folder || kind == Image {
			path = absPath(opt.Cwd, r.Arg)
			if kind == Path {
				st, err := os.Stat(path)
				switch {
				case err != nil:
					continue // not a path: "@someone" stays text
				case st.IsDir():
					kind = Folder
				case ImageExt(path):
					kind = Image
				default:
					kind = File
				}
			}
			if kind == File && ImageExt(path) {
				kind = Image
			}
		}
		if kind == Image {
			n := len(res.Images) + 1
			label := "[image " + strconv.Itoa(n) + "]"
			rec := Receipt{Ref: "@image " + filepath.Base(path), OK: true}
			if len(res.Images) >= opt.MaxImages {
				rec.Detail, rec.OK = fmt.Sprintf("skipped (%d images max)", opt.MaxImages), false
				res.Receipts = append(res.Receipts, rec)
				continue
			}
			url, size, err := LoadImage(path, opt.MaxImage)
			if err != nil {
				rec.Detail, rec.OK = err.Error(), false
				res.Receipts = append(res.Receipts, rec)
				fmt.Fprintf(&blocks, "\n\n<context ref=%q>(not attached: %s)</context>", r.Token, err.Error())
				continue
			}
			res.Images = append(res.Images, url)
			rec.Detail = HumanSize(int64(size))
			res.Receipts = append(res.Receipts, rec)
			// the token in the text becomes the image's label
			at := r.Start + shift
			text = text[:at] + label + text[at+len(r.Token):]
			shift += len(label) - len(r.Token)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		name, body, detail, err := expandOne(ctx, r, kind, path, opt)
		rec := Receipt{Ref: name, Detail: detail, OK: err == nil}
		if err != nil {
			rec.Detail = short(err.Error())
			fmt.Fprintf(&blocks, "\n\n<context ref=%q>(couldn't read: %s)</context>", r.Token, short(err.Error()))
			res.Receipts = append(res.Receipts, rec)
			continue
		}
		room := min(opt.PerRef, opt.Total-used)
		if room <= 200 {
			rec.Detail, rec.OK = "skipped (context cap reached)", false
			fmt.Fprintf(&blocks, "\n\n<context ref=%q>(left out: this message's context cap of %s is used up)</context>", r.Token, HumanSize(int64(opt.Total)))
			res.Receipts = append(res.Receipts, rec)
			continue
		}
		body, cut := capText(body, room)
		if cut != "" {
			rec.Detail += " (cut)"
			body += "\n" + cut
		}
		block := fmt.Sprintf("\n\n<context ref=%q>\n%s\n</context>", r.Token, strings.TrimRight(body, "\n"))
		used += len(block)
		blocks.WriteString(block)
		res.Receipts = append(res.Receipts, rec)
	}
	res.Text = text + blocks.String()
	return res
}

// expandOne reads one non-image reference: its receipt name, block body and
// receipt detail.
func expandOne(ctx context.Context, r Ref, kind Kind, path string, opt Options) (name, body, detail string, err error) {
	switch kind {
	case Diff, Staged:
		name = "@" + string(kind)
		args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color"}
		if kind == Staged {
			args = append(args, "--cached")
		}
		body, err = runGit(ctx, opt.Cwd, opt.Timeout, args...)
		if err != nil {
			return
		}
		if strings.TrimSpace(body) == "" {
			if kind == Staged {
				return name, "(nothing staged)", "nothing staged", nil
			}
			return name, "(no changes)", "clean", nil
		}
		return name, body, plural(lines(body), "line"), nil
	case Git:
		n, e := strconv.Atoi(strings.TrimSpace(r.Arg))
		if e != nil || n < 1 {
			return "@git", "", "", fmt.Errorf("@git takes a commit count, like @git:5")
		}
		n = min(n, maxGitCommits)
		name = "@git:" + strconv.Itoa(n)
		body, err = runGit(ctx, opt.Cwd, opt.Timeout, "log", "-n", strconv.Itoa(n), "-p", "--stat",
			"--no-ext-diff", "--no-textconv", "--no-color", "--date=short")
		if err != nil {
			return
		}
		if strings.TrimSpace(body) == "" {
			return name, "(no commits yet)", "no commits", nil
		}
		return name, body, plural(strings.Count("\n"+body, "\ncommit "), "commit") + " · " + plural(lines(body), "line"), nil
	case URL:
		u, title, text, e := fetchURL(ctx, r.Arg, opt)
		name = "@url " + u
		if e != nil {
			return name, "", "", e
		}
		if title != "" {
			text = "# " + title + "\n\n" + text
		}
		return name, "source: " + r.Arg + "\n\n" + text, HumanSize(int64(len(text))), nil
	case Folder:
		name = "@" + displayPath(opt.Cwd, path) + "/"
		body, detail, err = readFolder(path, opt.PerRef)
		return
	default: // File
		name = "@" + displayPath(opt.Cwd, path)
		body, detail, err = readFile(path)
		return
	}
}

// capText cuts s to at most n bytes on a line boundary (or a rune boundary)
// and returns the note to add when it did.
func capText(s string, n int) (string, string) {
	if len(s) <= n {
		return s, ""
	}
	total := lines(s)
	cut := s[:n-120]
	if i := strings.LastIndexByte(cut, '\n'); i > len(cut)/2 {
		cut = cut[:i]
	}
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, fmt.Sprintf("…(truncated: %d of %d lines shown, %s cap)", lines(cut), total, HumanSize(int64(n)))
}

func lines(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return strconv.Itoa(n) + " " + w + "s"
}

func short(s string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	s = strings.TrimPrefix(s, "fatal: ")
	if len(s) > 80 {
		s = s[:77] + "…"
	}
	return s
}

// absPath resolves p (~ and relative paths) against cwd.
func absPath(cwd, p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[1:])
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// displayPath is p relative to cwd when it is inside it.
func displayPath(cwd, p string) string {
	if rel, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return p
}

// HumanSize is a compact size like the @ menu shows: 512B, 3K, 13K, 1.2M.
func HumanSize(n int64) string {
	switch {
	case n < 1024:
		return strconv.FormatInt(n, 10) + "B"
	case n < 1024*1024:
		return strconv.FormatInt((n+512)/1024, 10) + "K"
	case n < 1024*1024*1024:
		return strconv.FormatFloat(float64(n)/(1024*1024), 'f', 1, 64) + "M"
	}
	return strconv.FormatFloat(float64(n)/(1024*1024*1024), 'f', 1, 64) + "G"
}
