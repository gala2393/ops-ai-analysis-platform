package web

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"yunwei/internal/config"
)

// checkItemRow is one inspection item on the read-only checks page.
type checkItemRow struct {
	ID          string
	Name        string
	AppliesTo   string
	PromQL      string
	Compare     string
	Warn        string
	Crit        string
	Severity    string
	Unit        string
	Description string
	AdviceRef   string
	Source      string
	Enabled     string
}

// overrideRow is one (match, item) threshold override.
type overrideRow struct {
	Match  string
	Item   string
	Change string
}

// serviceRuleRow is one service detection rule.
type serviceRuleRow struct {
	ID       string
	Name     string
	Jobs     string
	Apps     string
	Prefixes string
}

// systemdRow maps one systemd unit to a service.
type systemdRow struct {
	Unit    string
	Service string
}

// checksData is the model of the read-only checks page.
type checksData struct {
	Title         string
	Page          string
	Items         []checkItemRow
	Overrides     []overrideRow
	Services      []serviceRuleRow
	SystemdMetric string
	SystemdMap    []systemdRow
}

// checksPage renders `/checks`: every configured inspection item with its
// thresholds, the group overrides and the service detection rules. The page
// is read-only by construction; there are no edit controls.
func (s *Server) checksPage(w http.ResponseWriter, r *http.Request) {
	data := checksData{Title: "巡检项", Page: "checks"}
	insp := s.cfg.Inspection
	units := map[string]string{}
	for _, it := range insp.Items {
		units[it.ID] = it.Unit
	}
	for _, it := range insp.Items {
		applies := "全部主机"
		if len(it.AppliesTo) > 0 {
			names := make([]string, len(it.AppliesTo))
			for i, id := range it.AppliesTo {
				names[i] = s.serviceName(id)
			}
			applies = strings.Join(names, ", ")
		}
		enabled := "是"
		if !it.IsEnabled() {
			enabled = "否"
		}
		source := it.Source
		if source == "" {
			source = config.DefaultSource
		}
		data.Items = append(data.Items, checkItemRow{
			ID: it.ID, Name: it.Name, AppliesTo: applies, PromQL: it.Query,
			Compare: it.Compare, Warn: thresholdCell(it.Warn, it.Unit), Crit: thresholdCell(it.Crit, it.Unit),
			Severity: severityText(it.Severity), Unit: it.Unit, Description: it.Description,
			AdviceRef: it.AdviceRef, Source: source, Enabled: enabled,
		})
	}
	for _, o := range insp.Overrides {
		match := matchText(o.Match)
		ids := make([]string, 0, len(o.Items))
		for id := range o.Items {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			ov := o.Items[id]
			var parts []string
			if ov.Warn != nil {
				parts = append(parts, "warn "+fmtValue(*ov.Warn, units[id]))
			}
			if ov.Crit != nil {
				parts = append(parts, "crit "+fmtValue(*ov.Crit, units[id]))
			}
			data.Overrides = append(data.Overrides, overrideRow{
				Match: match, Item: id, Change: strings.Join(parts, ", "),
			})
		}
	}
	for _, sr := range s.cfg.Services.Services {
		data.Services = append(data.Services, serviceRuleRow{
			ID: sr.ID, Name: sr.Name,
			Jobs: orDash(sr.Match.Jobs), Apps: orDash(sr.Match.Apps), Prefixes: orDash(sr.Match.MetricPrefixes),
		})
	}
	if len(s.cfg.Services.SystemdUnits.Map) > 0 {
		data.SystemdMetric = s.cfg.Services.SystemdUnits.Metric
		unitNames := make([]string, 0, len(s.cfg.Services.SystemdUnits.Map))
		for u := range s.cfg.Services.SystemdUnits.Map {
			unitNames = append(unitNames, u)
		}
		sort.Strings(unitNames)
		for _, u := range unitNames {
			data.SystemdMap = append(data.SystemdMap, systemdRow{Unit: u, Service: s.cfg.Services.SystemdUnits.Map[u]})
		}
	}
	s.render(w, r, "checks", http.StatusOK, data)
}

// thresholdCell renders an optional threshold pointer with its unit.
func thresholdCell(v *float64, unit string) string {
	if v == nil {
		return "—"
	}
	return fmtValue(*v, unit)
}

// matchText renders an override match block as sorted k=v pairs.
func matchText(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%s", k, m[k])
	}
	return strings.Join(parts, ", ")
}

func orDash(vs []string) string {
	if len(vs) == 0 {
		return "—"
	}
	return strings.Join(vs, ", ")
}
