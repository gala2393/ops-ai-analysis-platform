package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestValidRating(t *testing.T) {
	for _, r := range []string{RatingUseful, RatingUseless, RatingWrong} {
		if !ValidRating(r) {
			t.Errorf("ValidRating(%q) = false, want true", r)
		}
	}
	for _, r := range []string{"", "great", "USEFUL", "ok", "5"} {
		if ValidRating(r) {
			t.Errorf("ValidRating(%q) = true, want false", r)
		}
	}
}

func TestValidFeedbackTargetType(t *testing.T) {
	if !ValidFeedbackTargetType(TargetRunGroup) || !ValidFeedbackTargetType(TargetAlertEvent) {
		t.Error("run_group and alert_event must be valid target types")
	}
	for _, tt := range []string{"", "run", "host", "ai_analysis"} {
		if ValidFeedbackTargetType(tt) {
			t.Errorf("ValidFeedbackTargetType(%q) = true, want false", tt)
		}
	}
}

// seedGroupRun stores one successful run with two problem groups.
func seedGroupRun(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	r, err := s.StartRun(ctx, TriggerCLI, "test", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	err = s.CompleteRun(ctx, r.ID, Result{
		FinishedAt: time.Now(),
		Groups: []GroupRow{
			{CheckID: "disk_usage", Level: "crit", Title: "g0", HostCount: 1, Group: json.RawMessage(`{"i":0}`)},
			{CheckID: "cpu_usage", Level: "warn", Title: "g1", HostCount: 2, Group: json.RawMessage(`{"i":1}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func TestFeedbackRoundTripPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "y.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	runID := seedGroupRun(t, s)

	entries := []*Feedback{
		{TargetType: TargetRunGroup, TargetID: fmt.Sprintf("%d:0", runID), Rating: RatingUseful, Note: "定位准确"},
		{TargetType: TargetRunGroup, TargetID: fmt.Sprintf("%d:0", runID), Rating: RatingWrong},
		{TargetType: TargetRunGroup, TargetID: fmt.Sprintf("%d:1", runID), Rating: RatingUseless, Note: "不适用于测试环境"},
	}
	for _, fb := range entries {
		if _, err := s.AddFeedback(ctx, fb); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Feedback must survive a full restart of the process (store reopen).
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.FeedbackFor(ctx, TargetRunGroup, fmt.Sprintf("%d:0", runID))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Rating != RatingUseful || got[0].Note != "定位准确" ||
		got[1].Rating != RatingWrong || got[1].Note != "" {
		t.Errorf("group 0 feedback = %+v", got)
	}
	if got[0].CreatedAt.IsZero() {
		t.Error("created_at not restored")
	}
	other, err := s.FeedbackFor(ctx, TargetRunGroup, fmt.Sprintf("%d:1", runID))
	if err != nil || len(other) != 1 || other[0].Rating != RatingUseless {
		t.Errorf("group 1 feedback = %+v, %v", other, err)
	}
	none, err := s.FeedbackFor(ctx, TargetRunGroup, "999:0")
	if err != nil || len(none) != 0 {
		t.Errorf("unknown target feedback = %+v, %v", none, err)
	}
}

func TestFeedbackTargetExists(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	runID := seedGroupRun(t, s)

	cases := []struct {
		targetType string
		targetID   string
		want       bool
	}{
		{TargetRunGroup, fmt.Sprintf("%d:0", runID), true},
		{TargetRunGroup, fmt.Sprintf("%d:1", runID), true},
		{TargetRunGroup, fmt.Sprintf("%d:2", runID), false}, // only two groups
		{TargetRunGroup, "999:0", false},                    // no such run
		{TargetRunGroup, "abc", false},                      // malformed
		{TargetRunGroup, fmt.Sprintf("%d:x", runID), false}, // malformed index
		{TargetAlertEvent, "1", false},                      // no events yet
		{TargetAlertEvent, "abc", false},                    // malformed
		{"host", "h1", false},                               // unknown type
	}
	for _, tc := range cases {
		got, err := s.FeedbackTargetExists(ctx, tc.targetType, tc.targetID)
		if err != nil {
			t.Fatalf("FeedbackTargetExists(%q, %q): %v", tc.targetType, tc.targetID, err)
		}
		if got != tc.want {
			t.Errorf("FeedbackTargetExists(%q, %q) = %v, want %v", tc.targetType, tc.targetID, got, tc.want)
		}
	}

	// M5 target type: an alert event becomes a valid feedback target.
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO alert_events(key, created_at, updated_at, status, event) VALUES ('k', 1, 1, 'firing', '{}')`); err != nil {
		t.Fatal(err)
	}
	ok, err := s.FeedbackTargetExists(ctx, TargetAlertEvent, "1")
	if err != nil || !ok {
		t.Errorf("alert_event target: ok=%v err=%v, want true", ok, err)
	}
}
