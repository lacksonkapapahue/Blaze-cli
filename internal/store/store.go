// Package store is Blaze's local database (~/.blaze/blaze.db): memories, an index
// of skills and an index of tools, each with an FTS5 full-text table so a turn
// only pulls the rows it needs instead of pasting everything into the prompt.
//
// It uses modernc.org/sqlite (pure Go, no cgo) so Windows and macOS cross-builds
// keep working. The skills folder stays the source of truth for skills (it is
// shared with the Android app and the CLI); the tools table is rebuilt from the
// Go registry. Only memories live here for real.
//
// SYNC NOTE (Blaze CLI): this package is a vendor copy of Blaze Desktop's
// internal/store (github.com/lacksonkapapahue/Blaze-dekstop, v0.7.0, schema v1).
// The CLI and Desktop open the SAME file, so the schema (migrations, tables,
// FTS tokenizers, triggers) must stay byte-for-byte identical in both repos.
// When Desktop adds a migration, copy it here in the same position and bump
// nothing else. CLI-only differences, all safe for Desktop to read:
//   - Open refuses a database whose user_version is newer than this copy knows
//     (ErrNewerSchema) instead of using tables it does not understand, and
//     checks the v1 tables are really there before touching anything.
//   - migrations run inside BEGIN IMMEDIATE and re-read user_version under the
//     write lock, so two apps starting at once never apply a migration twice.
//   - inserts that dedupe on norm do it in one statement (INSERT ... WHERE NOT
//     EXISTS), so two processes writing the same fact keep one row.
//   - the memory.md backup never overwrites an older memory.md.migrated.
//   - the CLI never calls SyncTools: the tools index is Desktop's registry.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "modernc.org/sqlite"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

// DB is an open blaze.db.
type DB struct {
	sql  *sql.DB
	Path string
	mu   sync.Mutex // serialises writes (SQLite allows one writer)
}

var (
	defMu  sync.Mutex
	defDB  *DB
	defErr error
)

// Default opens (once) config.Dir()/blaze.db, migrating an old memory.md on the
// way. It reopens when the config dir changes (tests point BLAZE_HOME elsewhere).
func Default() (*DB, error) {
	defMu.Lock()
	defer defMu.Unlock()
	p := filepath.Join(config.Dir(), "blaze.db")
	if defDB != nil && defDB.Path == p {
		return defDB, nil
	}
	if defDB != nil {
		_ = defDB.Close()
	}
	_ = os.MkdirAll(config.Dir(), 0o700)
	defDB, defErr = Open(p)
	if defErr == nil {
		_, _ = defDB.ImportMarkdown(filepath.Join(config.Dir(), "memory.md"))
	}
	return defDB, defErr
}

// Open opens or creates a database file and brings its schema up to date.
func Open(path string) (*DB, error) {
	// WAL lets Desktop read while the CLI writes (and the other way round);
	// busy_timeout waits up to 5 s for the other app's write lock instead of
	// failing; _txlock=immediate takes the write lock at BEGIN so a transaction
	// never has to upgrade a read lock mid-way (the classic SQLITE_BUSY deadlock).
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_txlock=immediate"
	s, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s.SetMaxOpenConns(1) // one connection: no "database is locked" between our own goroutines
	d := &DB{sql: s, Path: path}
	if err := d.migrate(); err != nil {
		s.Close()
		return nil, fmt.Errorf("blaze.db: %w", err)
	}
	return d, nil
}

func (d *DB) Close() error { return d.sql.Close() }

// ---------------- schema ----------------

