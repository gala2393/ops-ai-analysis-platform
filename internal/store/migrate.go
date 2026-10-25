package store

import (
	"context"
	"database/sql"
	"fmt"
)

// migrations are applied in order; each runs once, tracked by schema_version.
// Never edit an applied migration, append a new one.
var migrations = []string{
	`CREATE TABLE runs (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		trigger        TEXT NOT NULL,
		status         TEXT NOT NULL,
		started_at     INTEGER NOT NULL,
		finished_at    INTEGER NOT NULL DEFAULT 0,
		progress_done  INTEGER NOT NULL DEFAULT 0,
		progress_total INTEGER NOT NULL DEFAULT 0,
		hosts_total    INTEGER NOT NULL DEFAULT 0,
		hosts_ok       INTEGER NOT NULL DEFAULT 0,
		hosts_warn     INTEGER NOT NULL DEFAULT 0,
		hosts_crit     INTEGER NOT NULL DEFAULT 0,
		groups_count   INTEGER NOT NULL DEFAULT 0,
		findings_count INTEGER NOT NULL DEFAULT 0,
		query_count    INTEGER NOT NULL DEFAULT 0,
		query_total_ms INTEGER NOT NULL DEFAULT 0,
		query_p95_ms   INTEGER NOT NULL DEFAULT 0,
		ai_status      TEXT NOT NULL DEFAULT '',
		error          TEXT NOT NULL DEFAULT '',
		detail         TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX idx_runs_started ON runs(started_at);

	CREATE TABLE hosts_snapshot (
		run_id   INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		host_key TEXT NOT NULL,
		name     TEXT NOT NULL,
		host     TEXT NOT NULL,
		PRIMARY KEY (run_id, host_key)
	);

	CREATE TABLE findings (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id    INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		check_id  TEXT NOT NULL,
		host_key  TEXT NOT NULL,
		host      TEXT NOT NULL,
		level     TEXT NOT NULL,
		severity  TEXT NOT NULL,
		value     REAL NOT NULL,
		finding   TEXT NOT NULL
	);
	CREATE INDEX idx_findings_run ON findings(run_id);
	CREATE INDEX idx_findings_host ON findings(host_key);

	CREATE TABLE problem_groups (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id    INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		idx       INTEGER NOT NULL,
		check_id  TEXT NOT NULL,
		level     TEXT NOT NULL,
		severity  TEXT NOT NULL,
		title     TEXT NOT NULL,
		host_count INTEGER NOT NULL,
		grp       TEXT NOT NULL
	);
	CREATE INDEX idx_groups_run ON problem_groups(run_id);

	CREATE TABLE ai_analyses (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		target_type TEXT NOT NULL,
		target_id   TEXT NOT NULL,
		run_id      INTEGER NOT NULL DEFAULT 0,
		created_at  INTEGER NOT NULL,
		analysis    TEXT NOT NULL,
		validation  TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX idx_ai_target ON ai_analyses(target_type, target_id);

	CREATE TABLE llm_audit (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at        INTEGER NOT NULL,
		purpose           TEXT NOT NULL,
		input_summary     TEXT NOT NULL DEFAULT '',
		output            TEXT NOT NULL DEFAULT '',
		prompt_tokens     INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		duration_ms       INTEGER NOT NULL DEFAULT 0,
		status            TEXT NOT NULL DEFAULT '',
		error             TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX idx_audit_time ON llm_audit(created_at);

	CREATE TABLE alerts (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		received_at INTEGER NOT NULL,
		fingerprint TEXT NOT NULL DEFAULT '',
		event_id    INTEGER NOT NULL DEFAULT 0,
		status      TEXT NOT NULL DEFAULT '',
		raw         TEXT NOT NULL
	);
	CREATE INDEX idx_alerts_event ON alerts(event_id);

	CREATE TABLE alert_events (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		key        TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		status     TEXT NOT NULL,
		event      TEXT NOT NULL
	);
	CREATE INDEX idx_events_created ON alert_events(created_at);

	CREATE TABLE feedback (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		target_type TEXT NOT NULL,
		target_id   TEXT NOT NULL,
		rating      TEXT NOT NULL,
		note        TEXT NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL
	);
	CREATE INDEX idx_feedback_target ON feedback(target_type, target_id);

	CREATE TABLE notifications (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at INTEGER NOT NULL,
		channel    TEXT NOT NULL,
		target_type TEXT NOT NULL DEFAULT '',
		target_id  TEXT NOT NULL DEFAULT '',
		status     TEXT NOT NULL,
		error      TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE run_lock (
		id          INTEGER PRIMARY KEY CHECK (id = 1),
		run_id      INTEGER NOT NULL,
		owner       TEXT NOT NULL,
		acquired_at INTEGER NOT NULL,
		expires_at  INTEGER NOT NULL
	);`,
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: schema_version: %w", err)
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var cur int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&cur); err != nil {
			return err
		}
		for i := cur; i < len(migrations); i++ {
			if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
				return fmt.Errorf("store: migration %d: %w", i+1, err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES (?)`, i+1); err != nil {
				return err
			}
		}
		return nil
	})
}
