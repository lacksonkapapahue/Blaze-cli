package refs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxFileRead   = 2 * 1024 * 1024 // bytes read from one file (the block cap cuts further)
	folderDepth   = 3
	folderEntries = 200
	smallFile     = 8 * 1024 // files up to this size are inlined in a @folder block
)

// skipDirs are never walked into by @folder.
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "__pycache__": true,
	".venv": true, "venv": true, "dist": true, "build": true, "target": true, ".next": true, ".cache": true}

// readFile reads a text file for a block.
func readFile(p string) (body, detail string, err error) {
	f, err := os.Open(p)
	if err != nil {
		return "", "", cleanFSErr(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", "", cleanFSErr(err)
	}
	if st.IsDir() {
		return "", "", errors.New("is a folder (use @folder:)")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFileRead))
	if err != nil {
		return "", "", cleanFSErr(err)
	}
	if binary(b) {
		return "", "", fmt.Errorf("binary file (%s)", HumanSize(st.Size()))
	}
	s := string(b)
	if st.Size() > maxFileRead {
		s += fmt.Sprintf("\n…(only the first %s of %s read)", HumanSize(maxFileRead), HumanSize(st.Size()))
	}
	return s, plural(lines(s), "line"), nil
}

func binary(b []byte) bool { return bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 }

func cleanFSErr(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return errors.New("no such file")
	case errors.Is(err, os.ErrPermission):
		return errors.New("permission denied")
	}
	return err
}

// readFolder lists a folder as a tree (a few levels, capped) and inlines the
// small text files that fit in budget bytes.
func readFolder(root string, budget int) (body, detail string, err error) {
	st, err := os.Stat(root)
	if err != nil {
		return "", "", cleanFSErr(err)
	}
	if !st.IsDir() {
		return "", "", errors.New("not a folder")
	}
	var tree strings.Builder
	var files []string
	count, more := 0, false
	var walk func(dir, indent string, depth int)
	walk = func(dir, indent string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		sort.SliceStable(ents, func(i, j int) bool {
			if ents[i].IsDir() != ents[j].IsDir() {
				return ents[i].IsDir()
			}
			return strings.ToLower(ents[i].Name()) < strings.ToLower(ents[j].Name())
		})
		for _, e := range ents {
			nm := e.Name()
			if strings.HasPrefix(nm, ".") && nm != ".gitignore" && nm != ".env.example" {
				continue
			}
			if count >= folderEntries {
				more = true
				return
			}
			count++
			p := filepath.Join(dir, nm)
			if e.IsDir() {
				tree.WriteString(indent + nm + "/\n")
				if depth < folderDepth && !skipDirs[nm] {
					walk(p, indent+"  ", depth+1)
				}
				continue
			}
			size := ""
			if info, err := e.Info(); err == nil {
				size = "  " + HumanSize(info.Size())
				if info.Size() <= smallFile && info.Mode().IsRegular() {
					files = append(files, p)
				}
			}
			tree.WriteString(indent + nm + size + "\n")
		}
	}
	walk(root, "", 1)
	if more {
		fmt.Fprintf(&tree, "…(listing stops at %d entries)\n", folderEntries)
	}
	var b strings.Builder
	b.WriteString(tree.String())
	left := budget - b.Len() - 200
	inlined := 0
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil || binary(data) || ImageExt(p) {
			continue
		}
		rel, _ := filepath.Rel(root, p)
		chunk := fmt.Sprintf("\n<file path=%q>\n%s\n</file>\n", filepath.ToSlash(rel), strings.TrimRight(string(data), "\n"))
		if len(chunk) > left {
			continue
		}
		left -= len(chunk)
		b.WriteString(chunk)
		inlined++
	}
	if skipped := len(files) - inlined; skipped > 0 && inlined > 0 {
		fmt.Fprintf(&b, "\n…(%d more small files not inlined: size cap)\n", skipped)
	}
	return b.String(), plural(count, "entry") + " · " + plural(inlined, "file"), nil
}
