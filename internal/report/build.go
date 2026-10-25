package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"yunwei/internal/config"
	"yunwei/internal/discovery"
	"yunwei/internal/inspect"
	"yunwei/internal/store"
)

// Input is everything a report is built from. It can come from a fresh run
// (FromOutcome) or from the stored run documents (FromStored).
type Input struct {
	Run      *store.Run
	Findings []*inspect.Finding
	Groups   []*inspect.ProblemGroup
	Detail   inspect.Detail
	Hosts    []*discovery.Host
	// Items are the configured check items, used for source and unit.
	Items []config.Item
	Now   time.Time
}

// FromOutcome adapts a completed run.
func FromOutcome(out *inspect.Outcome, items []config.Item) Input {
	return Input{Run: out.Run, Findings: out.Result.Findings, Groups: out.Result.Groups,
		Detail: out.Detail, Hosts: out.Discovery.Hosts, Items: items, Now: time.Now()}
}

// FromStored adapts the documents saved by the inspection runner.
func FromStored(rd *store.RunDetail, items []config.Item) (Input, error) {
	in := Input{Run: rd.Run, Items: items, Now: time.Now()}
	if len(rd.Detail) > 0 {
		if err := json.Unmarshal(rd.Detail, &in.Detail); err != nil {
			return in, fmt.Errorf("读取巡检详情失败: %w", err)
		}
	}
	for _, raw := range rd.Findings {
		var f inspect.Finding
		if err := json.Unmarshal(raw, &f); err != nil {
			return in, fmt.Errorf("读取发现项失败: %w", err)
		}
		in.Findings = append(in.Findings, &f)
	}
	for _, raw := range rd.Groups {
		var g inspect.ProblemGroup
		if err := json.Unmarshal(raw, &g); err != nil {
			return in, fmt.Errorf("读取问题分组失败: %w", err)
		}
		in.Groups = append(in.Groups, &g)
	}
	for _, raw := range rd.Hosts {
		var h discovery.Host
		if err := json.Unmarshal(raw, &h); err != nil {
			return in, fmt.Errorf("读取主机快照失败: %w", err)
		}
		in.Hosts = append(in.Hosts, &h)
	}
	return in, nil
}

var (
	levelText  = map[string]string{inspect.LevelCrit: "严重", inspect.LevelWarn: "关注", inspect.LevelOK: "正常"}
	statusText = map[string]string{inspect.StatusGreen: "正常", inspect.StatusYellow: "关注", inspect.StatusRed: "异常"}
)

