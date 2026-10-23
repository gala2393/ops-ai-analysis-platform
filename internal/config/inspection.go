package config

import (
	"fmt"
	"sort"
	"strings"
)

// Compare operators allowed in an inspection item.
var compareOps = map[string]bool{">": true, ">=": true, "<": true, "<=": true, "==": true, "!=": true}

var severities = map[string]bool{"low": true, "medium": true, "high": true, "critical": true}

// DefaultSource marks items that come from generic defaults rather than an enterprise standard.
const DefaultSource = "通用默认（待企业标准替换）"

type Inspection struct {
	Items     []Item     `yaml:"items"`
	Overrides []Override `yaml:"overrides"`
}

// Item is one inspection check evaluated with a single aggregated instant query.
type Item struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	Enabled      *bool    `yaml:"enabled"`
	AppliesTo    []string `yaml:"applies_to"`
	Query        string   `yaml:"query"`
	Compare      string   `yaml:"compare"`
	Warn         *float64 `yaml:"warn"`
	Crit         *float64 `yaml:"crit"`
	Severity     string   `yaml:"severity"`
	Unit         string   `yaml:"unit"`
	Description  string   `yaml:"description"`
	AdviceRef    string   `yaml:"advice_ref"`
	GroupByExtra []string `yaml:"group_by_extra"`
	Source       string   `yaml:"source"`
}

// IsEnabled is true unless the item sets enabled: false.
func (i Item) IsEnabled() bool { return i.Enabled == nil || *i.Enabled }

// Override replaces thresholds for hosts whose internal label fields match.
// Later overrides take precedence over earlier ones.
type Override struct {
	Match map[string]string            `yaml:"match"`
	Items map[string]ThresholdOverride `yaml:"items"`
}

type ThresholdOverride struct {
	Warn *float64 `yaml:"warn"`
	Crit *float64 `yaml:"crit"`
}

// LoadInspection loads and validates inspection.yaml (structure only; use
// ValidateAgainst for cross-file references).
func LoadInspection(path string) (*Inspection, error) {
	var in Inspection
	if _, err := readYAML(path, &in, true); err != nil {
		return nil, err
	}
	if err := in.validate(path); err != nil {
		return nil, err
	}
	return &in, nil
}

func (in *Inspection) validate(file string) error {
	p := &problems{file: file}
	if len(in.Items) == 0 {
		p.add("items", "至少需要一个巡检项 (at least one item is required)")
	}
	ids := map[string]int{}
	for i := range in.Items {
		it := &in.Items[i]
		f := func(name string) string { return fmt.Sprintf("items[%d].%s", i, name) }
		if strings.TrimSpace(it.ID) == "" {
			p.add(f("id"), "不能为空 (must not be empty)")
		} else if prev, dup := ids[it.ID]; dup {
			p.add(f("id"), "巡检项 id %q 重复, 与 items[%d] 相同 (duplicate item id)", it.ID, prev)
		} else {
			ids[it.ID] = i
		}
		label := it.ID
		if label == "" {
			label = fmt.Sprintf("#%d", i)
		}
		if strings.TrimSpace(it.Name) == "" {
			p.add(f("name"), "巡检项 %s 缺少 name (name is required)", label)
		}
		if strings.TrimSpace(it.Query) == "" {
			p.add(f("query"), "巡检项 %s 缺少 query (query is required)", label)
		}
		if !compareOps[it.Compare] {
			p.add(f("compare"), "巡检项 %s 的比较符 %q 无效, 可用: > >= < <= == != (invalid compare operator)", label, it.Compare)
		}
		if it.Severity == "" {
			it.Severity = "medium"
		} else if !severities[it.Severity] {
			p.add(f("severity"), "巡检项 %s 的 severity %q 无效, 可用: low medium high critical (invalid severity)", label, it.Severity)
		}
		if it.Warn == nil && it.Crit == nil {
			p.add(f("warn"), "巡检项 %s 至少需要设置 warn 或 crit 阈值 (set warn and/or crit)", label)
		}
		if it.Warn != nil && it.Crit != nil && compareOps[it.Compare] {
			if !crossesInOrder(it.Compare, *it.Warn, *it.Crit) {
				p.add(f("crit"), "巡检项 %s 的 crit(%v) 应比 warn(%v) 更严重 (crit must be stricter than warn for %q)", label, *it.Crit, *it.Warn, it.Compare)
			}
		}
		for j, g := range it.GroupByExtra {
			if strings.TrimSpace(g) == "" {
				p.add(fmt.Sprintf("items[%d].group_by_extra[%d]", i, j), "标签名不能为空 (label name must not be empty)")
			}
		}
	}

	for i, o := range in.Overrides {
		f := func(name string) string { return fmt.Sprintf("overrides[%d].%s", i, name) }
		if len(o.Match) == 0 {
			p.add(f("match"), "不能为空 (must not be empty)")
		}
		if len(o.Items) == 0 {
			p.add(f("items"), "不能为空 (must not be empty)")
		}
		for _, id := range sortedKeys(o.Items) {
			idx, known := ids[id]
			if !known {
				p.add(f("items."+id), "引用了不存在的巡检项 id %q (unknown item id)", id)
				continue
			}
			ov := o.Items[id]
			if ov.Warn == nil && ov.Crit == nil {
				p.add(f("items."+id), "至少需要覆盖 warn 或 crit (set warn and/or crit)")
				continue
			}
			it := in.Items[idx]
			warn, crit := it.Warn, it.Crit
			if ov.Warn != nil {
				warn = ov.Warn
			}
			if ov.Crit != nil {
				crit = ov.Crit
			}
			if warn != nil && crit != nil && compareOps[it.Compare] && !crossesInOrder(it.Compare, *warn, *crit) {
				p.add(f("items."+id), "覆盖后 crit(%v) 应比 warn(%v) 更严重 (crit must be stricter than warn)", *crit, *warn)
			}
		}
	}
	return p.err()
}

// crossesInOrder reports whether crit is at least as severe as warn for the operator.
func crossesInOrder(op string, warn, crit float64) bool {
	switch op {
	case ">", ">=":
		return crit >= warn
	case "<", "<=":
		return crit <= warn
	default:
		return true
	}
}

// ValidateAgainst checks references that point into other files: applies_to
// service ids and override match keys.
func (in *Inspection) ValidateAgainst(file string, svc *Services, labels LabelsConfig) error {
	p := &problems{file: file}
	known := map[string]bool{}
	for _, s := range svc.Services {
		known[s.ID] = true
	}
	for i, it := range in.Items {
		for j, id := range it.AppliesTo {
			if !known[id] {
				p.add(fmt.Sprintf("items[%d].applies_to[%d]", i, j),
					"巡检项 %s 引用了 services.yaml 中不存在的服务 id %q (unknown service id)", it.ID, id)
			}
		}
	}
	fields := labels.Fields()
	for i, o := range in.Overrides {
		for _, k := range sortedKeys(o.Match) {
			if _, ok := fields[k]; !ok {
				p.add(fmt.Sprintf("overrides[%d].match.%s", i, k),
					"未知的匹配字段 %q, 可用: env role biz dc owner (unknown match field)", k)
			}
		}
	}
	return p.err()
}

// EnabledItems returns the items that should be evaluated.
func (in *Inspection) EnabledItems() []Item {
	out := make([]Item, 0, len(in.Items))
	for _, it := range in.Items {
		if it.IsEnabled() {
			out = append(out, it)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
