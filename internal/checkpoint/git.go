package checkpoint

// The git side: a snapshot of the whole working tree (tracked and untracked files
// that aren't ignored) written as a tree object through a private index file, so the
// user's index, branch and working tree are never touched. Trees are kept alive
// under refs/blaze/checkpoints/<session>/.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// hideCmd keeps git from flashing a console window on Windows (proc_windows.go).
var hideCmd = func(*exec.Cmd) {}

var gitTimeout = 60 * time.Second

func runGit(dir string, env []string, stdin []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), append([]string{"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"}, env...)...)
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	hideCmd(c)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(msg)
	}
	return out.String(), nil
}

func hasGit() bool { _, err := exec.LookPath("git"); return err == nil }

// repoTop is the top of the git repo holding dir ("" when there's none or no git).
// It is built from dir (not git's resolved path) so paths under dir keep the same
// spelling through symlinks like macOS's /tmp.
func repoTop(dir string) string {
	if dir == "" || !hasGit() {
		return ""
	}
	out, err := runGit(dir, nil, nil, "rev-parse", "--show-cdup")
	if err != nil {
		return ""
	}
	return filepath.Clean(filepath.Join(dir, filepath.FromSlash(strings.TrimSpace(out))))
}

// snapshotTree writes the working tree as a git tree and returns its id. scratch is
// a folder for the private index file.
func snapshotTree(top, scratch string) (string, error) {
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return "", err
	}
	idx := filepath.Join(scratch, "index.tmp")
	_ = os.Remove(idx)
	defer os.Remove(idx)
	// Start from a copy of the real index: git add then only rehashes what changed.
	if p, err := runGit(top, nil, nil, "rev-parse", "--git-path", "index"); err == nil {
		real := strings.TrimSpace(p)
		if !filepath.IsAbs(real) {
			real = filepath.Join(top, real)
		}
		if b, err := os.ReadFile(real); err == nil {
			_ = os.WriteFile(idx, b, 0o600)
		}
	}
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := runGit(top, env, nil, "add", "-A", "--", "."); err != nil {
		return "", err
	}
	out, err := runGit(top, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func setRef(top, ref, id string) error {
	_, err := runGit(top, nil, nil, "update-ref", ref, id)
	return err
}

func delRef(top, ref string) error {
	_, err := runGit(top, nil, nil, "update-ref", "-d", ref)
	return err
}

// diffTrees lists repo-relative paths that differ between two trees.
func diffTrees(top, a, b string) ([]string, error) {
	if a == b {
		return nil, nil
	}
	out, err := runGit(top, nil, nil, "diff-tree", "-r", "-z", "--name-only", "--no-renames", a, b)
	if err != nil {
		return nil, err
	}
	var ps []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			ps = append(ps, p)
		}
	}
	return ps, nil
}

// treeEntry reports whether rel is a file in tree and its mode.
func treeEntry(top, tree, rel string) (bool, os.FileMode, error) {
	out, err := runGit(top, nil, nil, "ls-tree", "-z", tree, "--", rel)
	if err != nil {
		return false, 0, err
	}
	out = strings.TrimRight(out, "\x00")
	if out == "" {
		return false, 0, nil
	}
	f := strings.Fields(out)
	if len(f) < 2 || f[1] != "blob" {
		return false, 0, nil // a submodule or folder: not something to restore
	}
	if f[0] == "100755" {
		return true, 0o755, nil
	}
	return true, 0o644, nil
}

// treeFile reads rel from tree the way checkout would write it (line endings and
// filters applied).
func treeFile(top, tree, rel string) ([]byte, error) {
	out, err := runGit(top, nil, nil, "cat-file", "--filters", tree+":"+rel)
	if err != nil {
		// older git without --filters: the raw blob
		out, err = runGit(top, nil, nil, "cat-file", "blob", tree+":"+rel)
		if err != nil {
			return nil, err
		}
	}
	return []byte(out), nil
}

// ignored reports which repo-relative paths git ignores (tracked files never are).
func ignored(top string, rels []string) map[string]bool {
	out := map[string]bool{}
	if len(rels) == 0 {
		return out
	}
	res, _ := runGit(top, nil, []byte(strings.Join(rels, "\x00")+"\x00"), "check-ignore", "-z", "--stdin") // exit 1 = none ignored
	for _, p := range strings.Split(res, "\x00") {
		if p != "" {
			out[p] = true
		}
	}
	return out
}
