package web

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"yunwei/internal/discovery"
	"yunwei/internal/inspect"
	"yunwei/internal/store"
)

// runView decodes the stored documents of one run into typed structures.
type runView struct {
	Run      *store.Run
	Detail   inspect.Detail
	Findings []*inspect.Finding
	Groups   []*inspect.ProblemGroup
	// Hosts is the discovery snapshot by host key.
	Hosts map[string]*discovery.Host
	// Health is the evaluated per-host health by host key.
	Health map[string]*inspect.HostHealth
}

func newRunView(rd *store.RunDetail) (*runView, error) {
	v := &runView{
		Run:    rd.Run,
		Hosts:  map[string]*discovery.Host{},
		Health: map[string]*inspect.HostHealth{},
	}
	if len(rd.Detail) > 0 {
		if err := json.Unmarshal(rd.Detail, &v.Detail); err != nil {
			return nil, fmt.Errorf("读取巡检详情失败: %w", err)
		}
	}
	for _, raw := range rd.Findings {
		var f inspect.Finding
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("读取发现项失败: %w", err)
		}
		v.Findings = append(v.Findings, &f)
	}
	for _, raw := range rd.Groups {
		var g inspect.ProblemGroup
		if err := json.Unmarshal(raw, &g); err != nil {
			return nil, fmt.Errorf("读取问题分组失败: %w", err)
		}
		v.Groups = append(v.Groups, &g)
	}
	for _, raw := range rd.Hosts {
		var h discovery.Host
		if err := json.Unmarshal(raw, &h); err != nil {
			return nil, fmt.Errorf("读取主机快照失败: %w", err)
		}
		v.Hosts[h.Key] = &h
	}
	for _, hh := range v.Detail.Hosts {
		v.Health[hh.Key] = hh
	}
	return v, nil
}

// hostRow is one row of the server list, joining evaluated health with the
// discovery snapshot.
type hostRow struct {
	Key      string            `json:"key"`
	Name     string            `json:"name"`
	IPs      string            `json:"-"`
	IPList   []string          `json:"ips"`
	Env      string            `json:"env"`
	Role     string            `json:"role"`
	Biz      string            `json:"biz"`
	DC       string            `json:"dc"`
	Owner    string            `json:"owner"`
	Labels   map[string]string `json:"labels"`
	Missing  []string          `json:"missing_labels,omitempty"`
	Score    int               `json:"score"`
	Status   string            `json:"status"`
	StatusZh string            `json:"status_text"`
	Services []string          `json:"services"`
	DownText string            `json:"down_cause,omitempty"`
}

// hostRows joins the run's host health (already sorted worst-first) with the
// discovery snapshot. The second return value holds the distinct filter
// option values per dimension.
func (v *runView) hostRows() (rows []hostRow, options map[string][]string) {
	options = map[string][]string{}
	seen := map[string]map[string]bool{"env": {}, "role": {}, "biz": {}, "dc": {}}
	for _, hh := range v.Detail.Hosts {
		row := hostRow{
			Key: hh.Key, Name: hh.Name, Score: hh.Score, Status: hh.Status,
			StatusZh: statusText(hh.Status), Labels: hh.Labels,
			Env: hh.Group["env"], Role: hh.Group["role"], Biz: hh.Group["biz"],
			DC: hh.Group["dc"], Owner: hh.Group["owner"],
			IPList: []string{hh.Key},
		}
		if h := v.Hosts[hh.Key]; h != nil {
			if len(h.IPs) > 0 {
				row.IPList = h.IPs
			}
			row.Services = h.ServiceIDs()
			row.Missing = h.MissingLabels
			row.Labels = h.Labels
			if !h.Normal() {
				row.DownText = downText(h)
			}
		}
		row.IPs = strings.Join(row.IPList, ", ")
		rows = append(rows, row)
		for _, dim := range []string{"env", "role", "biz", "dc"} {
			val := hh.Group[dim]
			if val != "" && !seen[dim][val] {
				seen[dim][val] = true
				options[dim] = append(options[dim], val)
			}
		}
	}
	for _, dim := range []string{"env", "role", "biz", "dc"} {
		sort.Strings(options[dim])
	}
	return rows, options
}

// decodeFinding unmarshals one stored finding document.
func decodeFinding(raw json.RawMessage) (*inspect.Finding, error) {
	var f inspect.Finding
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// hostFilter is the server list filter, shared by the HTML pages and the API.
type hostFilter struct {
	Env, Role, Biz, DC, Status, Q string
}

func filterFromQuery(vals map[string][]string) hostFilter {
	get := func(k string) string {
		if vs := vals[k]; len(vs) > 0 {
			return strings.TrimSpace(vs[0])
		}
		return ""
	}
	return hostFilter{Env: get("env"), Role: get("role"), Biz: get("biz"), DC: get("dc"), Status: get("status"), Q: get("q")}
}

func (f hostFilter) active() bool {
	return f.Env != "" || f.Role != "" || f.Biz != "" || f.DC != "" || f.Status != "" || f.Q != ""
}

// matches reports whether a row passes the filter. Label filters compare the
// effective grouping value (label value, or the job/instance fallback for
// hosts missing that label); the search matches host name and IP.
func (f hostFilter) matches(r hostRow) bool {
	if f.Env != "" && r.Env != f.Env {
		return false
	}
	if f.Role != "" && r.Role != f.Role {
		return false
	}
	if f.Biz != "" && r.Biz != f.Biz {
		return false
	}
	if f.DC != "" && r.DC != f.DC {
		return false
	}
	if f.Status != "" && r.Status != f.Status {
		return false
	}
	if f.Q != "" {
		q := strings.ToLower(f.Q)
		if !strings.Contains(strings.ToLower(r.Name), q) &&
			!strings.Contains(strings.ToLower(r.IPs), q) &&
			!strings.Contains(strings.ToLower(r.Key), q) {
			return false
		}
	}
	return true
}

func filterRows(rows []hostRow, f hostFilter) []hostRow {
	if !f.active() {
		return rows
	}
	out := make([]hostRow, 0, len(rows))
	for _, r := range rows {
		if f.matches(r) {
			out = append(out, r)
		}
	}
	return out
}

// rowsData is the model of the host-rows partial.
type rowsData struct {
	Rows     []hostRow
	Filtered bool
}

// downText renders the host-level down cause in Chinese.
func downText(h *discovery.Host) string {
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

// serviceName resolves a service id to its configured Chinese name.
func (s *Server) serviceName(id string) string {
	for _, r := range s.cfg.Services.Services {
		if r.ID == id {
			return r.Name
		}
	}
	return id
}

// sourceText renders a detection source in Chinese.
func sourceText(src string) string {
	switch src {
	case discovery.SourceJob:
		return "job 标签"
	case discovery.SourceApp:
		return "app 标签"
	case discovery.SourceMetric:
		return "指标前缀"
	case discovery.SourceSystemd:
		return "systemd 单元"
	}
	return src
}
