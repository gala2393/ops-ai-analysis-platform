package config

import (
	"strings"
	"time"
)

// Scenario drives the mock Prometheus generator. Unknown keys are tolerated so
// scenario files can carry generator-specific sections.
type Scenario struct {
	Hosts        int           `yaml:"hosts"`
	Seed         int64         `yaml:"seed"`
	History      time.Duration `yaml:"history"`
	Step         time.Duration `yaml:"step"`
	LiveInterval time.Duration `yaml:"live_interval"`

	// MissingLabelFraction is the share of hosts that lack some enterprise labels.
	MissingLabelFraction float64 `yaml:"missing_label_fraction"`

	Labels map[string][]string `yaml:"labels"`
}

// LoadScenario loads and validates a mock scenario file.
func LoadScenario(path string) (*Scenario, error) {
	sc := &Scenario{History: 6 * time.Hour, Step: 60 * time.Second, LiveInterval: 30 * time.Second}
	if _, err := readYAML(path, sc, false); err != nil {
		return nil, err
	}
	p := &problems{file: path}
	if sc.Hosts < 1 {
		p.add("hosts", "必须 >= 1 (must be >= 1)")
	}
	if sc.History <= 0 {
		p.add("history", "必须大于 0 (must be > 0)")
	}
	if sc.Step <= 0 {
		p.add("step", "必须大于 0 (must be > 0)")
	}
	if sc.History > 0 && sc.Step > 0 && sc.Step > sc.History {
		p.add("step", "不能大于 history (step must not exceed history)")
	}
	if sc.LiveInterval < 0 {
		p.add("live_interval", "不能为负数 (must not be negative)")
	}
	if sc.MissingLabelFraction < 0 || sc.MissingLabelFraction > 1 {
		p.add("missing_label_fraction", "必须在 0 到 1 之间 (must be within 0..1)")
	}
	for name, vals := range sc.Labels {
		if len(vals) == 0 || strings.TrimSpace(name) == "" {
			p.add("labels."+name, "取值列表不能为空 (value list must not be empty)")
		}
	}
	if err := p.err(); err != nil {
		return nil, err
	}
	return sc, nil
}
