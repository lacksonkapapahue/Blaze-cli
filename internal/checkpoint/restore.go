package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Item is one file a rewind would put back.
type Item struct {
	Path     string // absolute
	Delete   bool   // the file didn't exist then: remove it
	Conflict bool   // changed outside Blaze since the checkpoint
	TooBig   bool   // existed but was too big to keep a copy: can't be restored

	blob    string      // agent snapshot content (hash)
	tree    string      // or: git tree to read it from
	mode    os.FileMode //
	newDir  string      // folder the agent created for it (removed when left empty)
	fromGit bool
}

// Plan is what rewinding to a checkpoint would do to the files.
type Plan struct {
	Turn     *Turn
	Items    []Item
	Git      bool // shell changes are covered too
	Warnings []string
}

// Conflicts lists the files changed outside Blaze since the checkpoint.
func (p *Plan) Conflicts() []string {
	var out []string
	for _, it := range p.Items {
		if it.Conflict {
			out = append(out, it.Path)
		}
	}
	return out
}

// Restorable counts the files a restore would write or delete.
func (p *Plan) Restorable(overwrite bool) int {
	n := 0
	for _, it := range p.Items {
		if it.TooBig || (it.Conflict && !overwrite) {
			continue
		}
		n++
	}
	return n
}

// PlanRestore works out how to put the files back to how they were when turn i
// started.
func (s *Session) PlanRestore(i int) (*Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.Turns) {
		return nil, errors.New("no such checkpoint")
	}
	later := s.Turns[i:]
	plan := &Plan{Turn: later[0]}
	items := map[string]*Item{}

	// 1. Blaze's own file changes: the earliest saved copy wins.
	last := map[string]*Snap{}       // the last turn's snapshot for each path
	userTouched := map[string]bool{} // changed between Blaze's writes
	for _, t := range later {
		for _, p := range t.Order {
			sn := t.Files[p]
			if prev := last[p]; prev != nil && prev.Done && prev.After != sn.Before {
				userTouched[p] = true
			}
			last[p] = sn
			if _, ok := items[p]; ok {
				continue
			}
			it := &Item{Path: p, Delete: !sn.Before.Exists, TooBig: sn.TooBig, blob: sn.Before.Hash, mode: sn.Mode, newDir: sn.NewDir}
			items[p] = it
		}
	}
	for p, sn := range last {
		if sn.Done && stateOf(p) != sn.After {
			userTouched[p] = true
		}
	}

	// 2. With git: what shell commands changed, from the turn snapshots.
	start := later[0].GitStart
	if s.RepoTop != "" && start != "" {
		plan.Git = true
		now, err := snapshotTree(s.RepoTop, s.dir)
		if err != nil {
			plan.Git = false
			plan.Warnings = append(plan.Warnings, "couldn't read the git snapshot ("+err.Error()+"); only Blaze's own file edits can be put back")
		} else {
			// Files Blaze edited that git sees: the turn's start snapshot is the exact
			// state then (a command may have changed them before Blaze's edit).
			var rels []string
			for p := range items {
				if rel, err := filepath.Rel(s.RepoTop, p); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
					rels = append(rels, filepath.ToSlash(rel))
				}
			}
			ign := ignored(s.RepoTop, rels)
			for _, r := range rels {
				if ign[r] {
					continue
				}
				ok, mode, err := treeEntry(s.RepoTop, start, r)
				if err != nil {
					continue
				}
				it := items[filepath.Join(s.RepoTop, filepath.FromSlash(r))]
				it.tree, it.fromGit, it.Delete, it.TooBig = start, true, !ok, false
				if ok {
					it.mode = mode
				}
			}
			outside := map[string]bool{}
			for k, t := range later {
				end := t.GitEnd
				if end == "" {
					end = now
					if k+1 < len(later) && later[k+1].GitStart != "" {
						end = later[k+1].GitStart
					}
				}
				changed := t.GitFiles
				if t.GitEnd == "" && t.GitStart != "" {
					changed, _ = diffTrees(s.RepoTop, t.GitStart, end)
				}
				for _, r := range changed {
					abs := filepath.Join(s.RepoTop, filepath.FromSlash(r))
					if _, ok := items[abs]; ok {
						continue
					}
					it := &Item{Path: abs, tree: start, fromGit: true}
					ok, mode, err := treeEntry(s.RepoTop, start, r)
					if err != nil {
						continue
					}
					it.Delete, it.mode = !ok, mode
					items[abs] = it
				}
				// between this turn's end and the next turn's start only the user worked
				next := now
				if k+1 < len(later) {
					next = later[k+1].GitStart
				}
				if t.GitEnd != "" && next != "" {
					ps, _ := diffTrees(s.RepoTop, t.GitEnd, next)
					for _, r := range ps {
						outside[filepath.Join(s.RepoTop, filepath.FromSlash(r))] = true
					}
				}
			}
			for p := range outside {
				userTouched[p] = true
			}
		}
	} else if s.RepoTop == "" {
		plan.Warnings = append(plan.Warnings, "this folder isn't a git repo, so only Blaze's own file edits can be put back; changes made by commands it ran aren't tracked")
	}

	for p, it := range items {
		it.Conflict = userTouched[p]
		if it.Delete && it.fromGit {
			if _, err := os.Stat(p); err != nil {
				delete(items, p) // already gone
				continue
			}
		}
		plan.Items = append(plan.Items, *it)
	}
	sort.Slice(plan.Items, func(a, b int) bool { return plan.Items[a].Path < plan.Items[b].Path })
	for _, it := range plan.Items {
		if it.TooBig {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s was over %d MB, so no copy was kept; it stays as it is", it.Path, MaxFileSize>>20))
		}
	}
	return plan, nil
}