// Build assembles the report model.
func Build(in Input) *Report {
	r := &Report{Warnings: in.Detail.Warnings}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	r.GeneratedAt = fmtTime(in.Now)
	itemCfg := map[string]config.Item{}
	for _, it := range in.Items {
		itemCfg[it.ID] = it
	}
	if run := in.Run; run != nil {
		r.RunID = run.ID
		r.Trigger = run.Trigger
		r.RunStatus = run.Status
		r.StartedAt = fmtTime(run.StartedAt)
		if run.FinishedAt != nil {
			r.FinishedAt = fmtTime(*run.FinishedAt)
		}
		r.Duration = fmt.Sprintf("%.1f 秒", float64(run.DurationMS)/1000)
		r.Overview.Total, r.Overview.Normal = run.Hosts.Total, run.Hosts.OK
		r.Overview.Warn, r.Overview.Crit = run.Hosts.Warn, run.Hosts.Crit
		r.Overview.GroupCount, r.Overview.FindingCount = run.Groups, run.Findings
		r.Overview.QueryCount, r.Overview.QueryTotalMS, r.Overview.QueryP95MS = run.Query.Count, run.Query.TotalMS, run.Query.P95MS
	}
	r.Title = fmt.Sprintf("运维巡检报告 #%d", r.RunID)

	ov := &r.Overview
	ov.ByBiz = aggRows(in.Detail.ByBiz)
	ov.ByEnv = aggRows(in.Detail.ByEnv)
	ov.ByDC = aggRows(inspect.AggregateBy("dc", in.Detail.Hosts))

	for _, g := range in.Groups {
		r.Groups = append(r.Groups, groupSection(g))
	}
	r.Hosts = hostDetails(in, itemCfg)

	for _, t := range in.Detail.Top {
		sec := TopSection{Title: t.Title}
		for i, e := range t.Entries {
			extra := ""
			if mp := e.Extra["mountpoint"]; mp != "" {
				extra = "挂载点 " + mp
			}
			sec.Entries = append(sec.Entries, TopRow{Rank: i + 1, Host: e.Host, Value: formatValue(e.Value, t.Unit), Extra: extra})
		}
		r.Top = append(r.Top, sec)
	}

	for _, h := range in.Hosts {
		if len(h.MissingLabels) > 0 {
			r.MissingLabels = append(r.MissingLabels, MissingLabelHost{Host: h.Name, Missing: strings.Join(h.MissingLabels, ", ")})
		}
		r.Inventory = append(r.Inventory, inventoryHost(h))
		if h.Status != discovery.StatusNormal {
			dh := DownHost{Host: h.Name, Cause: downCause(h)}
			for _, t := range h.Targets {
				if !t.Up {
					dh.Targets = append(dh.Targets, DownTarget{Job: t.Job, Instance: t.Instance, LastError: oneLine(t.LastError)})
				}
			}
			if len(dh.Targets) == 0 {
				for _, j := range h.DownJobs {
					dh.Targets = append(dh.Targets, DownTarget{Job: j, LastError: oneLine(h.DownError)})
				}
			}
			r.Down = append(r.Down, dh)
		}
	}
	sort.Slice(r.Inventory, func(i, j int) bool { return r.Inventory[i].Name < r.Inventory[j].Name })
	sort.Slice(r.MissingLabels, func(i, j int) bool { return r.MissingLabels[i].Host < r.MissingLabels[j].Host })
	sort.Slice(r.Down, func(i, j int) bool { return r.Down[i].Host < r.Down[j].Host })

	for _, u := range in.Detail.Unclassified {
		r.Unclassified = append(r.Unclassified, UnclassifiedJob{Job: u.Job, Targets: u.Targets, Hosts: strings.Join(u.Hosts, ", ")})
	}
	r.Assets = assetSection(in.Detail.Diff)

	sources := map[string]int{}
	for _, it := range in.Detail.Items {
		src := sourceOf(itemCfg[it.ID])
		info := ItemInfo{ID: it.ID, Name: it.Name, Source: src, Result: "已执行", Findings: it.Findings}
		if it.Error != "" {
			info.Result = "执行失败"
			r.FailedItems = append(r.FailedItems, FailedItem{ID: it.ID, Name: it.Name, PromQL: it.PromQL, Error: oneLine(it.Error)})
		}
		sources[src]++
		r.Items = append(r.Items, info)
	}
	ov.FailedItemCount = len(r.FailedItems)
	r.SourceNote = sourceNote(sources)
	return r
}

func groupSection(g *inspect.ProblemGroup) GroupSection {
	s := GroupSection{Title: g.Title, CheckName: g.CheckName, Level: g.Level, LevelText: levelText[g.Level],
		Severity: g.Severity, Count: g.Count, Hosts: g.HostNames, Advice: g.AdviceRef}
	for _, f := range g.Common {
		s.Common = append(s.Common, f.Field+"="+f.Value)
	}
	return s
}

func hostDetails(in Input, itemCfg map[string]config.Item) []HostDetail {
	byHost := map[string][]*inspect.Finding{}
	for _, f := range in.Findings {
		byHost[f.HostKey] = append(byHost[f.HostKey], f)
	}
	snapshot := map[string]*discovery.Host{}
	for _, h := range in.Hosts {
		snapshot[h.Key] = h
	}
	var out []HostDetail
	for _, hh := range in.Detail.Hosts {
		fs := byHost[hh.Key]
		h := snapshot[hh.Key]
		if len(fs) == 0 && (h == nil || h.Normal()) {
			continue
		}
		d := HostDetail{Name: hh.Name, Key: hh.Key, Labels: labelText(hh.Labels), Score: hh.Score,
			Status: hh.Status, StatusText: statusText[hh.Status]}
		if h != nil && !h.Normal() {
			d.DownText = downCause(h)
		}
		sort.SliceStable(fs, func(i, j int) bool {
			if fs[i].Level != fs[j].Level {
				return fs[i].Level == inspect.LevelCrit
			}
			return fs[i].CheckID < fs[j].CheckID
		})
		for _, f := range fs {
			d.Findings = append(d.Findings, findingLine(f, itemCfg[f.CheckID]))
		}
		out = append(out, d)
	}
	return out
}

