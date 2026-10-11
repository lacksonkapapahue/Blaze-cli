package checkpoint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setup(t *testing.T) string {
	t.Helper()
	t.Setenv("BLAZE_HOME", t.TempDir())
	return t.TempDir()
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

// change does what a file tool does: snapshot, change, record.
func change(s *Session, p string, fn func()) {
	s.Before([]string{p})
	fn()
	s.After([]string{p})
}

func rewind(t *testing.T, s *Session, i int, overwrite bool) (*Plan, Result) {
	t.Helper()
	p, err := s.PlanRestore(i)
	if err != nil {
		t.Fatal(err)
	}
	return p, s.Restore(p, overwrite)
}

func TestEditCreateDeleteRestore(t *testing.T) {
	root := setup(t)
	edited, created, deleted := filepath.Join(root, "a.txt"), filepath.Join(root, "new", "deep", "b.txt"), filepath.Join(root, "c.txt")
	write(t, edited, "before")
	write(t, deleted, "keep me")

	s := Open(root)
	if s.Git() {
		t.Skip("temp dir is inside a git repo")
	}
	s.Begin("user", "do things", "do things", 0)
	change(s, edited, func() { write(t, edited, "after") })
	change(s, edited, func() { write(t, edited, "after again") }) // the first copy wins
	change(s, created, func() { write(t, created, "brand new") })
	change(s, deleted, func() { os.Remove(deleted) })
	s.End()

	turn := s.List()[0]
	if n := len(turn.Changed(s.RepoTop)); n != 3 {
		t.Fatalf("files changed = %d, want 3", n)
	}
	p, r := rewind(t, s, 0, false)
	if len(p.Conflicts()) != 0 || r.N() != 3 || len(r.Failed) != 0 {
		t.Fatalf("plan=%+v result=%+v", p.Items, r)
	}
	if got := read(t, edited); got != "before" {
		t.Errorf("edited = %q", got)
	}
	if got := read(t, deleted); got != "keep me" {
		t.Errorf("deleted = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Errorf("created file's new folders should be removed")
	}
	if !strings.Contains(strings.Join(p.Warnings, " "), "isn't a git repo") {
		t.Errorf("non-git rewind should say only Blaze's edits are tracked: %v", p.Warnings)
	}
}

func TestBlobsAreContentAddressedAndNoOpsDropped(t *testing.T) {
	root := setup(t)
	a, b := filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")
	write(t, a, "same text")
	write(t, b, "same text")
	s := Open(root)
	s.Begin("user", "x", "x", 0)
	change(s, a, func() { write(t, a, "1") })
	change(s, b, func() { write(t, b, "2") })
	untouched := filepath.Join(root, "c.txt")
	write(t, untouched, "c")
	change(s, untouched, func() {}) // declined write: nothing changed
	s.End()
	var blobs int
	_ = filepath.Walk(filepath.Join(s.Path(), "blobs"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			blobs++
		}
		return nil
	})
	if blobs != 2 { // "same text" once, "c" once
		t.Fatalf("blobs = %d, want 2", blobs)
	}
	if n := len(s.List()[0].Order); n != 2 {
		t.Fatalf("no-op change should be dropped, got %d files", n)
	}
}

func TestMultipleTurns(t *testing.T) {
	root := setup(t)
	f := filepath.Join(root, "f.txt")
	write(t, f, "v0")
	s := Open(root)
	for i, v := range []string{"v1", "v2", "v3"} {
		s.Begin("user", v, v, i*2)
		change(s, f, func() { write(t, f, v) })
		s.End()
	}
	rewind(t, s, 2, false)
	if got := read(t, f); got != "v2" {
		t.Fatalf("after rewinding turn 3: %q", got)
	}
	s.DropFrom(2)
	rewind(t, s, 1, false)
	if got := read(t, f); got != "v1" {
		t.Fatalf("after rewinding turn 2: %q", got)
	}
	s.DropFrom(1)
	rewind(t, s, 0, false)
	if got := read(t, f); got != "v0" {
		t.Fatalf("after rewinding turn 1: %q", got)
	}
	// reopening the session (blaze -c) keeps the checkpoints
	s2, err := Latest(root)
	if err != nil || len(s2.List()) != 1 {
		t.Fatalf("Latest: %v %d", err, len(s2.List()))
	}
}

func TestConflictDetection(t *testing.T) {
	root := setup(t)
	f, g := filepath.Join(root, "f.txt"), filepath.Join(root, "g.txt")
	write(t, f, "orig")
	write(t, g, "orig g")
	s := Open(root)
	s.Begin("user", "1", "1", 0)
	change(s, f, func() { write(t, f, "blaze 1") })
	change(s, g, func() { write(t, g, "blaze g") })
	s.End()
	write(t, g, "user edit between turns") // outside Blaze
	s.Begin("user", "2", "2", 2)
	change(s, g, func() { write(t, g, "blaze g 2") })
	s.End()
	write(t, f, "user edit after") // outside Blaze

	p, err := s.PlanRestore(0)
	if err != nil {
		t.Fatal(err)
	}
	cs := p.Conflicts()
	if len(cs) != 2 {
		t.Fatalf("conflicts = %v, want f.txt and g.txt", cs)
	}
	r := s.Restore(p, false)
	if len(r.Skipped) != 2 || read(t, f) != "user edit after" {
		t.Fatalf("without overwrite the user's files stay: %+v %q", r, read(t, f))
	}
	p, _ = s.PlanRestore(0)
	s.Restore(p, true)
	if read(t, f) != "orig" || read(t, g) != "orig g" {
		t.Fatalf("overwrite should restore: %q %q", read(t, f), read(t, g))
	}
}

func TestBigFilesAreNotedNotCopied(t *testing.T) {
	root := setup(t)
	old := MaxFileSize
	MaxFileSize = 10
	t.Cleanup(func() { MaxFileSize = old })
	f := filepath.Join(root, "big.bin")
	write(t, f, strings.Repeat("x", 100))
	s := Open(root)
	s.Begin("user", "x", "x", 0)
	change(s, f, func() { write(t, f, "small") })
	s.End()
	p, r := rewind(t, s, 0, false)
	if len(r.Skipped) != 1 || !strings.Contains(strings.Join(p.Warnings, " "), "no copy was kept") {
		t.Fatalf("big file: %+v %v", r, p.Warnings)
	}
}

func TestDeletedFolderComesBack(t *testing.T) {
	root := setup(t)
	d := filepath.Join(root, "docs")
	write(t, filepath.Join(d, "a.md"), "A")
	write(t, filepath.Join(d, "x", "b.md"), "B")
	s := Open(root)
	s.Begin("user", "x", "x", 0)
	change(s, d, func() { os.RemoveAll(d) })
	s.End()
	rewind(t, s, 0, false)
	if read(t, filepath.Join(d, "a.md")) != "A" || read(t, filepath.Join(d, "x", "b.md")) != "B" {
		t.Fatal("folder not restored")
	}
}

func TestPrune(t *testing.T) {
	root := setup(t)
	s := Open(root)
	s.Begin("user", "x", "x", 0)
	s.End()
	s.Updated = time.Now().Add(-31 * 24 * time.Hour)
	s.mu.Lock()
	s.save()
	s.mu.Unlock()
	// save stamps Updated; backdate the file itself
	b, _ := os.ReadFile(filepath.Join(s.Path(), "index.json"))
	stamp := s.Updated.Format(time.RFC3339Nano)
	old := time.Now().Add(-31 * 24 * time.Hour).Format(time.RFC3339Nano)
	_ = os.WriteFile(filepath.Join(s.Path(), "index.json"), []byte(strings.Replace(string(b), stamp, old, 1)), 0o600)
	if n := Prune(KeepFor); n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatal("old session should be gone")
	}
}

