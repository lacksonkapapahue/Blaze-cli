package store

import (
	"database/sql"
	"strings"
	"time"
)

// Memory is one saved fact.
type Memory struct {
	ID       int64
	Text     string
	Category string // "" = not sorted by a model yet
	Project  string
	Chat     string
	Created  time.Time
	Updated  time.Time
	Uses     int
	LastUsed time.Time
	Pinned   bool
}

const memCols = `id, text, category, project, chat, created, updated, uses, last_used, pinned`

func scanMems(rows *sql.Rows, err error) []Memory {
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		var c, u, lu int64
		var pin int
		if rows.Scan(&m.ID, &m.Text, &m.Category, &m.Project, &m.Chat, &c, &u, &m.Uses, &lu, &pin) != nil {
			continue
		}
		m.Created, m.Updated, m.Pinned = time.Unix(c, 0), time.Unix(u, 0), pin != 0
		if lu > 0 {
			m.LastUsed = time.Unix(lu, 0)
		}
		out = append(out, m)
	}
	return out
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

// Memories returns every memory, oldest first.
func (d *DB) Memories() []Memory {
	return scanMems(d.sql.Query(`SELECT ` + memCols + ` FROM memories ORDER BY created, id`))
}

// Memory returns one memory by id.
func (d *DB) Memory(id int64) (Memory, bool) {
	ms := scanMems(d.sql.Query(`SELECT `+memCols+` FROM memories WHERE id=?`, id))
	if len(ms) == 0 {
		return Memory{}, false
	}
	return ms[0], true
}

// CountMemories is how many memories are saved.
func (d *DB) CountMemories() int {
	var n int
	_ = d.sql.QueryRow(`SELECT count(*) FROM memories`).Scan(&n)
	return n
}

// Add result statuses.
const (
	Added   = "added"
	Exists  = "exists"  // the same text is already saved
	Updated = "updated" // a near-identical memory was replaced by the new wording
)

// similar is the Jaccard overlap of two texts' word sets.
func similar(a, b string) float64 {
	wa, wb := map[string]bool{}, map[string]bool{}
	for _, w := range strings.Fields(a) {
		wa[w] = true
	}
	for _, w := range strings.Fields(b) {
		wb[w] = true
	}
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	in := 0
	for w := range wa {
		if wb[w] {
			in++
		}
	}
	return float64(in) / float64(len(wa)+len(wb)-in)
}

// Near is the similarity at or above which two memories count as the same fact.
const Near = 0.85