func inventoryHost(h *discovery.Host) InventoryHost {
	ih := InventoryHost{Name: h.Name, Status: inspect.StatusGreen, StatusText: "正常", Services: strings.Join(h.ServiceIDs(), ", ")}
	if !h.Normal() {
		ih.Status, ih.StatusText = inspect.StatusRed, "异常"
	}
	for _, t := range h.Targets {
		health := "正常"
		if !t.Up {
			health = "宕机"
		}
		ih.Targets = append(ih.Targets, InventoryTarget{Job: t.Job, Instance: t.Instance, Health: health})
	}
	return ih
}

func findingLine(f *inspect.Finding, it config.Item) FindingLine {
	unit := f.Unit
	th := f.Evidence.Threshold
	l := FindingLine{CheckID: f.CheckID, CheckName: f.CheckName, Level: f.Level, LevelText: levelText[f.Level],
		Job: f.Evidence.Labels["job"], Instance: f.Evidence.Labels["instance"],
		QueryID: f.Evidence.QueryID, PromQL: f.Evidence.PromQL, Value: formatValue(f.Value, unit),
		Threshold: th.Compare + " " + formatValue(th.Crossed, unit), Source: sourceOf(it)}
	if len(f.Extra) > 0 {
		keys := make([]string, 0, len(f.Extra))
		for k := range f.Extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + "=" + f.Extra[k]
		}
		l.Extra = strings.Join(parts, ", ")
	}
	return l
}

func aggRows(in []inspect.Aggregate) []AggRow {
	out := make([]AggRow, len(in))
	for i, a := range in {
		out[i] = AggRow{Name: a.Name, Hosts: a.Hosts, Green: a.Green, Yellow: a.Yellow, Red: a.Red,
			AvgScore: strconv.FormatFloat(a.AvgScore, 'f', 1, 64), Status: a.Status, StatusText: statusText[a.Status]}
	}
	return out
}

func assetSection(d *discovery.AssetDiff) AssetSection {
	if d == nil || !d.HasPrevious {
		return AssetSection{Summary: "无上次巡检记录，本次不统计资产变化。"}
	}
	s := AssetSection{HasPrevious: true}
	if d.Empty() {
		s.Summary = fmt.Sprintf("与上次巡检（#%d）相比，主机与服务均无变化。", d.PreviousRunID)
		return s
	}
	s.Summary = fmt.Sprintf("与上次巡检（#%d）相比：新增主机 %d 台，移除主机 %d 台，服务新增 %d 台，服务移除 %d 台。",
		d.PreviousRunID, len(d.AddedHosts), len(d.RemovedHosts), len(d.ServicesAdded), len(d.ServicesRemoved))
	for _, h := range d.AddedHosts {
		s.Lines = append(s.Lines, "新增主机 "+h.Name)
	}
	for _, h := range d.RemovedHosts {
		s.Lines = append(s.Lines, "移除主机 "+h.Name)
	}
	for _, c := range d.ServicesAdded {
		s.Lines = append(s.Lines, fmt.Sprintf("服务新增 %s: %s", c.Name, strings.Join(c.Services, ", ")))
	}
	for _, c := range d.ServicesRemoved {
		s.Lines = append(s.Lines, fmt.Sprintf("服务移除 %s: %s", c.Name, strings.Join(c.Services, ", ")))
	}
	return s
}

func downCause(h *discovery.Host) string {
	if h.DownText != "" {
		return h.DownText
	}
	switch h.DownCause {
	case discovery.CauseHostDown:
		return discovery.CauseHostDownText
	case discovery.CauseExporterDown:
		return discovery.CauseExporterDownText
	}
	return "目标不可达"
}

func sourceOf(it config.Item) string {
	if it.Source != "" {
		return it.Source
	}
	return config.DefaultSource
}

func sourceNote(sources map[string]int) string {
	if len(sources) == 0 {
		return ""
	}
	names := make([]string, 0, len(sources))
	for s := range sources {
		names = append(names, s)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, s := range names {
		parts[i] = fmt.Sprintf("%s（%d 项）", s, sources[s])
	}
	note := "检查项来源：" + strings.Join(parts, "；") + "。"
	if _, ok := sources[config.DefaultSource]; ok {
		note += "通用默认阈值仅供参考，请以企业标准替换后再作为考核依据。"
	}
	return note
}

func labelText(l map[string]string) string {
	var parts []string
	for _, f := range discovery.LabelFields {
		if v := l[f]; v != "" {
			parts = append(parts, f+"="+v)
		}
	}
	return strings.Join(parts, " ")
}

// formatValue prints at most two decimals without trailing zeros.
func formatValue(v float64, unit string) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s + unit
}

func fmtTime(t time.Time) string { return t.Format("2006-01-02 15:04:05") }

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
