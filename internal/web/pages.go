package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"yunwei/internal/discovery"
	"yunwei/internal/inspect"
	"yunwei/internal/store"
)

// overviewData is the model of the overview page.
type overviewData struct {
	Title     string
	Page      string
	Empty     bool
	Run       *store.Run
	ByBiz     []inspect.Aggregate
	ByEnv     []inspect.Aggregate
	Diff      *discovery.AssetDiff
	DiffEmpty bool
	Top       []inspect.TopList
}

// overviewPage renders `/`: totals, per-biz/env health, latest run summary,
// asset diff and Top N rankings of the newest successful run.
func (s *Server) overviewPage(w http.ResponseWriter, r *http.Request) {
	rd, found, err := s.latestRun(r.Context())
	if err != nil {
		http.Error(w, "读取巡检数据失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := overviewData{Title: "总览", Page: "overview", Empty: !found}
	if found {
		v, err := newRunView(rd)
		if err != nil {
			http.Error(w, "读取巡检数据失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		data.Run = rd.Run
		data.ByBiz = v.Detail.ByBiz
		data.ByEnv = v.Detail.ByEnv
		data.Top = v.Detail.Top
		if v.Detail.Diff != nil {
			data.Diff = v.Detail.Diff
			data.DiffEmpty = v.Detail.Diff.Empty()
		}
	}
	s.render(w, r, "overview", http.StatusOK, data)
}

// hostsData is the model of the server list page.
type hostsData struct {
	Title   string
	Page    string
	Empty   bool
	Filter  hostFilter
	Options map[string][]string
	Rows    rowsData
}

// loadHostRows reads the newest successful run into filterable host rows.
func (s *Server) loadHostRows(r *http.Request) ([]hostRow, map[string][]string, bool, error) {
	rd, found, err := s.latestRun(r.Context())
	if err != nil || !found {
		return nil, nil, found, err
	}
	v, err := newRunView(rd)
	if err != nil {
		return nil, nil, false, err
	}
	rows, options := v.hostRows()
	return rows, options, true, nil
}

// hostsPage renders `/hosts` with the filter form and the initial rows.
func (s *Server) hostsPage(w http.ResponseWriter, r *http.Request) {
	rows, options, found, err := s.loadHostRows(r)
	if err != nil {
		http.Error(w, "读取主机数据失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	f := filterFromQuery(r.URL.Query())
	filtered := filterRows(rows, f)
	s.render(w, r, "hosts", http.StatusOK, hostsData{
		Title: "服务器列表", Page: "hosts", Empty: !found,
		Filter: f, Options: options,
		Rows: rowsData{Rows: filtered, Filtered: f.active()},
	})
}

// hostRowsPartial is the htmx target of the filter form: only the count line
// and the rows table are returned.
func (s *Server) hostRowsPartial(w http.ResponseWriter, r *http.Request) {
	rows, _, found, err := s.loadHostRows(r)
	if err != nil {
		http.Error(w, "读取主机数据失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		s.renderRows(w, rowsData{})
		return
	}
	f := filterFromQuery(r.URL.Query())
	s.renderRows(w, rowsData{Rows: filterRows(rows, f), Filtered: f.active()})
}

// labelRow is one internal label field of a host.
type labelRow struct {
	Field    string
	FieldZh  string
	Value    string
	Fallback string
	Missing  bool
}

// labelFieldZh maps internal label fields to Chinese names.
var labelFieldZh = map[string]string{
	"env": "环境", "role": "角色", "biz": "业务", "dc": "机房", "owner": "负责人",
}

// targetRow is one scrape target of a host.
type targetRow struct {
	Job       string
	Instance  string
	Up        bool
	LastError string
}

// serviceRow is one detected service of a host.
type serviceRow struct {
	ID      string
	Name    string
	Sources string
}

// findingRow is one recent finding of a host.
type findingRow struct {
	RunID     int64
	CheckName string
	Level     string
	LevelText string
	Value     string
	Threshold string
	PromQL    string
}

// hostDetailData is the model of the host detail page.
type hostDetailData struct {
	Title    string
	Page     string
	Host     *discovery.Host
	Health   *inspect.HostHealth
	DownText string
	Labels   []labelRow
	Targets  []targetRow
	Services []serviceRow
	Findings []findingRow
	Chart    chartView
}

// hostDetailPage renders `/hosts/{key}` with base info, labels (missing
// flagged), targets with up/down and cause, services, recent findings across
// runs and an inline SVG trend chart.
func (s *Server) hostDetailPage(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	rd, found, err := s.latestRun(r.Context())
	if err != nil {
		http.Error(w, "读取主机数据失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if found {
		v, err := newRunView(rd)
		if err != nil {
			http.Error(w, "读取主机数据失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if h := v.Hosts[key]; h != nil {
			s.renderHostDetail(w, r, v, h)
			return
		}
	}
	s.render(w, r, "notfound", http.StatusNotFound, map[string]any{
		"Title": "主机不存在", "Page": "hosts", "What": "主机 " + key + " 不存在于最近一次巡检中。",
	})
}

func (s *Server) renderHostDetail(w http.ResponseWriter, r *http.Request, v *runView, h *discovery.Host) {
	hh := v.Health[h.Key]
	if hh == nil {
		hh = &inspect.HostHealth{Key: h.Key, Name: h.Name, Labels: h.Labels, Score: 100, Status: inspect.StatusGreen}
	}
	data := hostDetailData{
		Title: "主机 " + h.Name, Page: "hosts", Host: h, Health: hh,
	}
	if !h.Normal() {
		data.DownText = downText(h)
	}
	for _, field := range discovery.LabelFields {
		lr := labelRow{Field: field, FieldZh: labelFieldZh[field], Value: h.Labels[field]}
		for _, m := range h.MissingLabels {
			if m == field {
				lr.Missing = true
				lr.Fallback = hh.Group[field]
			}
		}
		data.Labels = append(data.Labels, lr)
	}
	for _, t := range h.Targets {
		data.Targets = append(data.Targets, targetRow{Job: t.Job, Instance: t.Instance, Up: t.Up, LastError: t.LastError})
	}
	for _, svc := range h.Services {
		var sources []string
		for _, src := range svc.Sources {
			sources = append(sources, sourceText(src))
		}
		data.Services = append(data.Services, serviceRow{ID: svc.ID, Name: s.serviceName(svc.ID), Sources: joinCN(sources)})
	}

	// Recent findings of this host across successful runs, newest first.
	raw, err := s.store.FindingsByHost(r.Context(), h.Key, 20)
	if err == nil {
		for _, hf := range raw {
			f, derr := decodeFinding(hf.Finding)
			if derr != nil {
				continue
			}
			data.Findings = append(data.Findings, findingRow{
				RunID: hf.RunID, CheckName: f.CheckName, Level: f.Level, LevelText: levelText(f.Level),
				Value: fmtValue(f.Value, f.Unit), Threshold: thresholdText(f), PromQL: f.Evidence.PromQL,
			})
		}
	}

	data.Chart = s.diskTrend(r.Context(), h)
	s.render(w, r, "host_detail", http.StatusOK, data)
}

// thresholdText renders the breached threshold of a finding.
func thresholdText(f *inspect.Finding) string {
	th := f.Evidence.Threshold
	s := th.Compare + " " + fmtValue(th.Crossed, f.Unit)
	if th.Overridden {
		s += "（覆盖后）"
	}
	return s
}

func joinCN(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