// Add saves a fact, deduplicating: identical text (ignoring case, punctuation and
// spacing) is kept once, and a near-identical one (≥85% shared words) is replaced
// by the newer wording, keeping its id, history and category.
func (d *DB) Add(text, project, chat string, at time.Time) (int64, string, error) {
	text = clean(text)
	n := Norm(text)
	if at.IsZero() {
		at = time.Now()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var id int64
	if d.sql.QueryRow(`SELECT id FROM memories WHERE norm=? LIMIT 1`, n).Scan(&id) == nil {
		_, err := d.sql.Exec(`UPDATE memories SET updated=? WHERE id=?`, time.Now().Unix(), id)
		return id, Exists, err
	}
	if q := ftsQuery(text, 24); q != "" {
		rows, err := d.sql.Query(`SELECT m.id, m.norm FROM memories_fts f JOIN memories m ON m.id=f.rowid WHERE memories_fts MATCH ? ORDER BY bm25(memories_fts) LIMIT 5`, q)
		if err == nil {
			var best int64
			bs := 0.0
			for rows.Next() {
				var cid int64
				var cn string
				if rows.Scan(&cid, &cn) == nil {
					if s := similar(n, cn); s > bs {
						best, bs = cid, s
					}
				}
			}
			rows.Close()
			if bs >= Near {
				_, err := d.sql.Exec(`UPDATE memories SET text=?, norm=?, updated=? WHERE id=?`, text, n, time.Now().Unix(), best)
				return best, Updated, err
			}
		}
	}
	return d.insertNew(text, n, project, chat, at.Unix(), time.Now().Unix())
}

// insertNew inserts a memory unless one with the same norm exists, in a single
// statement: another process (Desktop) writing the same fact at the same moment
// can't slip a duplicate in between a SELECT and an INSERT.
func (d *DB) insertNew(text, n, project, chat string, created, updated int64) (int64, string, error) {
	r, err := d.sql.Exec(`INSERT INTO memories(text, norm, project, chat, created, updated)
		SELECT ?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM memories WHERE norm=?)`, text, n, project, chat, created, updated, n)
	if err != nil {
		return 0, "", err
	}
	if k, _ := r.RowsAffected(); k == 0 {
		var id int64
		err := d.sql.QueryRow(`SELECT id FROM memories WHERE norm=? LIMIT 1`, n).Scan(&id)
		return id, Exists, err
	}
	id, _ := r.LastInsertId()
	return id, Added, nil
}

// Edit changes a memory's text, keeping its id and metadata.
func (d *DB) Edit(id int64, text string) error {
	text = clean(text)
	d.mu.Lock()
	defer d.mu.Unlock()
	r, err := d.sql.Exec(`UPDATE memories SET text=?, norm=?, updated=? WHERE id=?`, text, Norm(text), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes one memory.
func (d *DB) Delete(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, err := d.sql.Exec(`DELETE FROM memories WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ForgetMatching removes every memory whose text contains s (case-insensitive)
// and returns what it removed.
func (d *DB) ForgetMatching(s string) []Memory {
	s = strings.ToLower(clean(s))
	if s == "" {
		return nil
	}
	var gone []Memory
	for _, m := range d.Memories() {
		if strings.Contains(strings.ToLower(m.Text), s) {
			if d.Delete(m.ID) == nil {
				gone = append(gone, m)
			}
		}
	}
	return gone
}

// SetCategory sets (or with "" clears) a memory's category.
func (d *DB) SetCategory(id int64, cat string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(`UPDATE memories SET category=? WHERE id=?`, strings.TrimSpace(cat), id)
	return err
}

// SetPinned pins a memory (pinned memories go into every prompt).
func (d *DB) SetPinned(id int64, on bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := 0
	if on {
		v = 1
	}
	_, err := d.sql.Exec(`UPDATE memories SET pinned=? WHERE id=?`, v, id)
	return err
}

// Search returns the memories that best match q (BM25), best first.
func (d *DB) Search(q string, limit int) []Memory {
	fq := ftsQuery(q, 32)
	if fq == "" {
		return nil
	}
	return scanMems(d.sql.Query(`SELECT m.id, m.text, m.category, m.project, m.chat, m.created, m.updated, m.uses, m.last_used, m.pinned
		FROM memories_fts f JOIN memories m ON m.id=f.rowid WHERE memories_fts MATCH ? ORDER BY bm25(memories_fts), m.uses DESC LIMIT ?`, fq, limit))
}

// Touch records that memories were shown to the model.
func (d *DB) Touch(ids []int64) {
	if len(ids) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now().Unix()
	for _, id := range ids {
		_, _ = d.sql.Exec(`UPDATE memories SET uses=uses+1, last_used=? WHERE id=?`, now, id)
	}
}

// ForPrompt picks the memories for one turn: every pinned one, then the best
// matches for q (up to max), topped up with the most used/recent ones when q
// matches little, all within a token budget. The picked rows are touched.
func (d *DB) ForPrompt(q string, max, budget int) []Memory {
	seen := map[int64]bool{}
	var out []Memory
	used := 0
	add := func(ms []Memory, limit int) {
		for _, m := range ms {
			if limit >= 0 && len(out) >= limit {
				return
			}
			if seen[m.ID] {
				continue
			}
			t := Tokens(m.Text) + 2
			if used+t > budget {
				continue
			}
			seen[m.ID] = true
			used += t
			out = append(out, m)
		}
	}
	add(scanMems(d.sql.Query(`SELECT `+memCols+` FROM memories WHERE pinned=1 ORDER BY created, id`)), -1)
	pinned := len(out)
	add(d.Search(q, max*2), pinned+max)
	if len(out)-pinned < 3 {
		add(scanMems(d.sql.Query(`SELECT `+memCols+` FROM memories ORDER BY uses DESC, updated DESC LIMIT 8`)), pinned+3)
	}
	ids := make([]int64, 0, len(out))
	for _, m := range out {
		ids = append(ids, m.ID)
	}
	d.Touch(ids)
	return out
}

// ReplaceAll makes the memories exactly the given lines (the Settings textarea):
// lines whose text is already saved keep their row, new ones are added, missing
// ones are removed.
func (d *DB) ReplaceAll(texts []string) {
	want := map[string]bool{}
	for _, t := range texts {
		if n := Norm(t); n != "" {
			want[n] = true
		}
	}
	have := map[string]bool{}
	for _, m := range d.Memories() {
		n := Norm(m.Text)
		if !want[n] || have[n] {
			_ = d.Delete(m.ID)
			continue
		}
		have[n] = true
	}
	for _, t := range texts {
		if n := Norm(t); n != "" && !have[n] {
			have[n] = true
			d.mu.Lock()
			now := time.Now().Unix()
			_, _, _ = d.insertNew(clean(t), n, "", "", now, now)
			d.mu.Unlock()
		}
	}
}
