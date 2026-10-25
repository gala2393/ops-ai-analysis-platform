package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Run triggers and statuses.
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
	TriggerCLI      = "cli"

	StatusRunning = "running"
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

// ErrNotFound is returned when a run does not exist.
var ErrNotFound = errors.New("store: not found")

// HostCounts splits hosts by health status.
type HostCounts struct {
	Total int `json:"total"`
	OK    int `json:"ok"`
	Warn  int `json:"warn"`
	Crit  int `json:"crit"`
}

// QueryStats are the Prometheus query numbers of one run.
type QueryStats struct {
	Count   int   `json:"count"`
	TotalMS int64 `json:"total_ms"`
	P95MS   int64 `json:"p95_ms"`
}

// Progress counts finished inspection items.
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// Run is one inspection run record.
type Run struct {
	ID         int64      `json:"id"`
	Trigger    string     `json:"trigger"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Progress   Progress   `json:"progress"`
	Hosts      HostCounts `json:"hosts"`
	Groups     int        `json:"groups"`
	Findings   int        `json:"findings"`
	Query      QueryStats `json:"query"`
	AIStatus   string     `json:"ai_status,omitempty"`
	Error      string     `json:"error,omitempty"`
	// DurationMS is finished-started, or elapsed time while running.
	DurationMS int64 `json:"duration_ms"`
}

const runCols = `id, trigger, status, started_at, finished_at, progress_done, progress_total,
	hosts_total, hosts_ok, hosts_warn, hosts_crit, groups_count, findings_count,
	query_count, query_total_ms, query_p95_ms, ai_status, error`

type scanner interface{ Scan(dest ...any) error }

func scanRun(sc scanner) (*Run, error) {
	var r Run
	var started, finished int64
	err := sc.Scan(&r.ID, &r.Trigger, &r.Status, &started, &finished, &r.Progress.Done, &r.Progress.Total,
		&r.Hosts.Total, &r.Hosts.OK, &r.Hosts.Warn, &r.Hosts.Crit, &r.Groups, &r.Findings,
		&r.Query.Count, &r.Query.TotalMS, &r.Query.P95MS, &r.AIStatus, &r.Error)
	if err != nil {
		return nil, err
	}
	r.StartedAt = fromMS(started)
	if finished != 0 {
		t := fromMS(finished)
		r.FinishedAt = &t
		r.DurationMS = finished - started
	} else if r.Status == StatusRunning {
		r.DurationMS = time.Now().UnixMilli() - started
	}
	return &r, nil
}

// GetRun loads one run record.
func (s *Store) GetRun(ctx context.Context, id int64) (*Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ListRuns returns the newest runs first. limit <= 0 means all.
func (s *Store) ListRuns(ctx context.Context, limit int) ([]*Run, error) {
	q := `SELECT ` + runCols + ` FROM runs ORDER BY id DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestSuccess returns the newest successful run, or ErrNotFound.
func (s *Store) LatestSuccess(ctx context.Context) (*Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE status = ? ORDER BY id DESC LIMIT 1`, StatusSuccess))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// SetProgress records finished items of a running run. Progress never moves
// backwards, so callbacks that arrive out of order cannot regress it.
func (s *Store) SetProgress(ctx context.Context, id int64, done, total int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET progress_done = MAX(progress_done, ?), progress_total = ? WHERE id = ? AND status = ?`,
		done, total, id, StatusRunning)
	return err
}

