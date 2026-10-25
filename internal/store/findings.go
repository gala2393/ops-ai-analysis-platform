package store

import (
	"context"
	"encoding/json"
)

// HostFinding is one stored finding together with the run that produced it.
type HostFinding struct {
	RunID   int64           `json:"run_id"`
	Finding json.RawMessage `json:"finding"`
}

// FindingsByHost returns the findings of one host across successful runs,
// newest run first. limit <= 0 means a sensible default of 50.
func (s *Store) FindingsByHost(ctx context.Context, hostKey string, limit int) ([]HostFinding, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.run_id, f.finding FROM findings f
		JOIN runs r ON r.id = f.run_id
		WHERE f.host_key = ? AND r.status = ?
		ORDER BY f.run_id DESC, f.id DESC LIMIT ?`, hostKey, StatusSuccess, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostFinding
	for rows.Next() {
		var hf HostFinding
		var js string
		if err := rows.Scan(&hf.RunID, &js); err != nil {
			return nil, err
		}
		hf.Finding = json.RawMessage(js)
		out = append(out, hf)
	}
	return out, rows.Err()
}
