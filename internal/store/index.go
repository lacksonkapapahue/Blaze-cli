package store

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// ---------------- skills ----------------

// Skill is one indexed SKILL.md.
type Skill struct {
	Name, Desc, Path, Body string
	Mtime                  int64 // unix nanoseconds of the file
}

// SkillMtimes is path -> mtime for every indexed skill (to spot changes cheaply).
func (d *DB) SkillMtimes() map[string]int64 {
	out := map[string]int64{}
	rows, err := d.sql.Query(`SELECT path, mtime FROM skills`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var m int64
		if rows.Scan(&p, &m) == nil {
			out[p] = m
		}
	}
	return out
}

// ReplaceSkills makes the skills index exactly these skills.
func (d *DB) ReplaceSkills(sk []Skill) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM skills; DELETE FROM skills_fts;`); err != nil {
		return err
	}
	for _, s := range sk {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO skills(name, desc, path, body, mtime) VALUES(?,?,?,?,?)`, s.Name, s.Desc, s.Path, s.Body, s.Mtime); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO skills_fts(path, name, desc, body) VALUES(?,?,?,?)`, s.Path, s.Name+" "+strings.ReplaceAll(s.Name, "-", " "), s.Desc, s.Body); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Skills lists every indexed skill by name.
func (d *DB) Skills() []Skill {
	return d.skillRows(`SELECT name, desc, path, mtime FROM skills ORDER BY name`)
}

func (d *DB) skillRows(q string, a ...any) []Skill {
	rows, err := d.sql.Query(q, a...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Skill
	for rows.Next() {
		var s Skill
		if rows.Scan(&s.Name, &s.Desc, &s.Path, &s.Mtime) == nil {
			out = append(out, s)
		}
	}
	return out
}

// SearchSkills returns the skills that best match q (name and description weigh
// more than the body).
func (d *DB) SearchSkills(q string, limit int) []Skill {
	fq := ftsQuery(q, 32)
	if fq == "" {
		return nil
	}
	return d.skillRows(`SELECT s.name, s.desc, s.path, s.mtime FROM skills_fts f JOIN skills s ON s.path=f.path
		WHERE skills_fts MATCH ? ORDER BY bm25(skills_fts, 0, 10.0, 5.0, 1.0) LIMIT ?`, fq, limit)
}

// SkillBody is the indexed text of a skill.
func (d *DB) SkillBody(path string) string {
	var b string
	_ = d.sql.QueryRow(`SELECT body FROM skills WHERE path=?`, path).Scan(&b)
	return b
}

// ---------------- tools ----------------

// ToolRow is one tool in the index.
type ToolRow struct{ Name, Desc, Group string }

// SyncTools rebuilds the tools index when the set of tools changed.
func (d *DB) SyncTools(ts []ToolRow) error {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	h := sha1.New()
	for _, t := range ts {
		h.Write([]byte(t.Name + "\x00" + t.Desc + "\x00" + t.Group + "\x01"))
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if d.Meta("tools_hash") == sum {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM tools; DELETE FROM tools_fts;`); err != nil {
		return err
	}
	for _, t := range ts {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO tools(name, desc, grp) VALUES(?,?,?)`, t.Name, t.Desc, t.Group); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO tools_fts(key, name, desc, grp) VALUES(?,?,?,?)`, t.Name, t.Name+" "+strings.ReplaceAll(t.Name, "_", " "), t.Desc, t.Group); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('tools_hash',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, sum); err != nil {
		return err
	}
	return tx.Commit()
}

// SearchTools returns tool names matching q, best first.
func (d *DB) SearchTools(q string, limit int) []string {
	fq := ftsQuery(q, 32)
	if fq == "" {
		return nil
	}
	rows, err := d.sql.Query(`SELECT key FROM tools_fts WHERE tools_fts MATCH ? ORDER BY bm25(tools_fts, 0, 10.0, 3.0, 2.0) LIMIT ?`, fq, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	return out
}

// Activate turns tools on for one conversation (they stay on for its rest).
func (d *DB) Activate(conv string, names ...string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now().Unix()
	for _, n := range names {
		if _, err := d.sql.Exec(`INSERT INTO active_tools(conv, name, at) VALUES(?,?,?) ON CONFLICT(conv, name) DO UPDATE SET at=excluded.at`, conv, n, now); err != nil {
			return err
		}
	}
	return nil
}

// Active lists the tools a conversation turned on.
func (d *DB) Active(conv string) []string {
	rows, err := d.sql.Query(`SELECT name FROM active_tools WHERE conv=? ORDER BY name`, conv)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	return out
}
