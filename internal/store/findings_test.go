package store

import (
	"context"
	"testing"
	"time"
)

func TestFindingsByHostAcrossRuns(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	mkRun := func(trigger string, findings []FindingRow) int64 {
		run, err := s.StartRun(ctx, trigger, "test", time.Minute, time.Now())
		if err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		res := Result{FinishedAt: time.Now()}
		res.Findings = findings
		if err := s.CompleteRun(ctx, run.ID, res); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		if err := s.ReleaseLock(ctx, run.ID); err != nil {
			t.Fatalf("ReleaseLock: %v", err)
		}
		return run.ID
	}

	id1 := mkRun(TriggerCLI, []FindingRow{
		{CheckID: "disk_usage", HostKey: "10.0.0.1", Host: "app-1", Level: "crit", Severity: "high", Value: 93, Finding: []byte(`{"check_id":"disk_usage"}`)},
	})
	id2 := mkRun(TriggerCLI, []FindingRow{
		{CheckID: "mem_usage", HostKey: "10.0.0.1", Host: "app-1", Level: "warn", Severity: "medium", Value: 85, Finding: []byte(`{"check_id":"mem_usage"}`)},
		{CheckID: "disk_usage", HostKey: "10.0.0.2", Host: "app-2", Level: "crit", Severity: "high", Value: 95, Finding: []byte(`{"check_id":"disk_usage"}`)},
	})

	got, err := s.FindingsByHost(ctx, "10.0.0.1", 10)
	if err != nil {
		t.Fatalf("FindingsByHost: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 findings, got %d", len(got))
	}
	if got[0].RunID != id2 || got[1].RunID != id1 {
		t.Fatalf("want newest run first [%d %d], got [%d %d]", id2, id1, got[0].RunID, got[1].RunID)
	}

	// A failed run must not contribute findings.
	run, err := s.StartRun(ctx, TriggerCLI, "test", time.Minute, time.Now())
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := s.FailRun(ctx, run.ID, "boom", time.Now()); err != nil {
		t.Fatalf("FailRun: %v", err)
	}
	if err := s.ReleaseLock(ctx, run.ID); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}
	got, err = s.FindingsByHost(ctx, "10.0.0.2", 10)
	if err != nil {
		t.Fatalf("FindingsByHost: %v", err)
	}
	if len(got) != 1 || got[0].RunID != id2 {
		t.Fatalf("want the single success-run finding of run %d, got %+v", id2, got)
	}

	got, err = s.FindingsByHost(ctx, "10.9.9.9", 10)
	if err != nil {
		t.Fatalf("FindingsByHost: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want no findings for unknown host, got %d", len(got))
	}
}