// ---------- git ----------

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if !hasGit() {
		t.Skip("git not installed")
	}
	root := setup(t)
	gitT(t, root, "init", "-q", "-b", "main")
	write(t, filepath.Join(root, ".gitignore"), "*.log\n")
	write(t, filepath.Join(root, "tracked.txt"), "committed\n")
	gitT(t, root, "add", ".")
	gitT(t, root, "commit", "-q", "-m", "init")
	return root
}

func TestGitSnapshotLeavesIndexBranchAndTreeAlone(t *testing.T) {
	root := gitRepo(t)
	// the user has a staged change, an unstaged change and an untracked file
	write(t, filepath.Join(root, "staged.txt"), "staged\n")
	gitT(t, root, "add", "staged.txt")
	write(t, filepath.Join(root, "tracked.txt"), "unstaged edit\n")
	write(t, filepath.Join(root, "untracked.txt"), "u\n")
	idx := filepath.Join(root, ".git", "index")
	idxBefore, _ := os.ReadFile(idx)
	statusBefore := gitT(t, root, "status", "--porcelain")
	headBefore := gitT(t, root, "rev-parse", "HEAD")
	branchBefore := gitT(t, root, "symbolic-ref", "HEAD")
	stashBefore := gitT(t, root, "stash", "list")

	s := Open(root)
	if !s.Git() {
		t.Fatal("should see the git repo")
	}
	s.Begin("user", "x", "x", 0)
	s.End()

	idxAfter, _ := os.ReadFile(idx)
	if string(idxBefore) != string(idxAfter) {
		t.Error("the user's index changed")
	}
	if got := gitT(t, root, "status", "--porcelain"); got != statusBefore {
		t.Errorf("status changed:\n%s\nvs\n%s", got, statusBefore)
	}
	if gitT(t, root, "rev-parse", "HEAD") != headBefore || gitT(t, root, "symbolic-ref", "HEAD") != branchBefore {
		t.Error("HEAD or branch changed")
	}
	if gitT(t, root, "stash", "list") != stashBefore {
		t.Error("stash list changed")
	}
	refs := gitT(t, root, "for-each-ref", "refs/blaze/checkpoints/")
	if !strings.Contains(refs, "1-start") {
		t.Errorf("checkpoint ref missing: %q", refs)
	}
	// the snapshot holds the working tree, untracked file included
	if out := gitT(t, root, "show", s.List()[0].GitStart+":untracked.txt"); out != "u\n" {
		t.Errorf("snapshot content = %q", out)
	}
}

