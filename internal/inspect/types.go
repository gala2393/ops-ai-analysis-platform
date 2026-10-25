// Package inspect evaluates the configured check items against Prometheus:
// one aggregated instant query per item, series mapped to discovered hosts,
// thresholds with overrides, findings with evidence, problem groups, health
// scores and Top N rankings. Code decides abnormality; nothing here calls an LLM.
package inspect

import "time"

// Levels of a finding. Hosts below every threshold have no finding.
const (
	LevelOK   = "ok"
	LevelWarn = "warn"
	LevelCrit = "crit"
)

// Host health status.
const (
	StatusGreen  = "green"
	StatusYellow = "yellow"
	StatusRed    = "red"
)

// Threshold is the effective threshold of a finding, after overrides.
type Threshold struct {
	Compare string   `json:"compare"`
	Warn    *float64 `json:"warn,omitempty"`
	Crit    *float64 `json:"crit,omitempty"`
	// Crossed is the threshold value the sample actually breached.
	Crossed float64 `json:"crossed"`
	// Overridden is true when at least one override changed warn or crit.
	Overridden bool `json:"overridden,omitempty"`
}

// Evidence ties a finding to the exact query and value that produced it.
type Evidence struct {
	QueryID   string            `json:"query_id"`
	PromQL    string            `json:"promql"`
	Value     float64           `json:"value"`
	Labels    map[string]string `json:"labels"`
	Threshold Threshold         `json:"threshold"`
}

// Finding is one abnormal (check, host) pair. When a host has several
// breaching series (e.g. mountpoints) the most extreme one is kept.
type Finding struct {
	CheckID   string `json:"check_id"`
	CheckName string `json:"check_name"`
	HostKey   string `json:"host_key"`
	Host      string `json:"host"`
	Level     string `json:"level"`
	// Severity is the item severity, lowered one step for warn findings.
	Severity string  `json:"severity"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit,omitempty"`
	// Extra holds the item's group_by_extra labels (e.g. mountpoint) of the kept series.
	Extra map[string]string `json:"extra,omitempty"`
	// Breaching is the number of series of this host that crossed a threshold.
	Breaching int      `json:"breaching"`
	AdviceRef string   `json:"advice_ref,omitempty"`
	Evidence  Evidence `json:"evidence"`
}

// Feature is one label value shared by every host of a problem group.
type Feature struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// ProblemGroup merges the findings of one (check, level) pair.
type ProblemGroup struct {
	CheckID   string    `json:"check_id"`
	CheckName string    `json:"check_name"`
	Level     string    `json:"level"`
	Severity  string    `json:"severity"`
	Title     string    `json:"title"`
	Count     int       `json:"count"`
	Hosts     []string  `json:"hosts"`
	HostNames []string  `json:"host_names"`
	Common    []Feature `json:"common"`
	Unit      string    `json:"unit,omitempty"`
	AdviceRef string    `json:"advice_ref,omitempty"`
	// Findings are ordered by host name.
	Findings []*Finding `json:"findings"`
}

// HostHealth is the score and status of one host.
type HostHealth struct {
	Key    string            `json:"key"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	// Group holds the value each label field aggregates under: the label, or
	// the discovery job/instance fallback when the label is missing.
	Group     map[string]string `json:"group,omitempty"`
	Score     int               `json:"score"`
	Status    string            `json:"status"`
	Crit      int               `json:"crit"`
	Warn      int               `json:"warn"`
	DownCause string            `json:"down_cause,omitempty"`
}

// Aggregate summarises host health for one biz or env value.
type Aggregate struct {
	Dimension string  `json:"dimension"`
	Name      string  `json:"name"`
	Hosts     int     `json:"hosts"`
	Green     int     `json:"green"`
	Yellow    int     `json:"yellow"`
	Red       int     `json:"red"`
	AvgScore  float64 `json:"avg_score"`
	Status    string  `json:"status"`
}

// TopEntry is one ranked host.
type TopEntry struct {
	HostKey string            `json:"host_key"`
	Host    string            `json:"host"`
	Value   float64           `json:"value"`
	Extra   map[string]string `json:"extra,omitempty"`
}

// TopList is one Top N ranking.
type TopList struct {
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	Unit    string     `json:"unit,omitempty"`
	Entries []TopEntry `json:"entries"`
}

// ItemRun records what happened to one check item.
type ItemRun struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	QueryID string `json:"query_id"`
	PromQL  string `json:"promql"`
	// Series is the number of samples returned; Hosts the number evaluated
	// after host mapping and applies_to filtering.
	Series   int           `json:"series"`
	Hosts    int           `json:"hosts"`
	Findings int           `json:"findings"`
	Duration time.Duration `json:"duration_ns"`
	// Error is set when the item could not be evaluated; other items continue.
	Error string `json:"error,omitempty"`
}

// HostCounts splits hosts by health status.
type HostCounts struct {
	Total int `json:"total"`
	OK    int `json:"ok"`
	Warn  int `json:"warn"`
	Crit  int `json:"crit"`
}

// Result is the outcome of one inspection evaluation.
type Result struct {
	Items    []ItemRun       `json:"items"`
	Findings []*Finding      `json:"findings"`
	Groups   []*ProblemGroup `json:"groups"`
	Hosts    []*HostHealth   `json:"hosts"`
	ByBiz    []Aggregate     `json:"by_biz"`
	ByEnv    []Aggregate     `json:"by_env"`
	Top      []TopList       `json:"top"`
	Counts   HostCounts      `json:"counts"`
	// Queries is the number of Prometheus queries the evaluation issued.
	Queries int `json:"queries"`
	// Unmapped counts samples whose instance matched no discovered host.
	Unmapped int `json:"unmapped"`
}

// Errors returns the items that failed.
func (r *Result) Errors() []ItemRun {
	var out []ItemRun
	for _, it := range r.Items {
		if it.Error != "" {
			out = append(out, it)
		}
	}
	return out
}
