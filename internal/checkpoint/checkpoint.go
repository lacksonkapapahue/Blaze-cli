// Package checkpoint lets the user rewind what Blaze did, like Claude Code's
// checkpoints (v0.8.0).
//
// Every user turn (one message and everything Blaze did for it) is a checkpoint:
//
//   - Before Blaze writes, edits or deletes a file (file_write, file_edit,
//     file_delete, or applying a specialist's changes) the file's prior content, or
//     the fact that it didn't exist, is saved once per turn. Contents are kept as
//     content-addressed, gzipped blobs, so the same text is stored once. Files over
//     5 MB are noted but not copied.
//   - When the folder is a git repo, a lightweight snapshot of the whole working
//     tree is also taken at the start and end of each turn: a git tree written with
//     a private index file and kept under refs/blaze/checkpoints/<session>/. The
//     user's index, branch and working tree are never touched. This is what lets a
//     rewind also undo what shell commands changed. Without git, only Blaze's own
//     file edits are tracked.
//
// The store lives in ~/.blaze/cli/checkpoints/<session>/ (BLAZE_HOME respected);
// sessions untouched for 30 days are pruned, refs included.
package checkpoint

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

// Limits.
var (
	MaxFileSize = int64(5 << 20)      // bigger files are noted, not copied
	KeepFor     = 30 * 24 * time.Hour // sessions older than this are pruned
	MaxDirFiles = 5000                // files saved when Blaze deletes a whole folder
)

// Dir is where every session's checkpoints live.
func Dir() string { return filepath.Join(config.Dir(), "cli", "checkpoints") }

// State is what a path held: nothing, or content with this sha256.
type State struct {
	Exists bool   `json:"exists"`
	Hash   string `json:"hash,omitempty"`
}

// Snap is one file's prior content in a turn.
type Snap struct {
	Before State       `json:"before"`
	Mode   fs.FileMode `json:"mode,omitempty"`
	TooBig bool        `json:"too_big,omitempty"` // existed but over MaxFileSize: no copy kept
	NewDir string      `json:"new_dir,omitempty"` // the top folder that didn't exist yet (created by the write)
	After  State       `json:"after"`             // what Blaze left there
	Done   bool        `json:"done,omitempty"`    // After is known
}

// Turn is one checkpoint: a user message and what Blaze changed for it.
type Turn struct {
	ID        int              `json:"id"`
	Time      time.Time        `json:"time"`
	Kind      string           `json:"kind"`   // user | agent | auto
	Prompt    string           `json:"prompt"` // what the user typed (put back on chat restore)
	SentHash  string           `json:"sent"`   // hash of the full message sent to the model
	HistIndex int              `json:"hist"`   // where the message sat in the conversation (absolute)
	Files     map[string]*Snap `json:"files,omitempty"`
	Order     []string         `json:"order,omitempty"`
	GitStart  string           `json:"git_start,omitempty"`
	GitEnd    string           `json:"git_end,omitempty"`
	GitFiles  []string         `json:"git_files,omitempty"` // repo-relative paths that changed during the turn
	ChatGone  bool             `json:"chat_gone,omitempty"` // the chat was compacted or cleared: code only
	Ended     bool             `json:"ended,omitempty"`
}

// Changed is every file the turn changed (absolute paths, sorted).
func (t *Turn) Changed(repoTop string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range t.Order {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if repoTop != "" {
		for _, r := range t.GitFiles {
			p := filepath.Join(repoTop, filepath.FromSlash(r))
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Session is one chat's checkpoints.
type Session struct {
	ID      string    `json:"id"`
	Root    string    `json:"root"`
	RepoTop string    `json:"repo_top,omitempty"` // "" = not a git repo (or git missing)
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Turns   []*Turn   `json:"turns"`
	NextID  int       `json:"next_id"`

	dir string
	mu  sync.Mutex
	cur *Turn
}

// folderKey ties sessions to the folder Blaze runs in.
func folderKey(root string) string {
	h := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(h[:6])
}

// Open starts a new session for root (and prunes old ones).
func Open(root string) *Session {
	Prune(KeepFor)
	now := time.Now()
	s := &Session{
		ID:      folderKey(root) + "-" + now.Format("20060102-150405") + fmt.Sprintf("-%03d", now.Nanosecond()/1e6),
		Root:    root,
		RepoTop: repoTop(root),
		Created: now,
		Updated: now,
		NextID:  1,
	}
	s.dir = filepath.Join(Dir(), s.ID)
	return s
}

// Latest reopens this folder's newest session (for /resume and blaze -c).
func Latest(root string) (*Session, error) {
	ents, err := os.ReadDir(Dir())
	if err != nil {
		return nil, err
	}
	prefix := folderKey(root) + "-"
	var names []string
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, errors.New("no checkpoints for this folder")
	}
	sort.Strings(names)
	return load(filepath.Join(Dir(), names[len(names)-1]))
}

func load(dir string) (*Session, error) {
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, err
	}
	s := &Session{}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	s.dir = dir
	if s.RepoTop != "" && repoTop(s.Root) != s.RepoTop {
		s.RepoTop = "" // the repo is gone: file snapshots still work
	}
	return s, nil
}

// Git reports whether shell changes are tracked too (the folder is a git repo).
func (s *Session) Git() bool { return s != nil && s.RepoTop != "" }

// Path is the session's folder.
func (s *Session) Path() string { return s.dir }

func (s *Session) save() {
	if len(s.Turns) == 0 && s.cur == nil {
		return // nothing worth a folder yet
	}
	s.Updated = time.Now()
	_ = os.MkdirAll(s.dir, 0o700)
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	tmp := filepath.Join(s.dir, "index.json.tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(s.dir, "index.json"))
	}
}

// HashText is the hash stored for a sent message (to find it in the history later).
func HashText(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:12])
}

