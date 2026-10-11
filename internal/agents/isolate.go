package agents

// Isolation (v0.6.0): a Build-mode specialist that works on code gets its own copy of
// the folder, so parallel specialists never trample each other or the user's files.
//
//   git repo   a git worktree on its own branch (blaze/<handle>), sharing the repo's
//              objects: cheap on disk and instant to create.
//   otherwise  a plain copy of the folder (heavy caches like node_modules skipped)
//              with a manifest of what each file looked like, so Apply can tell what
//              the specialist changed and refuse to overwrite files the user changed
//              meanwhile.
//
// Nothing goes back to the original folder until the lead has reviewed the changes
// and calls Apply (apply_specialist). Discard removes the copy (and the branch).

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Isolation is where a specialist works instead of the original folder.
type Isolation struct {
	Kind     string            `json:"kind"`             // worktree | copy
	Source   string            `json:"source"`           // the original folder (the repo top for worktrees)
	Sub      string            `json:"sub,omitempty"`    // the asked-for workdir relative to Source
	Path     string            `json:"path"`             // the isolated root
	Branch   string            `json:"branch,omitempty"` // worktree: its branch
	Base     string            `json:"base,omitempty"`   // worktree: the commit it started from
	Dirty    bool              `json:"dirty,omitempty"`  // worktree: the source had uncommitted changes the copy doesn't carry
	Manifest map[string]string `json:"manifest,omitempty"`
}

// Workdir is the folder the specialist works in (the asked-for subfolder inside its copy).
func (iso *Isolation) Workdir() string {
	if iso.Sub == "" || iso.Sub == "." {
		return iso.Path
	}
	return filepath.Join(iso.Path, iso.Sub)
}

// OriginalWorkdir is the asked-for folder in the original tree.
func (iso *Isolation) OriginalWorkdir() string {
	if iso.Sub == "" || iso.Sub == "." {
		return iso.Source
	}
	return filepath.Join(iso.Source, iso.Sub)
}

// Copy limits: a folder bigger than this should be a git repo (worktrees are free).
var (
	maxCopyFiles = 20000
	maxCopyBytes = int64(400 << 20)
)

// skipDirs are never copied (rebuildable caches); the specialist reinstalls if needed.
var skipDirs = map[string]bool{".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true, ".next": true, ".cache": true, ".gradle": true, ".blaze-worktrees": true}

// hideCmd keeps git from flashing a console window on Windows (set in proc_windows.go).
var hideCmd = func(*exec.Cmd) {}

func git(dir string, args ...string) (string, error) {
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	hideCmd(c)
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out.String(), nil
}

// gitInput runs git with stdin.
func gitInput(dir string, in []byte, args ...string) (string, error) {
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	hideCmd(c)
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	c.Stdin = bytes.NewReader(in)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(errb.String()+" "+out.String()))
	}
	return out.String(), nil
}

// HasGit reports whether a git binary is on PATH.
func HasGit() bool { _, err := exec.LookPath("git"); return err == nil }

// repoTop is the git top-level folder of dir ("" when it isn't in a repo with commits).
func repoTop(dir string) string {
	if !HasGit() {
		return ""
	}
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	if _, err := git(dir, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		return "" // a repo without a first commit: a worktree has nothing to start from
	}
	return filepath.Clean(strings.TrimSpace(top))
}

// Isolate makes the isolated copy of src at dest. branch names the worktree branch.
func Isolate(src, dest, branch string) (*Isolation, error) {
	src = filepath.Clean(src)
	st, err := os.Stat(src)
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("the workdir %q isn't a folder", src)
	}
	if _, err := os.Stat(dest); err == nil {
		return nil, fmt.Errorf("%s already exists", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, err
	}
	if top := repoTop(src); top != "" {
		return worktree(src, top, dest, branch)
	}
	return copyTree(src, dest)
}

func worktree(src, top, dest, branch string) (*Isolation, error) {
	base, err := git(top, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	sub, _ := filepath.Rel(evalPath(top), evalPath(src))
	if strings.HasPrefix(sub, "..") {
		sub = ""
	}
	dirty := false
	if s, err := git(top, "status", "--porcelain"); err == nil && strings.TrimSpace(s) != "" {
		dirty = true
	}
	// A branch left over from an earlier specialist of the same name gets a suffix.
	b := branch
	for i := 2; ; i++ {
		if _, err := git(top, "rev-parse", "--verify", "-q", "refs/heads/"+b); err != nil {
			break
		}
		b = fmt.Sprintf("%s-%d", branch, i)
	}
	if _, err := git(top, "worktree", "add", "-q", "-b", b, dest, "HEAD"); err != nil {
		return nil, err
	}
	return &Isolation{Kind: "worktree", Source: top, Sub: filepath.ToSlash(sub), Path: dest, Branch: b, Base: strings.TrimSpace(base), Dirty: dirty}, nil
}

func evalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// walkFiles lists regular files (and symlinks) under root, skipping cache folders.
func walkFiles(root string, fn func(rel string, d fs.DirEntry) error) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		return fn(filepath.ToSlash(rel), d)
	})
}

