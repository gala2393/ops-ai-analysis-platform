package web

import (
	"errors"
	"fmt"
	"net/http"
	"sort"

	"yunwei/internal/discovery"
	"yunwei/internal/inspect"
	"yunwei/internal/store"
)

// reportsData is the model of the report list page.
type reportsData struct {
	Title string
	Page  string
	Runs  []*store.Run
}

// reportsPage renders `/reports`: every stored run, newest first.
func (s *Server) reportsPage(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.ListRuns(r.Context(), 100)
	if err != nil {
		http.Error(w, "读取巡检报告失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "reports", http.StatusOK, reportsData{Title: "巡检报告", Page: "reports", Runs: runs})
}

// hostFindingRow is one finding of a problem group with its host link.
type hostFindingRow struct {
	HostKey   string
	Host      string
	Level     string
	LevelText string
	Value     string
	Threshold string
	PromQL    string
}

// groupView is one problem group with its per-host evidence rows, the AI
// analysis placeholder (M4 fills AIAnalysis) and the feedback widget.
type groupView struct {
	Title      string
	Level      string
	LevelText  string
	Severity   string
	Count      int
	Common     []string
	Advice     string
	Rows       []hostFindingRow
	AIAnalysis string
	Feedback   feedbackView
}

// reportDetailData is the model of the report detail page.
type reportDetailData struct {
	Title        string
	Page         string
	Run          *store.Run
	ByBiz        []inspect.Aggregate
	ByEnv        []inspect.Aggregate
	Groups       []groupView
	Top          []inspect.TopList
	Diff         *discovery.AssetDiff
	DiffEmpty    bool
	Missing      []missingRow
	Down         []downRow
	Unclassified []discovery.UnclassifiedJob
	Items        []inspect.ItemRun
	Warnings     []string
}

type missingRow struct {
	Host    string
	Key     string
	Missing string
}

type downRow struct {
	Host    string
	Key     string
	Cause   string
	Targets []targetRow
}

// reportDetailPage renders `/reports/{id}` from the stored run documents:
// overview, problem groups with per-host evidence (PromQL + value), Top N,
// missing labels, asset changes, unclassified jobs and down causes.
func (s *Server) reportDetailPage(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		s.render(w, r, "notfound", http.StatusNotFound, map[string]any{"Title": "报告不存在", "Page": "reports"})
		return
	}
	rd, err := s.store.LoadRun(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.render(w, r, "notfound", http.StatusNotFound, map[string]any{"Title": "报告不存在", "Page": "reports"})
		return
	}
	if err != nil {
		http.Error(w, "读取报告失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	v, err := newRunView(rd)
	if err != nil {
		http.Error(w, "读取报告失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	data := reportDetailData{
		Title: "巡检报告", Page: "reports", Run: rd.Run,
		ByBiz: v.Detail.ByBiz, ByEnv: v.Detail.ByEnv, Top: v.Detail.Top,
		Unclassified: v.Detail.Unclassified, Items: v.Detail.Items, Warnings: v.Detail.Warnings,
	}
	if v.Detail.Diff != nil {
		data.Diff = v.Detail.Diff
		data.DiffEmpty = v.Detail.Diff.Empty()
	}
	for i, g := range v.Groups {
		gv := groupView{
			Title: g.Title, Level: g.Level, LevelText: levelText(g.Level),
			Severity: severityText(g.Severity), Count: g.Count, Advice: g.AdviceRef,
		}
		// The feedback target is the group's position within the run.
		targetID := fmt.Sprintf("%d:%d", rd.Run.ID, i)
		stored, ferr := s.store.FeedbackFor(r.Context(), store.TargetRunGroup, targetID)
		if ferr != nil {
			http.Error(w, "读取反馈失败: "+ferr.Error(), http.StatusInternalServerError)
			return
		}
		gv.Feedback = newFeedbackView(store.TargetRunGroup, targetID, stored)
		for _, c := range g.Common {
			gv.Common = append(gv.Common, c.Field+"="+c.Value)
		}
		for _, f := range g.Findings {
			gv.Rows = append(gv.Rows, hostFindingRow{
				HostKey: f.HostKey, Host: f.Host, Level: f.Level, LevelText: levelText(f.Level),
				Value: fmtValue(f.Value, f.Unit), Threshold: thresholdText(f), PromQL: f.Evidence.PromQL,
			})
		}
		data.Groups = append(data.Groups, gv)
	}
	for _, h := range v.Hosts {
		if len(h.MissingLabels) > 0 {
			data.Missing = append(data.Missing, missingRow{Host: h.Name, Key: h.Key, Missing: joinCN(h.MissingLabels)})
		}
		if !h.Normal() {
			dr := downRow{Host: h.Name, Key: h.Key, Cause: downText(h)}
			for _, t := range h.Targets {
				if !t.Up {
					dr.Targets = append(dr.Targets, targetRow{Job: t.Job, Instance: t.Instance, LastError: t.LastError})
				}
			}
			data.Down = append(data.Down, dr)
		}
	}
	sort.Slice(data.Missing, func(i, j int) bool { return data.Missing[i].Host < data.Missing[j].Host })
	sort.Slice(data.Down, func(i, j int) bool { return data.Down[i].Host < data.Down[j].Host })
	s.render(w, r, "report_detail", http.StatusOK, data)
}
