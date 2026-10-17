package main

// The host side of @ context references and pasted images (v0.13, internal/refs
// and internal/clip). The "@" menu lists the reference types first (@diff,
// @staged, @file:, @folder:, @git:, @url:, @image:) and then this folder's files
// with their sizes; on send, expandRefs turns every reference into a <context>
// block, collects the images, and prints a one-line receipt under the message:
//
//	■ @diff 214 lines · @url example.com 3K · @image shot.png 230K

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/refs"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

// refsTimeout bounds expanding one message's references (git, web pages).
const refsTimeout = 30 * time.Second

var (
	refsMu        sync.Mutex
	pendingImages []string // the images of the message expandRefs last expanded
)

// expandRefs expands the @ references in line for the model: the line plus a
// <context ref="…"> block per reference, with image refs turned into [image N].
// It prints the receipt and keeps the images for takePendingImages. Lines with no
// reference come back unchanged.
func expandRefs(a *agent.Agent, line string) string {
	refsMu.Lock()
	pendingImages = nil
	refsMu.Unlock()
	if !refs.Has(line) {
		return line
	}
	ctx, cancel := context.WithTimeout(context.Background(), refsTimeout)
	defer cancel()
	var sp ui.Spinner
	if refs.Slow(line) {
		sp.Start("Reading @ references")
	}
	res := refs.Expand(ctx, line, refs.Options{Cwd: a.Env.Shell.Cwd()})
	sp.Stop()
	if r := ui.Receipt(receiptItems(res.Receipts)); r != "" {
		ui.Println(r)
	}
	refsMu.Lock()
	pendingImages = res.Images
	refsMu.Unlock()
	return res.Text
}

// takePendingImages returns (and forgets) the images expandRefs collected, for
// a.AskImages.
func takePendingImages() []string {
	refsMu.Lock()
	defer refsMu.Unlock()
	imgs := pendingImages
	pendingImages = nil
	return imgs
}

func receiptItems(rs []refs.Receipt) []ui.ReceiptItem {
	out := make([]ui.ReceiptItem, len(rs))
	for i, r := range rs {
		out[i] = ui.ReceiptItem{Text: r.String(), OK: r.OK}
	}
	return out
}

// withRefsMenu wraps a line menu (slashMenu) so an "@" word opens the reference
// menu instead of the plain files menu.
func withRefsMenu(base func(string) *ui.MenuSet) func(string) *ui.MenuSet {
	return func(line string) *ui.MenuSet {
		if m, ok := refsMenu(line); ok {
			return m
		}
		if base == nil {
			return nil
		}
		return base(line)
	}
}

// gitCounts are the @git: suggestions.
var gitCounts = []struct{ n, desc string }{
	{"1", "the last commit with its patch"},
	{"3", "the last 3 commits"},
	{"5", "the last 5 commits"},
	{"10", "the last 10 commits"},
}