// FailRun marks a running run failed. Runs already finished are left alone.
func (s *Store) FailRun(ctx context.Context, id int64, reason string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = ?, error = ?, finished_at = ? WHERE id = ? AND status = ?`,
		StatusFailed, reason, ms(at), id, StatusRunning)
	return err
}

// FindingRow is one stored finding. Finding holds the full JSON document.
type FindingRow struct {
	CheckID  string
	HostKey  string
	Host     string
	Level    string
	Severity string
	Value    float64
	Finding  json.RawMessage
}

// GroupRow is one stored problem group.
type GroupRow struct {
	CheckID   string
	Level     string
	Severity  string
	Title     string
	HostCount int
	Group     json.RawMessage
}

// HostRow is one host of the run's inventory snapshot.
type HostRow struct {
	Key  string
	Name string
	Host json.RawMessage
}

// Result is everything judged in a successful run. Detail carries the rest
// of the run document (item runs, rankings, aggregates, asset diff) as JSON.
type Result struct {
	Hosts      HostCounts
	Query      QueryStats
	AIStatus   string
	Findings   []FindingRow
	Groups     []GroupRow
	Snapshot   []HostRow
	Detail     json.RawMessage
	FinishedAt time.Time
}

// CompleteRun stores the results and marks the run successful in one
// transaction. It refuses a run that is no longer running (e.g. taken over
// as stale), so a zombie process cannot overwrite a newer outcome.
func (s *Store) CompleteRun(ctx context.Context, id int64, res Result) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		upd, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, finished_at = ?, hosts_total = ?, hosts_ok = ?,
			hosts_warn = ?, hosts_crit = ?, groups_count = ?, findings_count = ?, query_count = ?,
			query_total_ms = ?, query_p95_ms = ?, ai_status = ?, detail = ?, error = '', progress_done = progress_total
			WHERE id = ? AND status = ?`,
			StatusSuccess, ms(res.FinishedAt), res.Hosts.Total, res.Hosts.OK, res.Hosts.Warn, res.Hosts.Crit,
			len(res.Groups), len(res.Findings), res.Query.Count, res.Query.TotalMS, res.Query.P95MS,
			res.AIStatus, string(res.Detail), id, StatusRunning)
		if err != nil {
			return err
		}
		if n, _ := upd.RowsAffected(); n == 0 {
			return errors.New("run is no longer running (lock lost)")
		}
		for _, f := range res.Findings {
			if _, err := tx.ExecContext(ctx, `INSERT INTO findings(run_id, check_id, host_key, host, level, severity, value, finding)
				VALUES (?,?,?,?,?,?,?,?)`, id, f.CheckID, f.HostKey, f.Host, f.Level, f.Severity, f.Value, string(f.Finding)); err != nil {
				return err
			}
		}
		for i, g := range res.Groups {
			if _, err := tx.ExecContext(ctx, `INSERT INTO problem_groups(run_id, idx, check_id, level, severity, title, host_count, grp)
				VALUES (?,?,?,?,?,?,?,?)`, id, i, g.CheckID, g.Level, g.Severity, g.Title, g.HostCount, string(g.Group)); err != nil {
				return err
			}
		}
		for _, h := range res.Snapshot {
			if _, err := tx.ExecContext(ctx, `INSERT INTO hosts_snapshot(run_id, host_key, name, host) VALUES (?,?,?,?)`,
				id, h.Key, h.Name, string(h.Host)); err != nil {
				return err
			}
		}
		return nil
	})
}

// RunDetail is a run with its stored documents.
type RunDetail struct {
	Run      *Run
	Detail   json.RawMessage
	Findings []json.RawMessage
	Groups   []json.RawMessage
	Hosts    []json.RawMessage
}

// LoadRun returns the run with its findings, groups, host snapshot and detail.
func (s *Store) LoadRun(ctx context.Context, id int64) (*RunDetail, error) {
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	d := &RunDetail{Run: run}
	var detail string
	if err := s.db.QueryRowContext(ctx, `SELECT detail FROM runs WHERE id = ?`, id).Scan(&detail); err != nil {
		return nil, err
	}
	if detail != "" {
		d.Detail = json.RawMessage(detail)
	}
	collect := func(q string) ([]json.RawMessage, error) {
		rows, err := s.db.QueryContext(ctx, q, id)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []json.RawMessage
		for rows.Next() {
			var js string
			if err := rows.Scan(&js); err != nil {
				return nil, err
			}
			out = append(out, json.RawMessage(js))
		}
		return out, rows.Err()
	}
	if d.Findings, err = collect(`SELECT finding FROM findings WHERE run_id = ? ORDER BY id`); err != nil {
		return nil, err
	}
	if d.Groups, err = collect(`SELECT grp FROM problem_groups WHERE run_id = ? ORDER BY idx`); err != nil {
		return nil, err
	}
	if d.Hosts, err = collect(`SELECT host FROM hosts_snapshot WHERE run_id = ? ORDER BY host_key`); err != nil {
		return nil, err
	}
	return d, nil
}

// HostHistory maps a host key to the number of successful runs in which it
// had at least one finding.
func (s *Store) HostHistory(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.host_key, COUNT(DISTINCT f.run_id) FROM findings f
		JOIN runs r ON r.id = f.run_id WHERE r.status = ? GROUP BY f.host_key`, StatusSuccess)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// PurgeBefore deletes runs started before cutoff together with their
// findings, groups and snapshots, plus audit, alert and notification rows
// older than cutoff. It returns the number of runs removed.
func (s *Store) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	var removed int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		c := ms(cutoff)
		res, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE started_at < ? AND status <> ?`, c, StatusRunning)
		if err != nil {
			return err
		}
		removed, _ = res.RowsAffected()
		for _, q := range []string{
			`DELETE FROM ai_analyses WHERE created_at < ?`,
			`DELETE FROM llm_audit WHERE created_at < ?`,
			`DELETE FROM alerts WHERE received_at < ?`,
			`DELETE FROM alert_events WHERE updated_at < ?`,
			`DELETE FROM feedback WHERE created_at < ?`,
			`DELETE FROM notifications WHERE created_at < ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, c); err != nil {
				return err
			}
		}
		return nil
	})
	return removed, err
}