// Begin starts a checkpoint for a new turn. histIndex is where the message will sit
// in the conversation.
func (s *Session) Begin(kind, typed, sent string, histIndex int) *Turn {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		s.endLocked()
	}
	t := &Turn{ID: s.NextID, Time: time.Now(), Kind: kind, Prompt: typed, SentHash: HashText(sent), HistIndex: histIndex, Files: map[string]*Snap{}}
	s.NextID++
	if s.RepoTop != "" {
		if tree, err := snapshotTree(s.RepoTop, s.dir); err == nil {
			t.GitStart = tree
			_ = setRef(s.RepoTop, s.ref(t.ID, "start"), tree)
		}
	}
	s.Turns = append(s.Turns, t)
	s.cur = t
	s.save()
	return t
}

func (s *Session) ref(id int, which string) string {
	return fmt.Sprintf("refs/blaze/checkpoints/%s/%d-%s", s.ID, id, which)
}

// Before saves the prior content of paths (once per turn) ahead of a change Blaze
// is about to make. A folder saves every file inside it.
func (s *Session) Before(paths []string) {
	if s == nil || len(paths) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.cur
	if t == nil {
		return
	}
	for _, p := range paths {
		p = filepath.Clean(p)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			n := 0
			_ = filepath.WalkDir(p, func(f string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || !d.Type().IsRegular() {
					return nil
				}
				if n++; n > MaxDirFiles {
					return filepath.SkipAll
				}
				s.beforeOne(t, f)
				return nil
			})
			continue
		}
		s.beforeOne(t, p)
	}
	s.save()
}

func (s *Session) beforeOne(t *Turn, p string) {
	if _, ok := t.Files[p]; ok {
		return
	}
	sn := &Snap{}
	st, err := os.Stat(p)
	switch {
	case err != nil:
		sn.NewDir = topMissing(filepath.Dir(p))
	case st.IsDir():
		return
	default:
		sn.Mode = st.Mode().Perm()
		h, err := hashFile(p)
		if err != nil {
			return // unreadable: nothing Blaze could restore anyway
		}
		sn.Before = State{Exists: true, Hash: h}
		if st.Size() > MaxFileSize {
			sn.TooBig = true
		} else if err := s.putBlob(p, h); err != nil {
			sn.TooBig = true
		}
	}
	t.Files[p] = sn
	t.Order = append(t.Order, p)
}

// After records what Blaze left at paths, so a later rewind can tell whether the
// user changed them since.
func (s *Session) After(paths []string) {
	if s == nil || len(paths) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.cur
	if t == nil {
		return
	}
	for _, p := range paths {
		p = filepath.Clean(p)
		for _, f := range t.Order {
			if f == p || strings.HasPrefix(f, p+string(filepath.Separator)) {
				sn := t.Files[f]
				sn.After, sn.Done = stateOf(f), true
			}
		}
	}
	s.save()
}

// End closes the current turn: the end snapshot, and file entries Blaze didn't
// actually change are dropped.
func (s *Session) End() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endLocked()
	s.save()
}

