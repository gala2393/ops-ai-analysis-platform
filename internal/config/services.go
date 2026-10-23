package config

import (
	"fmt"
	"strings"
)

type Services struct {
	Services     []ServiceRule `yaml:"services"`
	SystemdUnits SystemdUnits  `yaml:"systemd_units"`
}

// ServiceRule describes how to detect one service type. Jobs and Apps are
// case-insensitive substring matches against target labels; MetricPrefixes
// are matched against metric names.
type ServiceRule struct {
	ID    string    `yaml:"id"`
	Name  string    `yaml:"name"`
	Match MatchRule `yaml:"match"`
}

type MatchRule struct {
	Jobs           []string `yaml:"jobs"`
	Apps           []string `yaml:"apps"`
	MetricPrefixes []string `yaml:"metric_prefixes"`
}

type SystemdUnits struct {
	Metric string            `yaml:"metric"`
	Map    map[string]string `yaml:"map"`
}

// LoadServices loads and validates services.yaml.
func LoadServices(path string) (*Services, error) {
	var s Services
	if _, err := readYAML(path, &s, true); err != nil {
		return nil, err
	}
	if err := s.validate(path); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Services) validate(file string) error {
	p := &problems{file: file}
	if len(s.Services) == 0 {
		p.add("services", "至少需要一条服务规则 (at least one service rule is required)")
	}
	ids := map[string]int{}
	for i, r := range s.Services {
		f := func(name string) string { return fmt.Sprintf("services[%d].%s", i, name) }
		if strings.TrimSpace(r.ID) == "" {
			p.add(f("id"), "不能为空 (must not be empty)")
		} else if prev, dup := ids[r.ID]; dup {
			p.add(f("id"), "服务 id %q 重复, 与 services[%d] 相同 (duplicate service id)", r.ID, prev)
		} else {
			ids[r.ID] = i
		}
		if len(r.Match.Jobs)+len(r.Match.Apps)+len(r.Match.MetricPrefixes) == 0 {
			p.add(f("match"), "服务 %s 至少需要 jobs / apps / metric_prefixes 之一 (at least one match rule)", r.ID)
		}
		for _, list := range []struct {
			name string
			vals []string
		}{{"jobs", r.Match.Jobs}, {"apps", r.Match.Apps}, {"metric_prefixes", r.Match.MetricPrefixes}} {
			for j, v := range list.vals {
				if strings.TrimSpace(v) == "" {
					p.add(fmt.Sprintf("services[%d].match.%s[%d]", i, list.name, j), "不能为空 (must not be empty)")
				}
			}
		}
	}
	if len(s.SystemdUnits.Map) > 0 && strings.TrimSpace(s.SystemdUnits.Metric) == "" {
		p.add("systemd_units.metric", "配置了 map 时必须指定 metric (metric is required when map is set)")
	}
	for _, unit := range sortedKeys(s.SystemdUnits.Map) {
		if id := s.SystemdUnits.Map[unit]; !s.Has(id) {
			p.add("systemd_units.map."+unit, "引用了不存在的服务 id %q (unknown service id)", id)
		}
	}
	return p.err()
}

// Has reports whether a service id is defined.
func (s *Services) Has(id string) bool {
	for _, r := range s.Services {
		if r.ID == id {
			return true
		}
	}
	return false
}
