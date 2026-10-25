package inspect

import (
	"fmt"
	"sort"

	"yunwei/internal/discovery"
)

// titleSuffix words the breach direction of an operator.
func titleSuffix(op string) string {
	switch op {
	case "<", "<=":
		return "低于阈值"
	case "==":
		return "等于阈值"
	case "!=":
		return "不等于阈值"
	}
	return "超过阈值"
}

// buildGroups merges findings by (check_id, level) and orders the groups by
// severity, then host count descending.
func buildGroups(findings []*Finding, hosts map[string]*discovery.Host) []*ProblemGroup {
	type key struct{ check, level string }
	byKey := map[key]*ProblemGroup{}
	var order []key
	for _, f := range findings {
		k := key{f.CheckID, f.Level}
		g := byKey[k]
		if g == nil {
			g = &ProblemGroup{
				CheckID: f.CheckID, CheckName: f.CheckName, Level: f.Level,
				Severity: f.Severity, Unit: f.Unit, AdviceRef: f.AdviceRef,
			}
			byKey[k] = g
			order = append(order, k)
		}
		g.Findings = append(g.Findings, f)
	}

	groups := make([]*ProblemGroup, 0, len(order))
	for _, k := range order {
		g := byKey[k]
		sort.Slice(g.Findings, func(i, j int) bool { return findingLess(g.Findings[i], g.Findings[j]) })
		var affected []*discovery.Host
		seen := map[string]bool{}
		for _, f := range g.Findings {
			if seen[f.HostKey] {
				continue
			}
			seen[f.HostKey] = true
			g.Hosts = append(g.Hosts, f.HostKey)
			g.HostNames = append(g.HostNames, f.Host)
			if h := hosts[f.HostKey]; h != nil {
				affected = append(affected, h)
			}
		}
		g.Count = len(g.Hosts)
		g.Common = commonFeatures(affected)
		g.Title = fmt.Sprintf("%s%s（%d 台）", g.CheckName, titleSuffix(g.Findings[0].Evidence.Threshold.Compare), g.Count)
		groups = append(groups, g)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if ra, rb := SeverityRank(a.Severity), SeverityRank(b.Severity); ra != rb {
			return ra > rb
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.CheckID != b.CheckID {
			return a.CheckID < b.CheckID
		}
		return rank(a.Level) > rank(b.Level)
	})
	return groups
}

// commonFeatures lists env/role/biz/dc values shared by every host. A host
// lacking the label breaks the commonality.
func commonFeatures(hosts []*discovery.Host) []Feature {
	if len(hosts) == 0 {
		return nil
	}
	var out []Feature
	for _, field := range []string{"env", "role", "biz", "dc"} {
		v := hosts[0].Labels[field]
		if v == "" {
			continue
		}
		same := true
		for _, h := range hosts[1:] {
			if h.Labels[field] != v {
				same = false
				break
			}
		}
		if same {
			out = append(out, Feature{Field: field, Value: v})
		}
	}
	return out
}
