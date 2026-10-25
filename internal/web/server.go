// Package web implements the HTML UI and JSON API of `yunwei serve`: a chi
// router, embedded html/template pages and embedded static assets (htmx and
// CSS), so the single binary serves everything. All UI text is Chinese.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"yunwei/internal/config"
	"yunwei/internal/inspect"
	"yunwei/internal/store"
)

//go:embed templates static
var assets embed.FS

// RangeQuerier is the subset of the Prometheus client the host trend chart
// needs. *prom.Client satisfies it; tests substitute a stub.
type RangeQuerier interface {
	QueryRange(ctx context.Context, query string, r promv1.Range) (model.Value, promv1.Warnings, error)
}

// Server serves the UI and API. It is safe for concurrent use.
type Server struct {
	store *store.Store
	cfg   *config.Bundle
	prom  RangeQuerier // nil disables the trend chart
	pages map[string]*template.Template
	rows  *template.Template
}

// pageTemplates lists the extra template files each page parses on top of the
// layout and the shared snippets.
var pageTemplates = map[string][]string{
	"overview":      {"templates/overview.html"},
	"hosts":         {"templates/hosts.html", "templates/host_rows.html"},
	"host_detail":   {"templates/host_detail.html", "templates/chart.html"},
	"checks":        {"templates/checks.html"},
	"alerts":        {"templates/alerts.html"},
	"reports":       {"templates/reports.html"},
	"report_detail": {"templates/report_detail.html"},
	"notfound":      {"templates/notfound.html"},
}

// New parses the embedded templates and binds the dependencies.
func New(st *store.Store, cfg *config.Bundle, p RangeQuerier) (*Server, error) {
	s := &Server{store: st, cfg: cfg, prom: p, pages: map[string]*template.Template{}}
	layout, err := template.New("layout").Funcs(funcs).ParseFS(assets, "templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("解析页面模板失败 (layout): %w", err)
	}
	for name, extra := range pageTemplates {
		cloned, err := layout.Clone()
		if err != nil {
			return nil, fmt.Errorf("解析页面模板失败 (%s): %w", name, err)
		}
		files := append([]string{"templates/shared.html"}, extra...)
		t, err := cloned.ParseFS(assets, files...)
		if err != nil {
			return nil, fmt.Errorf("解析页面模板失败 (%s): %w", name, err)
		}
		s.pages[name] = t
	}
	rows, err := template.New("host_rows").Funcs(funcs).ParseFS(assets, "templates/host_rows.html")
	if err != nil {
		return nil, fmt.Errorf("解析页面模板失败 (host_rows): %w", err)
	}
	s.rows = rows
	return s, nil
}

// Handler returns the chi router with every UI page, static asset and JSON
// API endpoint.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	static, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServerFS(static)))
	// Browsers always ask for a favicon; answer 204 so the console stays clean.
	r.Get("/favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	r.Get("/", s.overviewPage)
	r.Get("/hosts", s.hostsPage)
	r.Get("/hosts/rows", s.hostRowsPartial)
	r.Get("/hosts/{key}", s.hostDetailPage)
	r.Get("/reports", s.reportsPage)
	r.Get("/reports/{id}", s.reportDetailPage)
	r.Get("/alerts", s.alertsPage)
	r.Get("/checks", s.checksPage)

	r.Get("/api/health", s.apiHealth)
	r.Get("/api/runs", s.apiListRuns)
	r.Get("/api/runs/{id}", s.apiGetRun)
	r.Get("/api/hosts", s.apiListHosts)
	r.Get("/api/alerts", s.apiListAlerts)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "接口不存在 (not found)"})
			return
		}
		s.render(w, r, "notfound", http.StatusNotFound, map[string]any{
			"Title": "页面不存在", "Page": "",
		})
	})
	return r
}

// render executes a page through the layout. Template errors surface as a
// plain 500 (never partial HTML).
func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, status int, data any) {
	var buf strings.Builder
	if err := s.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		http.Error(w, "页面渲染失败 (template error)", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(buf.String()))
}

// renderRows executes the host-rows partial (htmx target).
func (s *Server) renderRows(w http.ResponseWriter, data any) {
	var buf strings.Builder
	if err := s.rows.ExecuteTemplate(&buf, "host_rows", data); err != nil {
		http.Error(w, "页面渲染失败 (template error)", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(buf.String()))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// latestRun loads the newest successful run with its documents. found is
// false when no run has completed yet.
func (s *Server) latestRun(ctx context.Context) (rd *store.RunDetail, found bool, err error) {
	run, err := s.store.LatestSuccess(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	rd, err = s.store.LoadRun(ctx, run.ID)
	if err != nil {
		return nil, false, err
	}
	return rd, true, nil
}

func parseID(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

var funcs = template.FuncMap{
	"fmtTime":     fmtTime,
	"fmtDuration": fmtDuration,
	"fmtVal":      fmtValue,
	"fmtScore":    fmtScore,
	"statusText":  statusText,
	"triggerText": triggerText,
	"levelText":   levelText,
	"sevText":     severityText,
	"runStatus":   runStatusText,
	"aiStatus":    aiStatusText,
	"join":        strings.Join,
	"inc":         func(i int) int { return i + 1 },
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}

func fmtDuration(ms int64) string {
	return fmt.Sprintf("%.1f 秒", float64(ms)/1000)
}

// fmtValue prints at most two decimals without trailing zeros, plus unit.
func fmtValue(v float64, unit string) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s + unit
}

// fmtScore prints an aggregate average score with one decimal.
func fmtScore(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64)
}

func statusText(status string) string {
	switch status {
	case inspect.StatusGreen:
		return "正常"
	case inspect.StatusYellow:
		return "关注"
	case inspect.StatusRed:
		return "异常"
	}
	return status
}

func triggerText(trigger string) string {
	switch trigger {
	case store.TriggerCLI:
		return "命令行"
	case store.TriggerSchedule:
		return "定时"
	case store.TriggerManual:
		return "手动"
	}
	return trigger
}

func levelText(level string) string {
	switch level {
	case inspect.LevelCrit:
		return "严重"
	case inspect.LevelWarn:
		return "关注"
	case inspect.LevelOK:
		return "正常"
	}
	return level
}

func severityText(sev string) string {
	switch sev {
	case "critical":
		return "致命"
	case "high":
		return "高"
	case "medium":
		return "中"
	case "low":
		return "低"
	}
	return sev
}

func runStatusText(status string) string {
	switch status {
	case store.StatusRunning:
		return "运行中"
	case store.StatusSuccess:
		return "成功"
	case store.StatusFailed:
		return "失败"
	}
	return status
}

// aiStatusText renders the run's AI status column; AI arrives in M4, so an
// empty status simply means no AI analysis was part of this run.
func aiStatusText(status string) string {
	switch status {
	case "":
		return "—"
	case "disabled":
		return "未启用"
	case "success":
		return "已完成"
	case "degraded":
		return "已降级"
	}
	return status
}
