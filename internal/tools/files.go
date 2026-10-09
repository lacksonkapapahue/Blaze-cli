package tools

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var skipDirs = map[string]bool{".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true, "dist": true, "build": true, ".next": true, "target": true, ".gradle": true, ".idea": true}

func init() {
	register("file_read", "Read a text file with line numbers. Use offset/limit for big files.",
		[]prop{p("path", "string", "File path (relative to the current folder, or absolute, ~ allowed)"), p("offset", "integer", "First line, 1-based"), p("limit", "integer", "Max lines, default 400")},
		[]string{"path"}, func(e *Env, a args) string {
			path := expand(e, a.s("path"))
			f, err := os.Open(path)
			if err != nil {
				return "Error: " + err.Error() + "\n[hint: use file_list to see what's there]"
			}
			defer f.Close()
			off, lim := a.i("offset", 1), a.i("limit", 400)
			if off < 1 {
				off = 1
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
			var b strings.Builder
			n, shown := 0, 0
			for sc.Scan() {
				n++
				if n < off {
					continue
				}
				if shown >= lim {
					continue
				}
				line := sc.Text()
				if strings.ContainsRune(line, 0) {
					return "That's a binary file; can't show it as text."
				}
				fmt.Fprintf(&b, "%5d  %s\n", n, line)
				shown++
			}
			if n == 0 {
				return path + " is empty."
			}
			if off+shown-1 < n {
				fmt.Fprintf(&b, "… %d lines total; read more with offset=%d\n", n, off+shown)
			}
			return b.String()
		})

	register("file_write", "Create or overwrite a text file (folders are created). Writing outside the folder Blaze started in asks the user first.",
		[]prop{p("path", "string", "File path"), p("content", "string", "Full file content"), p("append", "boolean", "Append instead of overwrite")},
		[]string{"path", "content"}, func(e *Env, a args) string {
			path := expand(e, a.s("path"))
			if !inside(e.Root, path) && !inside(dataDir(""), path) && !e.ask("Write a file outside this project?", path) {
				return "The user did NOT approve writing there. Write inside the project folder instead, or ask."
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "Error: " + err.Error()
			}
			flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
			if a.b("append") {
				flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
			}
			f, err := os.OpenFile(path, flag, 0o644)
			if err != nil {
				return "Error: " + err.Error()
			}
			defer f.Close()
			c := a.s("content")
			if _, err := f.WriteString(c); err != nil {
				return "Error: " + err.Error()
			}
			e.status("Wrote " + rel(e, path))
			return fmt.Sprintf("Wrote %s (%d lines, %d bytes).", path, strings.Count(c, "\n")+1, len(c))
		})

	register("file_edit", "Replace an exact piece of text in a file. old_text must appear exactly once (include surrounding lines to make it unique) unless all=true.",
		[]prop{p("path", "string", "File path"), p("old_text", "string", "Exact text to replace"), p("new_text", "string", "Replacement"), p("all", "boolean", "Replace every occurrence")},
		[]string{"path", "old_text", "new_text"}, func(e *Env, a args) string {
			path := expand(e, a.s("path"))
			b, err := os.ReadFile(path)
			if err != nil {
				return "Error: " + err.Error()
			}
			s, old := string(b), a.s("old_text")
			if old == "" {
				return "Error: old_text is empty."
			}
			n := strings.Count(s, old)
			if n == 0 {
				// Forgive CRLF vs LF differences.
				if strings.Contains(s, "\r\n") && strings.Contains(s, strings.ReplaceAll(old, "\n", "\r\n")) {
					old = strings.ReplaceAll(old, "\n", "\r\n")
					n = strings.Count(s, old)
				}
			}
			if n == 0 {
				return "Error: old_text wasn't found. Read the file again (file_read) and copy the text exactly."
			}
			if n > 1 && !a.b("all") {
				return fmt.Sprintf("Error: old_text appears %d times. Add surrounding lines so it's unique, or set all=true.", n)
			}
			if !inside(e.Root, path) && !e.ask("Edit a file outside this project?", path) {
				return "The user did NOT approve editing that file."
			}
			if a.b("all") {
				s = strings.ReplaceAll(s, old, a.s("new_text"))
			} else {
				s = strings.Replace(s, old, a.s("new_text"), 1)
			}
			if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
				return "Error: " + err.Error()
			}
			e.status("Edited " + rel(e, path))
			return fmt.Sprintf("Edited %s (%d replacement%s).", path, map[bool]int{true: n, false: 1}[a.b("all")], plural(n, a.b("all")))
		})

	register("file_list", "List a folder as a tree (skips .git, node_modules, venvs, build output).",
		[]prop{p("path", "string", "Folder, default the current one"), p("depth", "integer", "How deep, default 2")}, nil,
		func(e *Env, a args) string {
			root := expand(e, a.s("path"))
			st, err := os.Stat(root)
			if err != nil {
				return "Error: " + err.Error()
			}
			if !st.IsDir() {
				return fmt.Sprintf("%s is a file (%d bytes).", root, st.Size())
			}
			depth := a.i("depth", 2)
			var b strings.Builder
			b.WriteString(root + "\n")
			count := 0
			var walk func(dir, indent string, d int)
			walk = func(dir, indent string, d int) {
				ents, err := os.ReadDir(dir)
				if err != nil {
					return
				}
				sort.Slice(ents, func(i, j int) bool {
					if ents[i].IsDir() != ents[j].IsDir() {
						return ents[i].IsDir()
					}
					return ents[i].Name() < ents[j].Name()
				})
				for _, en := range ents {
					if count > 400 {
						return
					}
					count++
					if en.IsDir() {
						b.WriteString(indent + en.Name() + "/\n")
						if skipDirs[en.Name()] {
							continue
						}
						if d < depth {
							walk(filepath.Join(dir, en.Name()), indent+"  ", d+1)
						}
					} else {
						info, _ := en.Info()
						size := int64(0)
						if info != nil {
							size = info.Size()
						}
						fmt.Fprintf(&b, "%s%s  %s\n", indent, en.Name(), human(size))
					}
				}
			}
			walk(root, "  ", 1)
			if count > 400 {
				b.WriteString("  … (cut at 400 entries; list a subfolder)\n")
			}
			return b.String()
		})

	register("file_search", "Search file contents for a regex (or plain text) under a folder, like grep -rn. Optional glob filter, e.g. *.go.",
		[]prop{p("pattern", "string", "Regex or text"), p("path", "string", "Folder, default current"), p("glob", "string", "File name filter, e.g. *.py")},
		[]string{"pattern"}, func(e *Env, a args) string {
			re, err := regexp.Compile("(?i)" + a.s("pattern"))
			if err != nil {
				re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(a.s("pattern")))
			}
			root := expand(e, a.s("path"))
			glob := a.s("glob")
			var hits []string
			_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if skipDirs[d.Name()] && path != root {
						return filepath.SkipDir
					}
					return nil
				}
				if glob != "" {
					if ok, _ := filepath.Match(glob, d.Name()); !ok {
						return nil
					}
				}
				if info, _ := d.Info(); info != nil && info.Size() > 2<<20 {
					return nil
				}
				f, err := os.Open(path)
				if err != nil {
					return nil
				}
				defer f.Close()
				sc := bufio.NewScanner(f)
				sc.Buffer(make([]byte, 256*1024), 1024*1024)
				n := 0
				for sc.Scan() {
					n++
					l := sc.Text()
					if strings.ContainsRune(l, 0) {
						return nil
					}
					if re.MatchString(l) {
						if len(l) > 200 {
							l = l[:200] + "…"
						}
						hits = append(hits, fmt.Sprintf("%s:%d: %s", rel(e, path), n, strings.TrimSpace(l)))
						if len(hits) >= 200 {
							return filepath.SkipAll
						}
					}
				}
				return nil
			})
			if len(hits) == 0 {
				return "No matches."
			}
			return strings.Join(hits, "\n")
		})

	register("file_delete", "Delete a file or folder. Always asks the user first.",
		[]prop{p("path", "string", "Path")}, []string{"path"}, func(e *Env, a args) string {
			path := expand(e, a.s("path"))
			if _, err := os.Stat(path); err != nil {
				return "Nothing at " + path
			}
			h, _ := os.UserHomeDir()
			if path == h || path == "/" || filepath.Dir(path) == path {
				return "Refused: that would delete a whole drive or home folder."
			}
			if !e.Confirm("Delete?", path) {
				return "The user did NOT approve deleting it."
			}
			if err := os.RemoveAll(path); err != nil {
				return "Error: " + err.Error()
			}
			return "Deleted " + path
		})
}

func rel(e *Env, path string) string {
	if r, err := filepath.Rel(e.Shell.Cwd(), path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}

func human(n int64) string {
	switch {
	case n > 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n > 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n > 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func plural(n int, all bool) string {
	if all && n != 1 {
		return "s"
	}
	return ""
}
