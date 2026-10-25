// Package store persists inspection runs and their judgement results in
// SQLite (pure-Go modernc driver, WAL). It keeps findings, problem groups and
// host snapshots with evidence values only; raw time series are never stored.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is safe for concurrent use. Several processes may open the same file.
type Store struct {
	db   *sql.DB
	path string
}

// Open creates the database file (and its directory) if needed, enables WAL
// and a busy timeout, and applies pending migrations.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("store: empty database path")
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create directory: %w", err)
		}
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	// Write transactions take the lock up front so concurrent processes wait on
	// busy_timeout instead of failing with a lock upgrade error.
	q.Set("_txlock", "immediate")
	dsn := "file:" + filepath.ToSlash(path) + "?" + q.Encode()
	if filepath.IsAbs(path) && !strings.HasPrefix(filepath.ToSlash(path), "/") {
		dsn = "file:/" + filepath.ToSlash(path) + "?" + q.Encode()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(4)
	s := &Store{db: db, path: path}
	// Switching a fresh file to WAL is not covered by busy_timeout, so several
	// processes starting together may see SQLITE_BUSY once.
	var merr error
	for attempt := 0; attempt < 50; attempt++ {
		if merr = s.migrate(context.Background()); merr == nil || !strings.Contains(merr.Error(), "SQLITE_BUSY") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if merr != nil {
		db.Close()
		return nil, merr
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Path is the database file path.
func (s *Store) Path() string { return s.path }

// DB exposes the handle to sibling features (alerts, feedback, audit) that
// own their tables' queries.
func (s *Store) DB() *sql.DB { return s.db }

// Tables lists user tables, for schema inspection.
func (s *Store) Tables(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

// tx runs fn in a write transaction.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
