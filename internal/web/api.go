package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"yunwei/internal/store"
)

// apiHealth reports liveness. It never carries configuration or secrets.
func (s *Server) apiHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// apiListRuns returns the stored runs, newest first.
func (s *Server) apiListRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.ListRuns(r.Context(), 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if runs == nil {
		runs = []*store.Run{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// apiGetRun returns one run with its detail, findings, groups and host
// snapshot documents.
func (s *Server) apiGetRun(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的巡检 ID (invalid run id)"})
		return
	}
	d, err := s.store.LoadRun(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "巡检不存在 (run not found)"})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if d.Detail == nil {
		d.Detail = json.RawMessage("{}")
	}
	if d.Findings == nil {
		d.Findings = []json.RawMessage{}
	}
	if d.Groups == nil {
		d.Groups = []json.RawMessage{}
	}
	if d.Hosts == nil {
		d.Hosts = []json.RawMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run": d.Run, "detail": d.Detail, "findings": d.Findings, "groups": d.Groups, "hosts": d.Hosts,
	})
}

// apiListHosts returns the hosts of the latest successful run. It accepts the
// same env/role/biz/dc/status/q filters as the server list page.
func (s *Server) apiListHosts(w http.ResponseWriter, r *http.Request) {
	rows, _, found, err := s.loadHostRows(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := []hostRow{}
	if found {
		f := filterFromQuery(r.URL.Query())
		out = filterRows(rows, f)
		if out == nil {
			out = []hostRow{}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// apiListAlerts returns the stored alert events (empty until M5).
func (s *Server) apiListAlerts(w http.ResponseWriter, r *http.Request) {
	events, err := s.listAlertEvents(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if events == nil {
		events = []alertJSON{}
	}
	writeJSON(w, http.StatusOK, events)
}
