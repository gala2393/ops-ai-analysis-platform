package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// alertRow is one alert event of the alerts page. The alert pipeline arrives
// in M5; until then the table is empty and the page shows an empty state.
type alertRow struct {
	ID         int64
	Title      string
	Status     string
	StatusText string
	Alerts     int
	Updated    string
}

// alertsData is the model of the alerts page.
type alertsData struct {
	Title  string
	Page   string
	Events []alertRow
}

// alertJSON is the API shape of one stored alert event.
type alertJSON struct {
	ID      int64           `json:"id"`
	Status  string          `json:"status"`
	Updated time.Time       `json:"updated_at"`
	Event   json.RawMessage `json:"event"`
}

// listAlertEvents reads stored alert events, newest first. The alert_events
// table is part of the base schema, so this works before M5 lands.
func (s *Server) listAlertEvents(ctx context.Context) ([]alertJSON, error) {
	rows, err := s.store.DB().QueryContext(ctx,
		`SELECT id, status, updated_at, event FROM alert_events ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []alertJSON{}
	for rows.Next() {
		var e alertJSON
		var updated int64
		var raw string
		if err := rows.Scan(&e.ID, &e.Status, &updated, &raw); err != nil {
			return nil, err
		}
		e.Updated = time.UnixMilli(updated)
		e.Event = json.RawMessage(raw)
		out = append(out, e)
	}
	return out, rows.Err()
}

// alertTitle extracts a display title from the event document.
func alertTitle(e alertJSON) string {
	var doc map[string]any
	if err := json.Unmarshal(e.Event, &doc); err == nil {
		if t, ok := doc["title"].(string); ok && t != "" {
			return t
		}
		if cl, ok := doc["common_labels"].(map[string]any); ok {
			if n, ok := cl["alertname"].(string); ok && n != "" {
				return n
			}
		}
	}
	return "告警事件"
}

func eventStatusText(status string) string {
	switch status {
	case "firing":
		return "触发中"
	case "resolved":
		return "已恢复"
	}
	return status
}

// alertsPage renders `/alerts`: the event list, or the empty state.
func (s *Server) alertsPage(w http.ResponseWriter, r *http.Request) {
	events, err := s.listAlertEvents(r.Context())
	if err != nil {
		http.Error(w, "读取告警事件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := alertsData{Title: "告警事件", Page: "alerts"}
	for _, e := range events {
		n := 0
		var doc struct {
			Alerts []json.RawMessage `json:"alerts"`
		}
		if json.Unmarshal(e.Event, &doc) == nil {
			n = len(doc.Alerts)
		}
		data.Events = append(data.Events, alertRow{
			ID: e.ID, Title: alertTitle(e), Status: e.Status,
			StatusText: eventStatusText(e.Status), Alerts: n, Updated: fmtTime(e.Updated),
		})
	}
	s.render(w, r, "alerts", http.StatusOK, data)
}