var migrations = []string{
	// v1
	`CREATE TABLE memories(
		id INTEGER PRIMARY KEY,
		text TEXT NOT NULL,
		norm TEXT NOT NULL,
		category TEXT NOT NULL DEFAULT '',
		project TEXT NOT NULL DEFAULT '',
		chat TEXT NOT NULL DEFAULT '',
		created INTEGER NOT NULL,
		updated INTEGER NOT NULL,
		uses INTEGER NOT NULL DEFAULT 0,
		last_used INTEGER NOT NULL DEFAULT 0,
		pinned INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX memories_norm ON memories(norm);
	CREATE VIRTUAL TABLE memories_fts USING fts5(text, content='memories', content_rowid='id', tokenize='porter unicode61');
	CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
		INSERT INTO memories_fts(rowid, text) VALUES (new.id, new.text);
	END;
	CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
		INSERT INTO memories_fts(memories_fts, rowid, text) VALUES ('delete', old.id, old.text);
	END;
	CREATE TRIGGER memories_au AFTER UPDATE OF text ON memories BEGIN
		INSERT INTO memories_fts(memories_fts, rowid, text) VALUES ('delete', old.id, old.text);
		INSERT INTO memories_fts(rowid, text) VALUES (new.id, new.text);
	END;
	CREATE TABLE skills(name TEXT NOT NULL, desc TEXT NOT NULL DEFAULT '', path TEXT PRIMARY KEY, body TEXT NOT NULL DEFAULT '', mtime INTEGER NOT NULL DEFAULT 0);
	CREATE VIRTUAL TABLE skills_fts USING fts5(path UNINDEXED, name, desc, body, tokenize='porter unicode61');
	CREATE TABLE tools(name TEXT PRIMARY KEY, desc TEXT NOT NULL DEFAULT '', grp TEXT NOT NULL DEFAULT '');
	CREATE VIRTUAL TABLE tools_fts USING fts5(key UNINDEXED, name, desc, grp, tokenize='porter unicode61');
	CREATE TABLE active_tools(conv TEXT NOT NULL, name TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY(conv, name));
	CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);`,
}

// ErrNewerSchema means blaze.db was written by a newer Blaze (Desktop or CLI)
// than this one. The CLI leaves it untouched rather than guess.
var ErrNewerSchema = errors.New("blaze.db was created by a newer Blaze; update the CLI (blaze update) to use memory")

// SchemaVersion is the newest schema this copy knows.
func SchemaVersion() int { return len(migrations) }

func (d *DB) migrate() error {
	var v int
	if err := d.sql.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("%w (schema v%d, this build knows v%d)", ErrNewerSchema, v, len(migrations))
	}
	for v < len(migrations) {
		// BEGIN IMMEDIATE (via _txlock) holds the write lock; re-read the version
		// under it in case the other app migrated while we waited.
		tx, err := d.sql.Begin()
		if err != nil {
			return err
		}
		if err := tx.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
			tx.Rollback()
			return err
		}
		if v >= len(migrations) {
			tx.Rollback()
			break
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		v++
	}
	if v > len(migrations) {
		return fmt.Errorf("%w (schema v%d, this build knows v%d)", ErrNewerSchema, v, len(migrations))
	}
	if err := d.checkTables(); err != nil {
		return err
	}
	// Forget tool activations of chats nobody touched for a month.
	_, _ = d.sql.Exec(`DELETE FROM active_tools WHERE at < ?`, time.Now().Add(-30*24*time.Hour).Unix())
	return nil
}

// checkTables makes sure a database that claims schema v1 really has the v1
// tables, so a damaged or foreign file is refused instead of written to.
func (d *DB) checkTables() error {
	for _, t := range []string{"memories", "memories_fts", "skills", "skills_fts", "tools", "tools_fts", "active_tools", "meta"} {
		var n int
		if err := d.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name=?`, t).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("schema v%d is missing table %s; not touching it", d.Version(), t)
		}
	}
	return nil
}

// Version is the schema version (PRAGMA user_version).
func (d *DB) Version() int {
	var v int
	_ = d.sql.QueryRow(`PRAGMA user_version`).Scan(&v)
	return v
}

// ---------------- meta ----------------

func (d *DB) Meta(key string) string {
	var v string
	_ = d.sql.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	return v
}

func (d *DB) SetMeta(key, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---------------- text helpers ----------------

// Norm is the comparison form of a text: lower case, words only, single spaces.
func Norm(s string) string {
	f := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r)) })
	return strings.Join(f, " ")
}

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by can could did do does for from had has have he her his how i if in into is it its just me my no not of on or our she so than that the their them then there these they this to too up us was we were what when where which who why will with would you your yes ok okay please thanks thank hi hello hey can't don't i'm it's let lets make want need get got use using also very really some any all about`) {
		stop[w] = true
	}
}

// ftsQuery turns free text into a safe FTS5 query: the most recent distinct
// non-stopword terms, quoted, OR-ed (BM25 ranks rows matching more of them first).
func ftsQuery(q string, max int) string {
	words := strings.Fields(Norm(q))
	seen := map[string]bool{}
	var terms []string
	for i := len(words) - 1; i >= 0 && len(terms) < max; i-- { // newest words win
		w := words[i]
		if len(w) < 2 || stop[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, `"`+w+`"`)
	}
	return strings.Join(terms, " OR ")
}

// Tokens approximates a text's token count (≈4 characters per token).
func Tokens(s string) int { return (len(s) + 3) / 4 }

var ErrNotFound = errors.New("That memory changed or was removed. Reopen the graph.")
