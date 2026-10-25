package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrRunning is returned when another inspection holds a live lock.
var ErrRunning = errors.New("已有巡检在运行 (an inspection is already running)")

// StartRun creates a run record and takes the single run lock for it. An
// existing lock past its TTL is stale: it is taken over and any run still
// marked running is marked failed. A live lock yields ErrRunning and no run.
func (s *Store) StartRun(ctx context.Context, trigger, owner string, ttl time.Duration, now time.Time) (*Run, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var heldBy string
		var expires int64
		err := tx.QueryRowContext(ctx, `SELECT owner, expires_at FROM run_lock WHERE id = 1`).Scan(&heldBy, &expires)
		switch {
		case err == nil && expires > ms(now):
			return ErrRunning
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM run_lock`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE runs SET status = ?, error = ?, finished_at = ? WHERE status = ?`,
			StatusFailed, "巡检进程中断,锁已过期 (interrupted; lock expired)", ms(now), StatusRunning); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO runs(trigger, status, started_at) VALUES (?,?,?)`,
			trigger, StatusRunning, ms(now))
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO run_lock(id, run_id, owner, acquired_at, expires_at) VALUES (1,?,?,?,?)`,
			id, owner, ms(now), ms(now.Add(ttl)))
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetRun(ctx, id)
}

// RenewLock extends the lock of run id. It fails when the lock was lost.
func (s *Store) RenewLock(ctx context.Context, id int64, ttl time.Duration, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE run_lock SET expires_at = ? WHERE id = 1 AND run_id = ?`,
		ms(now.Add(ttl)), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("run %d no longer holds the run lock", id)
	}
	return nil
}

// ReleaseLock drops the lock if run id still holds it.
func (s *Store) ReleaseLock(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM run_lock WHERE id = 1 AND run_id = ?`, id)
	return err
}