func TestGitTracksShellChangesAndRewinds(t *testing.T) {
	root := gitRepo(t)
	tracked, made, log := filepath.Join(root, "tracked.txt"), filepath.Join(root, "made-by-shell.txt"), filepath.Join(root, "notes.log")
	write(t, log, "ignored v1")
	s := Open(root)
	s.Begin("user", "run things", "run things", 0)
	// a shell command (not seen by Blaze's file tools)
	write(t, tracked, "changed by a command\n")
	write(t, made, "new\n")
	// and an agent edit of an ignored file (only the blob copy can restore it)
	change(s, log, func() { write(t, log, "ignored v2") })
	s.End()
	turn := s.List()[0]
	if n := len(turn.Changed(s.RepoTop)); n != 3 {
		t.Fatalf("files changed = %d (%v %v), want 3", n, turn.Order, turn.GitFiles)
	}
	p, r := rewind(t, s, 0, false)
	if len(p.Conflicts()) != 0 || len(r.Failed) != 0 {
		t.Fatalf("plan %+v result %+v", p.Items, r)
	}
	if read(t, tracked) != "committed\n" || read(t, made) != "<missing>" || read(t, log) != "ignored v1" {
		t.Fatalf("not restored: %q %q %q", read(t, tracked), read(t, made), read(t, log))
	}
	if gitT(t, root, "status", "--porcelain") != "" {
		t.Errorf("tree should be clean again: %s", gitT(t, root, "status", "--porcelain"))
	}
	s.DropFrom(0)
	if refs := gitT(t, root, "for-each-ref", "refs/blaze/"); refs != "" {
		t.Errorf("refs should be removed: %s", refs)
	}
}

func TestGitDetectsUserChangesOutsideBlaze(t *testing.T) {
	root := gitRepo(t)
	tracked := filepath.Join(root, "tracked.txt")
	s := Open(root)
	s.Begin("user", "x", "x", 0)
	write(t, tracked, "by a command\n")
	s.End()
	write(t, tracked, "the user's own edit\n")
	p, _ := s.PlanRestore(0)
	if cs := p.Conflicts(); len(cs) != 1 || cs[0] != tracked {
		t.Fatalf("conflicts = %v", cs)
	}
	s.Restore(p, false)
	if read(t, tracked) != "the user's own edit\n" {
		t.Fatal("must not overwrite without asking")
	}
}

func TestCodeOnlyRewindThenEarlier(t *testing.T) {
	root := setup(t)
	f := filepath.Join(root, "f.txt")
	write(t, f, "v0")
	s := Open(root)
	for i, v := range []string{"v1", "v2"} {
		s.Begin("user", v, v, i*2)
		change(s, f, func() { write(t, f, v) })
		s.End()
	}
	rewind(t, s, 1, false)
	s.CodeRewound(1)
	p, _ := s.PlanRestore(0)
	if len(p.Conflicts()) != 0 {
		t.Fatalf("Blaze's own restore is not a user change: %v", p.Conflicts())
	}
	s.Restore(p, false)
	if read(t, f) != "v0" {
		t.Fatalf("f = %q", read(t, f))
	}
}