func (s *Session) endLocked() {
	t := s.cur
	if t == nil {
		return
	}
	s.cur = nil
	var keep []string
	for _, p := range t.Order {
		sn := t.Files[p]
		if !sn.Done {
			sn.After, sn.Done = stateOf(p), true
		}
		if sn.After == sn.Before {
			delete(t.Files, p)
			continue
		}
		keep = append(keep, p)
	}
	t.Order = keep
	if s.RepoTop != "" && t.GitStart != "" {
		if tree, err := snapshotTree(s.RepoTop, s.dir); err == nil {
			t.GitEnd = tree
			if tree != t.GitStart {
				_ = setRef(s.RepoTop, s.ref(t.ID, "end"), tree)
				t.GitFiles, _ = diffTrees(s.RepoTop, t.GitStart, tree)
			}
		}
	}
	t.Ended = true
}

// Discard drops a turn that never reached the conversation (the model call failed
// before anything happened). It keeps the turn when files changed.
func (s *Session) Discard(t *Turn) {
	if s == nil || t == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(t.Order) > 0 || len(t.GitFiles) > 0 {
		t.ChatGone = true
		s.save()
		return
	}
	for i, x := range s.Turns {
		if x == t {
			s.Turns = append(s.Turns[:i], s.Turns[i+1:]...)
			s.dropRefs([]*Turn{t})
			break
		}
	}
	s.save()
}

// List is the checkpoints, oldest first.
func (s *Session) List() []*Turn {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Turn(nil), s.Turns...)
}

// Index finds a turn by id (-1 when gone).
func (s *Session) Index(id int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.Turns {
		if t.ID == id {
			return i
		}
	}
	return -1
}

// ChatGone marks every checkpoint as code-only (the conversation was compacted).
func (s *Session) ChatGone() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.Turns {
		t.ChatGone = true
	}
	s.save()
}

// DropFrom forgets checkpoint i and every later one (after a chat rewind).
func (s *Session) DropFrom(i int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.Turns) {
		return
	}
	s.dropRefs(s.Turns[i:])
	s.Turns = s.Turns[:i]
	s.save()
}

func (s *Session) dropRefs(ts []*Turn) {
	if s.RepoTop == "" {
		return
	}
	for _, t := range ts {
		_ = delRef(s.RepoTop, s.ref(t.ID, "start"))
		_ = delRef(s.RepoTop, s.ref(t.ID, "end"))
	}
}

// ---------- blobs ----------

func (s *Session) blobPath(h string) string { return filepath.Join(s.dir, "blobs", h[:2], h) }

func (s *Session) putBlob(src, h string) error {
	dst := s.blobPath(h)
	if _, err := os.Stat(dst); err == nil {
		return nil // already stored: content-addressed
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	_, err = io.Copy(zw, in)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func (s *Session) blob(h string) ([]byte, error) {
	f, err := os.Open(s.blobPath(h))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if _, err := io.Copy(&b, zr); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// ---------- files ----------

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func stateOf(p string) State {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return State{}
	}
	h, err := hashFile(p)
	if err != nil {
		return State{Exists: true, Hash: "?"}
	}
	return State{Exists: true, Hash: h}
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// topMissing is the highest folder in dir's chain that doesn't exist yet.
func topMissing(dir string) string {
	top := ""
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		}
		top = d
		if filepath.Dir(d) == d {
			break
		}
	}
	return top
}

// ---------- pruning ----------

// Prune removes sessions not touched for older (and their git refs).
func Prune(older time.Duration) int {
	ents, err := os.ReadDir(Dir())
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(Dir(), e.Name())
		when := time.Time{}
		s, err := load(dir)
		if err == nil {
			when = s.Updated
		} else if info, err := e.Info(); err == nil {
			when = info.ModTime()
		}
		if time.Since(when) < older {
			continue
		}
		if s != nil && s.RepoTop != "" {
			s.dropRefs(s.Turns)
		}
		if os.RemoveAll(dir) == nil {
			n++
		}
	}
	return n
}

// CodeRewound records that the code was put back to before turn i while the chat
// kept its messages: those turns no longer hold file changes, so a later rewind
// doesn't mistake Blaze's own restore for the user's edits.
func (s *Session) CodeRewound(i int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.Turns) {
		return
	}
	start := s.Turns[i].GitStart
	for k, t := range s.Turns[i:] {
		if s.RepoTop != "" {
			_ = delRef(s.RepoTop, s.ref(t.ID, "end"))
			if k > 0 {
				_ = delRef(s.RepoTop, s.ref(t.ID, "start"))
			}
		}
		t.Files, t.Order, t.GitFiles = map[string]*Snap{}, nil, nil
		t.GitStart, t.GitEnd = start, start
	}
	s.save()
}
