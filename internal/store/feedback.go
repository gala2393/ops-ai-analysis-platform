package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"
)

// Feedback ratings.
const (
	RatingUseful  = "useful"
	RatingUseless = "useless"
	RatingWrong   = "wrong"
)

// Feedback target types. TargetRunGroup identifies one problem group of a
// run as "<runID>:<groupIdx>"; TargetAlertEvent is the alert event id (M5).
const (
	TargetRunGroup   = "run_group"
	TargetAlertEvent = "alert_event"
)

// ValidRating reports whether rating is one of the allowed values.
func ValidRating(rating string) bool {
	switch rating {
	case RatingUseful, RatingUseless, RatingWrong:
		return true
	}
	return false
}

// ValidFeedbackTargetType reports whether targetType is supported.
func ValidFeedbackTargetType(targetType string) bool {
	return targetType == TargetRunGroup || targetType == TargetAlertEvent
}

// Feedback is one piece of user feedback on an analysis target.
type Feedback struct {
	ID         int64     `json:"id"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	Rating     string    `json:"rating"`
	Note       string    `json:"note"`
	CreatedAt  time.Time `json:"created_at"`
}

// AddFeedback stores one feedback entry and returns its id. fb.CreatedAt is
// set to the stored timestamp.
func (s *Store) AddFeedback(ctx context.Context, fb *Feedback) (int64, error) {
	fb.CreatedAt = time.Now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO feedback(target_type, target_id, rating, note, created_at) VALUES (?,?,?,?,?)`,
		fb.TargetType, fb.TargetID, fb.Rating, fb.Note, ms(fb.CreatedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FeedbackFor returns every feedback entry of one target, oldest first.
func (s *Store) FeedbackFor(ctx context.Context, targetType, targetID string) ([]*Feedback, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, target_type, target_id, rating, note, created_at FROM feedback
		 WHERE target_type = ? AND target_id = ? ORDER BY id`, targetType, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Feedback{}
	for rows.Next() {
		var f Feedback
		var created int64
		if err := rows.Scan(&f.ID, &f.TargetType, &f.TargetID, &f.Rating, &f.Note, &created); err != nil {
			return nil, err
		}
		f.CreatedAt = fromMS(created)
		out = append(out, &f)
	}
	return out, rows.Err()
}

// FeedbackTargetExists reports whether targetID refers to a stored object of
// the given type. Unknown types simply report false.
func (s *Store) FeedbackTargetExists(ctx context.Context, targetType, targetID string) (bool, error) {
	var q string
	var args []any
	switch targetType {
	case TargetRunGroup:
		runID, idx, ok := splitRunGroupTarget(targetID)
		if !ok {
			return false, nil
		}
		q = `SELECT COUNT(1) FROM problem_groups WHERE run_id = ? AND idx = ?`
		args = []any{runID, idx}
	case TargetAlertEvent:
		id, err := strconv.ParseInt(targetID, 10, 64)
		if err != nil {
			return false, nil
		}
		q = `SELECT COUNT(1) FROM alert_events WHERE id = ?`
		args = []any{id}
	default:
		return false, nil
	}
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return n > 0, nil
}

// splitRunGroupTarget parses "<runID>:<groupIdx>".
func splitRunGroupTarget(targetID string) (runID, idx int64, ok bool) {
	parts := strings.SplitN(targetID, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	runID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	idx, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return runID, idx, true
}
