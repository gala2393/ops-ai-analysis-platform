package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesSchemaWithoutSeriesTables(t *testing.T) {
	s := openTemp(t)
	tables, err := s.Tables(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"runs": false, "hosts_snapshot": false, "findings": false, "problem_groups": false,
		"ai_analyses": false, "llm_audit": false, "alerts": false, "alert_events": false, "feedback": false,
		"run_lock": false, "notifications": false}
	for _, n := range tables {
		if _, ok := want[n]; ok {
			want[n] = true
		}
		if n == "samples" || n == "series" || n == "metrics" {
			t.Errorf("unexpected raw series table %q", n)
		}
	}
	for n, ok := range want {
		if !ok {
			t.Errorf("missing table %s", n)
		}
	}
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v", mode, err)
	}
}

func TestLockRejectsSecondRunAndRecoversStale(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	now := time.Now()

	r1, err := s.StartRun(ctx, TriggerCLI, "a", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(ctx, TriggerManual, "b", time.Minute, now.Add(10*time.Second)); !errors.Is(err, ErrRunning) {
		t.Fatalf("second run err = %v, want ErrRunning", err)
	}
	list, _ := s.ListRuns(ctx, 0)
	if len(list) != 1 {
		t.Fatalf("rejected run created a record: %d runs", len(list))
	}

	// Renewal keeps the lock alive past the original TTL.
	if err := s.RenewLock(ctx, r1.ID, time.Minute, now.Add(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(ctx, TriggerManual, "b", time.Minute, now.Add(90*time.Second)); !errors.Is(err, ErrRunning) {
		t.Fatalf("renewed lock was taken over: %v", err)
	}

	r2, err := s.StartRun(ctx, TriggerManual, "b", time.Minute, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("stale lock not recovered: %v", err)
	}
	old, _ := s.GetRun(ctx, r1.ID)
	if old.Status != StatusFailed || old.Error == "" {
		t.Errorf("interrupted run = %s %q, want failed with reason", old.Status, old.Error)
	}
	if err := s.RenewLock(ctx, r1.ID, time.Minute, now); err == nil {
		t.Error("zombie run could renew a lock it lost")
	}
	if err := s.CompleteRun(ctx, r1.ID, Result{FinishedAt: now}); err == nil {
		t.Error("zombie run completed after takeover")
	}
	if err := s.ReleaseLock(ctx, r2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(ctx, TriggerCLI, "c", time.Minute, now.Add(3*time.Minute)); err != nil {
		t.Errorf("run after release: %v", err)
	}
}

func TestConcurrentStartExactlyOneWins(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "y.db")
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(path) // separate handles stand in for separate processes
			if err != nil {
				results <- err
				return
			}
			defer s.Close()
			_, err = s.StartRun(ctx, TriggerCLI, "p", time.Minute, time.Now())
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrRunning):
			rejected++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || rejected != 5 {
		t.Errorf("ok=%d rejected=%d, want 1 and 5", ok, rejected)
	}
}

func TestCompleteAndLoadRun(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	now := time.Now()
	r, _ := s.StartRun(ctx, TriggerSchedule, "a", time.Minute, now)
	if err := s.SetProgress(ctx, r.ID, 3, 14); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRun(ctx, r.ID)
	if got.Progress.Done != 3 || got.Progress.Total != 14 || got.Status != StatusRunning {
		t.Fatalf("progress: %+v", got)
	}
	res := Result{
		Hosts: HostCounts{Total: 4, OK: 2, Warn: 1, Crit: 1}, Query: QueryStats{Count: 14, TotalMS: 500, P95MS: 60},
		FinishedAt: now.Add(2 * time.Second), Detail: json.RawMessage(`{"x":1}`),
		Findings: []FindingRow{{CheckID: "disk", HostKey: "h1", Host: "h1", Level: "crit", Severity: "high", Value: 91, Finding: json.RawMessage(`{"a":1}`)}},
		Groups:   []GroupRow{{CheckID: "disk", Level: "crit", Title: "t", HostCount: 1, Group: json.RawMessage(`{"g":1}`)}},
		Snapshot: []HostRow{{Key: "h1", Name: "h1", Host: json.RawMessage(`{"key":"h1"}`)}},
	}
	if err := s.CompleteRun(ctx, r.ID, res); err != nil {
		t.Fatal(err)
	}
	d, err := s.LoadRun(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Run.Status != StatusSuccess || d.Run.Hosts.Crit != 1 || d.Run.Query.P95MS != 60 || d.Run.Findings != 1 ||
		d.Run.Groups != 1 || d.Run.DurationMS != 2000 || len(d.Findings) != 1 || len(d.Hosts) != 1 || string(d.Detail) != `{"x":1}` {
		t.Errorf("loaded: %+v findings=%d", d.Run, len(d.Findings))
	}
	h, _ := s.HostHistory(ctx)
	if h["h1"] != 1 {
		t.Errorf("history = %v", h)
	}
	if _, err := s.GetRun(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing run err = %v", err)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "y.db")
	s, _ := Open(path)
	r, _ := s.StartRun(ctx, TriggerCLI, "a", time.Minute, time.Now())
	_ = s.CompleteRun(ctx, r.ID, Result{FinishedAt: time.Now()})
	s.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetRun(ctx, r.ID)
	if err != nil || got.Status != StatusSuccess {
		t.Fatalf("after reopen: %+v %v", got, err)
	}
}

func TestPurgeBeforeRemovesOldRunsAndChildren(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	now := time.Now()
	mk := func(at time.Time) int64 {
		r, err := s.StartRun(ctx, TriggerCLI, "a", time.Minute, at)
		if err != nil {
			t.Fatal(err)
		}
		err = s.CompleteRun(ctx, r.ID, Result{FinishedAt: at,
			Findings: []FindingRow{{CheckID: "c", HostKey: "h", Host: "h", Level: "warn", Finding: json.RawMessage(`{}`)}},
			Groups:   []GroupRow{{CheckID: "c", Group: json.RawMessage(`{}`)}},
			Snapshot: []HostRow{{Key: "h", Host: json.RawMessage(`{}`)}}})
		if err != nil {
			t.Fatal(err)
		}
		_ = s.ReleaseLock(ctx, r.ID)
		return r.ID
	}
	oldID := mk(now.Add(-100 * 24 * time.Hour))
	newID := mk(now.Add(-time.Hour))

	n, err := s.PurgeBefore(ctx, now.Add(-90*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}
	if _, err := s.GetRun(ctx, oldID); !errors.Is(err, ErrNotFound) {
		t.Error("old run kept")
	}
	if _, err := s.GetRun(ctx, newID); err != nil {
		t.Error("recent run removed")
	}
	for _, tbl := range []string{"findings", "problem_groups", "hosts_snapshot"} {
		var c, orphan int
		s.db.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&c)
		s.db.QueryRow(`SELECT COUNT(*) FROM `+tbl+` WHERE run_id = ?`, oldID).Scan(&orphan)
		if c != 1 || orphan != 0 {
			t.Errorf("%s rows=%d orphans=%d, want 1 and 0", tbl, c, orphan)
		}
	}
}
