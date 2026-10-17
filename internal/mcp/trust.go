package mcp

// Project trust: a folder's .mcp.json / .blaze/mcp.json can name any command,
// so its servers only start once the user trusted that exact content. The
// record lives in ~/.blaze/mcp-trust.json, keyed by the folder's absolute path,
// holding the SHA-256 of the project files; any edit makes the folder untrusted
// again until the user re-approves it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// Trust states of a folder's project servers.
const (
	TrustNone      = "none"      // no project file in the folder
	TrustTrusted   = "trusted"   // trusted, and the files are unchanged since
	TrustUntrusted = "untrusted" // never trusted
	TrustChanged   = "changed"   // trusted once, but the files changed since
)

// Project is the working folder's project MCP config as read right now.
type Project struct {
	Dir      string   // absolute folder path (the trust key)
	Hash     string   // SHA-256 of the project files' content ("" when there are none)
	Servers  []Server // project servers, .blaze/mcp.json over .mcp.json (unexpanded)
	Trust    string   // TrustNone, TrustTrusted, TrustUntrusted or TrustChanged
	Warnings []string
}

// Trusted is true when the project's servers may start.
func (p Project) Trusted() bool { return p.Trust == TrustTrusted }

// TrustFile is ~/.blaze/mcp-trust.json.
func TrustFile(home string) string { return filepath.Join(home, "mcp-trust.json") }

type trustEntry struct {
	SHA256  string `json:"sha256"`
	Trusted string `json:"trusted_at,omitempty"`
}

type trustDoc struct {
	Folders map[string]trustEntry `json:"folders"`
}

func readTrust(home string) trustDoc {
	d := trustDoc{Folders: map[string]trustEntry{}}
	if b, err := os.ReadFile(TrustFile(home)); err == nil {
		_ = json.Unmarshal(b, &d)
	}
	if d.Folders == nil {
		d.Folders = map[string]trustEntry{}
	}
	return d
}

func writeTrust(home string, d trustDoc) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(TrustFile(home), append(b, '\n'), 0o600)
}

// FolderKey is the absolute, cleaned (and, when possible, symlink-resolved) path
// trust is recorded under.
func FolderKey(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	return filepath.Clean(abs)
}

// projectFiles are the folder's project files Load reads (not the global file,
// in case the folder is the Blaze home itself).
func projectFiles(home, dir string) []string {
	var out []string
	for _, f := range ProjectFiles(dir) {
		if !samePath(f, GlobalFile(home)) {
			out = append(out, f)
		}
	}
	return out
}

// IsProjectFile is true when file is one of the folder's project files.
func IsProjectFile(home, dir, file string) bool {
	for _, f := range projectFiles(home, dir) {
		if samePath(f, file) {
			return true
		}
	}
	return false
}

// LoadProject reads the folder's project files once, hashes exactly the bytes
// it parses, and compares the hash with the trust record.
func LoadProject(home, dir string) Project {
	p := Project{Dir: FolderKey(dir), Trust: TrustNone}
	if dir == "" {
		return p
	}
	h := sha256.New()
	found := false
	byName := map[string]Server{}
	for _, f := range projectFiles(home, dir) {
		rel, _ := filepath.Rel(dir, f)
		b, err := os.ReadFile(f)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s: %v", f, err))
			}
			fmt.Fprintf(h, "%s\x00-\x00", filepath.ToSlash(rel))
			continue
		}
		found = true
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(b))
		h.Write(b)
		ss, err := parseServers(f, b)
		if err != nil {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		for _, s := range ss {
			s.Project = true
			byName[s.Name] = s
		}
	}
	if !found {
		return p
	}
	p.Hash = hex.EncodeToString(h.Sum(nil))
	for _, s := range byName {
		p.Servers = append(p.Servers, s)
	}
	sort.Slice(p.Servers, func(i, j int) bool { return p.Servers[i].Name < p.Servers[j].Name })
	e, ok := readTrust(home).Folders[p.Dir]
	switch {
	case ok && e.SHA256 == p.Hash:
		p.Trust = TrustTrusted
	case ok:
		p.Trust = TrustChanged
	default:
		p.Trust = TrustUntrusted
	}
	for i := range p.Servers {
		p.Servers[i].Trusted = p.Trust == TrustTrusted
	}
	return p
}

// Trust records that the user trusts the folder's project files with this hash
// (the hash of the content they were shown, from LoadProject).
func Trust(home, dir, hash string) error {
	if hash == "" {
		return errors.New("this folder has no project MCP file to trust")
	}
	key := FolderKey(dir)
	d := readTrust(home)
	d.Folders[key] = trustEntry{SHA256: hash, Trusted: time.Now().UTC().Format(time.RFC3339)}
	return writeTrust(home, d)
}

// Untrust forgets the folder's trust; false when it wasn't trusted.
func Untrust(home, dir string) (bool, error) {
	key := FolderKey(dir)
	d := readTrust(home)
	if _, ok := d.Folders[key]; !ok {
		return false, nil
	}
	delete(d.Folders, key)
	return true, writeTrust(home, d)
}

// writeFileAtomic writes to a temp file in the same folder, syncs it and
// renames it over path, so a crash never leaves a half-written file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// ---------------- approval keys ----------------

// ApprovalKey identifies a server for "Always allow": where it came from (the
// global file, or the exact project file), its name, and a hash of what it
// runs (command, args, cwd) or where it connects (URL). A project server can
// therefore never inherit a global server's approvals, and changing a server's
// command or URL drops its approvals.
func ApprovalKey(s Server) string {
	scope := "global"
	if s.Project {
		src, err := filepath.Abs(s.Source)
		if err != nil {
			src = s.Source
		}
		scope = "project:" + filepath.Clean(src)
	}
	h := sha256.New()
	if s.Remote() {
		fmt.Fprintf(h, "http\x00%s", s.URL)
	} else {
		fmt.Fprintf(h, "stdio\x00%s\x00%s\x00", s.Command, s.Cwd)
		for _, a := range s.Args {
			fmt.Fprintf(h, "%s\x00", strconv.Quote(a))
		}
	}
	return scope + ":" + s.Name + "#" + hex.EncodeToString(h.Sum(nil))[:16]
}