// Result is what a restore did.
type Result struct {
	Restored []string
	Deleted  []string
	Skipped  []string // conflicts left alone, or too big
	Failed   []string
}

// N is how many files were put back (written or deleted).
func (r Result) N() int { return len(r.Restored) + len(r.Deleted) }

// Restore applies a plan. overwrite also puts back files changed outside Blaze.
func (s *Session) Restore(p *Plan, overwrite bool) Result {
	var r Result
	for _, it := range p.Items {
		if it.TooBig || (it.Conflict && !overwrite) {
			r.Skipped = append(r.Skipped, it.Path)
			continue
		}
		if it.Delete {
			err := os.Remove(it.Path)
			if err != nil && !os.IsNotExist(err) {
				r.Failed = append(r.Failed, it.Path+": "+err.Error())
				continue
			}
			s.tidy(filepath.Dir(it.Path), it.newDir)
			r.Deleted = append(r.Deleted, it.Path)
			continue
		}
		var data []byte
		var err error
		if it.fromGit {
			rel, _ := filepath.Rel(s.RepoTop, it.Path)
			data, err = treeFile(s.RepoTop, it.tree, filepath.ToSlash(rel))
		} else {
			data, err = s.blob(it.blob)
			if err == nil && hashBytes(data) != it.blob {
				err = errors.New("the saved copy is damaged")
			}
		}
		if err != nil {
			r.Failed = append(r.Failed, it.Path+": "+err.Error())
			continue
		}
		mode := it.mode
		if mode == 0 {
			mode = 0o644
		}
		if st, err := os.Stat(it.Path); err == nil && st.IsDir() {
			r.Failed = append(r.Failed, it.Path+": a folder is there now")
			continue
		}
		if err := os.MkdirAll(filepath.Dir(it.Path), 0o755); err != nil {
			r.Failed = append(r.Failed, it.Path+": "+err.Error())
			continue
		}
		if err := os.WriteFile(it.Path, data, mode); err != nil {
			r.Failed = append(r.Failed, it.Path+": "+err.Error())
			continue
		}
		_ = os.Chmod(it.Path, mode)
		r.Restored = append(r.Restored, it.Path)
	}
	return r
}

// tidy removes folders Blaze created that are empty now, from dir up to top.
func (s *Session) tidy(dir, top string) {
	if top == "" {
		return
	}
	for d := dir; ; d = filepath.Dir(d) {
		if !strings.HasPrefix(d, top) {
			return
		}
		if os.Remove(d) != nil { // only empty folders go
			return
		}
		if d == top || filepath.Dir(d) == d {
			return
		}
	}
}