func copyTree(src, dest string) (*Isolation, error) {
	man := map[string]string{}
	var files int
	var size int64
	err := walkFiles(src, func(rel string, d fs.DirEntry) error {
		files++
		if files > maxCopyFiles {
			return fmt.Errorf("the folder has more than %d files; make it a git repo (git init && git add -A && git commit) so the specialist can use a worktree", maxCopyFiles)
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			size += info.Size()
		}
		if size > maxCopyBytes {
			return fmt.Errorf("the folder is over %d MB; make it a git repo so the specialist can use a worktree", maxCopyBytes>>20)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = walkFiles(src, func(rel string, d fs.DirEntry) error {
		from, to := filepath.Join(src, filepath.FromSlash(rel)), filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if t, err := os.Readlink(from); err == nil {
				_ = os.Symlink(t, to)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		h, err := copyFile(from, to)
		if err != nil {
			return err
		}
		man[rel] = h
		return nil
	})
	if err != nil {
		_ = os.RemoveAll(dest)
		return nil, err
	}
	_ = os.MkdirAll(dest, 0o755) // an empty folder still gets its copy
	return &Isolation{Kind: "copy", Source: src, Path: dest, Manifest: man}, nil
}

func copyFile(from, to string) (string, error) {
	in, err := os.Open(from)
	if err != nil {
		return "", err
	}
	defer in.Close()
	mode := os.FileMode(0o644)
	if st, err := in.Stat(); err == nil {
		mode = st.Mode().Perm()
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return "", err
	}
	h := sha1.New()
	_, err = io.Copy(io.MultiWriter(out, h), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return hex.EncodeToString(h.Sum(nil)), err
}

// Change is one file the specialist changed.
type Change struct {
	Path string `json:"path"`
	Op   string `json:"op"` // added | modified | deleted
}

// Changes lists what the specialist changed, and a unified diff when git can make one
// (always for worktrees; for copies when git is installed), clipped to max bytes.
func (iso *Isolation) Changes(max int) ([]Change, string, error) {
	if iso.Kind == "worktree" {
		if _, err := git(iso.Path, "add", "-A"); err != nil {
			return nil, "", err
		}
		names, err := git(iso.Path, "diff", "--cached", "--name-status", "--no-renames", iso.Base)
		if err != nil {
			return nil, "", err
		}
		var cs []Change
		for _, ln := range strings.Split(strings.TrimSpace(names), "\n") {
			f := strings.SplitN(ln, "\t", 2)
			if len(f) != 2 {
				continue
			}
			op := map[string]string{"A": "added", "D": "deleted"}[f[0][:1]]
			if op == "" {
				op = "modified"
			}
			cs = append(cs, Change{Path: f[1], Op: op})
		}
		patch, _ := git(iso.Path, "diff", "--cached", "--stat", "--patch", iso.Base)
		return cs, clipBytes(patch, max), nil
	}
	cs, err := iso.copyChanges()
	if err != nil {
		return nil, "", err
	}
	var b strings.Builder
	if HasGit() {
		for _, c := range cs {
			a, z := filepath.Join(iso.Source, filepath.FromSlash(c.Path)), filepath.Join(iso.Path, filepath.FromSlash(c.Path))
			switch c.Op {
			case "added":
				a = "/dev/null" // git's own name for "no file", on every OS
			case "deleted":
				z = "/dev/null"
			}
			d, _ := git(iso.Path, "diff", "--no-index", "--", a, z) // exit 1 = differences
			b.WriteString(d)
			if b.Len() > max {
				break
			}
		}
	}
	return cs, clipBytes(b.String(), max), nil
}

func clipBytes(s string, max int) string {
	if max > 0 && len(s) > max {
		return s[:max] + "\n… (diff clipped)"
	}
	return s
}

func (iso *Isolation) copyChanges() ([]Change, error) {
	var cs []Change
	seen := map[string]bool{}
	err := walkFiles(iso.Path, func(rel string, d fs.DirEntry) error {
		if !d.Type().IsRegular() {
			return nil
		}
		seen[rel] = true
		h, err := hashFile(filepath.Join(iso.Path, filepath.FromSlash(rel)))
		if err != nil {
			return nil
		}
		old, ok := iso.Manifest[rel]
		switch {
		case !ok:
			cs = append(cs, Change{Path: rel, Op: "added"})
		case old != h:
			cs = append(cs, Change{Path: rel, Op: "modified"})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for rel := range iso.Manifest {
		if !seen[rel] {
			cs = append(cs, Change{Path: rel, Op: "deleted"})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Path < cs[j].Path })
	return cs, nil
}

// ErrNothing means the specialist changed nothing.
var ErrNothing = errors.New("the specialist changed nothing")

// Apply brings the specialist's changes into the original folder. Worktrees: the
// changes are committed on the specialist's branch, then applied to the original
// working tree as uncommitted changes (3-way when needed), so the user reviews and
// commits them like their own. Copies: changed files are copied back, but a file the
// user also changed since the copy is left alone and reported as a conflict.
func (iso *Isolation) Apply(message string) (string, error) {
	cs, _, err := iso.Changes(0)
	if err != nil {
		return "", err
	}
	if len(cs) == 0 {
		return "", ErrNothing
	}
	if iso.Kind == "worktree" {
		if strings.TrimSpace(message) == "" {
			message = "Blaze specialist changes"
		}
		_, _ = git(iso.Path, "-c", "user.name=Blaze", "-c", "user.email=blaze@localhost", "commit", "-q", "--no-verify", "-m", message)
		patch, err := git(iso.Path, "diff", "--binary", iso.Base, "HEAD")
		if err != nil {
			return "", err
		}
		if _, err := gitInput(iso.Source, []byte(patch), "apply", "--check", "--whitespace=nowarn", "-"); err == nil {
			if _, err := gitInput(iso.Source, []byte(patch), "apply", "--whitespace=nowarn", "-"); err != nil {
				return "", err
			}
		} else if _, err3 := gitInput(iso.Source, []byte(patch), "apply", "--3way", "--whitespace=nowarn", "-"); err3 != nil {
			return "", fmt.Errorf("the changes don't apply cleanly to %s (it changed meanwhile): %v. They're kept on branch %s", iso.Source, err3, iso.Branch)
		}
		return fmt.Sprintf("Applied %d file change(s) to %s as uncommitted changes; review and commit them like your own.", len(cs), iso.Source), nil
	}
	var applied, conflicts []string
	for _, c := range cs {
		orig := filepath.Join(iso.Source, filepath.FromSlash(c.Path))
		cp := filepath.Join(iso.Path, filepath.FromSlash(c.Path))
		cur, curErr := hashFile(orig)
		switch c.Op {
		case "added":
			if curErr == nil { // the user made the same file meanwhile
				conflicts = append(conflicts, c.Path)
				continue
			}
		case "modified", "deleted":
			if curErr != nil || cur != iso.Manifest[c.Path] {
				conflicts = append(conflicts, c.Path)
				continue
			}
		}
		if c.Op == "deleted" {
			if err := os.Remove(orig); err != nil {
				conflicts = append(conflicts, c.Path)
				continue
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(orig), 0o755); err != nil {
				return "", err
			}
			if _, err := copyFile(cp, orig); err != nil {
				return "", err
			}
		}
		applied = append(applied, c.Path)
	}
	msg := fmt.Sprintf("Applied %d file change(s) to %s.", len(applied), iso.Source)
	if len(conflicts) > 0 {
		msg += fmt.Sprintf(" Skipped %d file(s) that also changed in the original since the copy (resolve by hand): %s", len(conflicts), strings.Join(conflicts, ", "))
		if len(applied) == 0 {
			return msg, errors.New(msg)
		}
	}
	return msg, nil
}

// Remove deletes the isolated copy (and a worktree's branch when its work was applied
// or discarded).
func (iso *Isolation) Remove(dropBranch bool) error {
	if iso.Kind == "worktree" {
		_, err := git(iso.Source, "worktree", "remove", "--force", iso.Path)
		if err != nil {
			_ = os.RemoveAll(iso.Path)
			_, _ = git(iso.Source, "worktree", "prune")
		}
		if dropBranch && iso.Branch != "" {
			_, _ = git(iso.Source, "branch", "-D", iso.Branch)
		}
		return nil
	}
	return os.RemoveAll(iso.Path)
}
