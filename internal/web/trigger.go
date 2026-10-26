package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"yunwei/internal/inspect"
	"yunwei/internal/store"
)

// apiStartRun handles POST /api/runs: it starts an inspection in the
// background and answers immediately with the new run id. A run already in
// progress yields 409 with the Chinese message the UI shows verbatim.
func (s *Server) apiStartRun(w http.ResponseWriter, r *http.Request) {
	if s.starter == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "巡检服务不可用 (inspection unavailable)"})
		return
	}
	id, err := s.starter.RunAsync(store.TriggerManual)
	switch {
	case errors.Is(err, inspect.ErrRunning):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "已有巡检在运行"})
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
	default:
		writeJSON(w, http.StatusCreated, map[string]any{
			"id": id, "status": store.StatusRunning, "message": "巡检已启动",
		})
	}
}

// runProgress serves the htmx-polled progress fragment of one run. While the
// run is in progress the fragment re-polls itself every second; once finished
// it links to the report and polling stops.
func (s *Server) runProgress(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		s.renderFrag(w, http.StatusNotFound, "run_progress", nil)
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			http.Error(w, "读取巡检进度失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.renderFrag(w, http.StatusNotFound, "run_progress", nil)
		return
	}
	s.renderFrag(w, http.StatusOK, "run_progress", run)
}

// reportDownload serves the Markdown or HTML report file of a run as an
// attachment. Files live at <report.out_dir>/<runID>.md|.html.
func (s *Server) reportDownload(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		http.Error(w, "无效的巡检 ID (invalid run id)", http.StatusBadRequest)
		return
	}
	kind := chi.URLParam(r, "kind")
	if kind != "md" && kind != "html" {
		http.Error(w, "未知的报告格式 (unknown report format)", http.StatusNotFound)
		return
	}
	dir := s.cfg.App.Report.OutDir
	if dir == "" {
		dir = "reports"
	}
	name := fmt.Sprintf("%d.%s", id, kind)
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "报告文件不存在 (report file not found)", http.StatusNotFound)
		return
	}
	if kind == "md" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "巡检报告-"+name))
	http.ServeFile(w, r, path)
}