// refsMenu is the menu for a line whose last word starts with "@". ok is false
// when the last word isn't an @ word (the base menu decides).
func refsMenu(line string) (*ui.MenuSet, bool) {
	i := lastWord(line)
	tok := line[i:]
	if !strings.HasPrefix(tok, "@") {
		return nil, false
	}
	before, frag := line[:i], tok[1:]
	name, arg, colon := strings.Cut(frag, ":")
	if colon {
		switch strings.ToLower(name) {
		case "file":
			return pathMenu(before, "@file:", arg, pathAll), true
		case "folder":
			return pathMenu(before, "@folder:", arg, pathDirs), true
		case "image":
			return pathMenu(before, "@image:", arg, pathImages), true
		case "git":
			var items []ui.MenuItem
			for _, g := range gitCounts {
				if strings.HasPrefix(g.n, arg) {
					items = append(items, ui.MenuItem{Name: "@git:" + g.n, Desc: g.desc, Insert: before + "@git:" + g.n + " ", DescAccent: true})
				}
			}
			return &ui.MenuSet{Title: "git log", Items: items, Full: len(gitCounts)}, true
		case "url":
			return nil, true // free text: no menu
		}
	}
	// "@" + a path being walked ("src/ma"): files only
	files := pathMenu(before, "@", frag, pathAll)
	if strings.ContainsAny(frag, `/\`) {
		return files, true
	}
	var types []ui.MenuItem
	for _, t := range refs.Types {
		it := ui.MenuItem{Name: t.Name, Desc: t.Desc, Insert: before + t.Name, Section: "references", DescAccent: true}
		if !strings.HasSuffix(t.Name, ":") {
			it.Insert += " "
		}
		types = append(types, it)
	}
	types = ui.Rank(types, frag, func(it ui.MenuItem) string { return strings.TrimPrefix(it.Name, "@") })
	var items []ui.MenuItem
	items = append(items, types...)
	full := len(refs.Types)
	if files != nil {
		for _, it := range files.Items {
			it.Section = "files"
			items = append(items, it)
		}
		full += files.Full
	}
	if len(items) == 0 {
		return nil, true
	}
	return &ui.MenuSet{Title: "context", Items: items, Full: full}, true
}

// lastWord is where line's last word starts; a space inside a double-quoted
// part (@file:"my dir/) doesn't end a word.
func lastWord(line string) int {
	start, in := 0, false
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '"':
			in = !in
		case !in && (c == ' ' || c == '\t' || c == '\n'):
			start = i + 1
		}
	}
	return start
}

type pathKind int

const (
	pathAll pathKind = iota
	pathDirs
	pathImages
)

const refsMenuMax = 500 // entries read from one folder

// pathMenu lists the folder named by frag ("src/ma" lists src/, filtered by "ma")
// as items turning the line into before + prefix + path: a folder ends in "/" so
// the menu moves into it, a file ends in " " so the menu closes. Sizes are
// compact (13K).
func pathMenu(before, prefix, frag string, kind pathKind) *ui.MenuSet {
	frag = strings.Trim(frag, `"`)
	i := strings.LastIndexAny(frag, `/\`)
	dir, base := frag[:i+1], frag[i+1:]
	look := dir
	if look == "" {
		look = "."
	}
	if strings.HasPrefix(look, "~") {
		if h, err := os.UserHomeDir(); err == nil {
			look = h + look[1:]
		}
	}
	ents, err := os.ReadDir(look)
	if err != nil {
		return nil
	}
	needsQuote := func(p string) bool { return strings.ContainsAny(p, " \t\"'") }
	quote := func(p string) string { // a finished path
		if needsQuote(p) {
			return `"` + p + `"`
		}
		return p
	}
	open := func(p string) string { // a folder being walked into: the quote stays open
		if needsQuote(p) {
			return `"` + p
		}
		return p
	}
	var dirs, files []ui.MenuItem
	for _, en := range ents {
		nm := en.Name()
		if nm == ".git" || (strings.HasPrefix(nm, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		info, err := os.Stat(filepath.Join(look, nm)) // follows symlinks
		if err != nil {
			continue
		}
		switch {
		case info.IsDir():
			dirs = append(dirs, ui.MenuItem{Name: nm + "/", Desc: "folder", Insert: before + prefix + open(dir+nm+"/")})
		case kind == pathDirs:
		case kind == pathImages && !refs.ImageExt(nm):
		default:
			files = append(files, ui.MenuItem{Name: nm, Desc: refs.HumanSize(info.Size()), Insert: before + prefix + quote(dir+nm) + " "})
		}
		if len(dirs)+len(files) >= refsMenuMax {
			break
		}
	}
	byName := func(a []ui.MenuItem) {
		sort.SliceStable(a, func(i, j int) bool { return strings.ToLower(a[i].Name) < strings.ToLower(a[j].Name) })
	}
	byName(dirs)
	byName(files)
	all := append(dirs, files...)
	title := "files"
	if dir != "" {
		title += " · " + dir
	}
	return &ui.MenuSet{Title: title, Items: ui.Rank(all, base, nil), Full: len(all)}
}

// refsHelpLines are the "keys" help rows for @ references and pasting.
func refsHelpLines() []string {
	return []string{
		ui.Ink("@path") + ui.Sub("         attach a file, folder or image (tab completes)"),
		ui.Ink("@diff @staged") + ui.Sub(" git working tree / staged diff   ") + ui.Ink("@git:5") + ui.Sub(" last 5 commits with patches"),
		ui.Ink("@file: @folder:") + ui.Sub(" a file · a folder's tree and small files   ") + ui.Ink("@url:") + ui.Sub(" a web page as text"),
		ui.Ink("ctrl+v") + ui.Sub("        paste: a clipboard image becomes [image N], else its text"),
		ui.Ink("ctrl+shift+v") + ui.Sub("  paste text only   ") + ui.Ink("@image:a.png") + ui.Sub(" or drag an image in to attach it"),
	}
}
