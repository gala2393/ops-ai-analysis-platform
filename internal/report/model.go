// Package report turns an inspection run into a three-layer report (overview,
// problem groups, per-host detail) and renders it as Markdown and HTML.
// The model is fully pre-formatted text so both renderers stay trivial and
// later additions (AI sections, degradation banner) only fill optional fields.
package report

// Report is the renderer-independent document.
type Report struct {
	RunID       int64
	Title       string
	StartedAt   string
	FinishedAt  string
	Trigger     string
	RunStatus   string
	Duration    string
	GeneratedAt string

	// Banner is a notice shown at the top, e.g. "本报告未包含 AI 分析" when
	// AI analysis is degraded. Nil when there is nothing to announce.
	Banner *Banner

	Overview Overview
	Groups   []GroupSection
	Hosts    []HostDetail

	Top           []TopSection
	MissingLabels []MissingLabelHost
	Assets        AssetSection
	Unclassified  []UnclassifiedJob
	Down          []DownHost
	FailedItems   []FailedItem
	Items         []ItemInfo
	// SourceNote states where the check items come from.
	SourceNote string
	Warnings   []string
}

// Banner is a report-level notice.
type Banner struct {
	Level string // info | warn
	Text  string
}

// Overview is layer one: totals and status per biz, dc and env.
type Overview struct {
	Total, Normal, Warn, Crit int
	GroupCount, FindingCount  int
	QueryCount                int
	QueryTotalMS, QueryP95MS  int64
	FailedItemCount           int
	ByBiz, ByDC, ByEnv        []AggRow
}

// AggRow is one biz/dc/env bucket.
type AggRow struct {
	Name       string
	Hosts      int
	Green      int
	Yellow     int
	Red        int
	AvgScore   string
	Status     string // green | yellow | red
	StatusText string
}

// GroupSection is layer two: one problem group.
type GroupSection struct {
	Title     string
	CheckName string
	Level     string // warn | crit
	LevelText string
	Severity  string
	Count     int
	Hosts     []string
	Common    []string
	Advice    string
	// AI is filled by the analysis pipeline; nil means no AI section.
	AI *AISection
}

// AISection is the model-written explanation of one problem group.
type AISection struct {
	Phenomenon string
	Causes     []string
	Steps      []string
	Impact     string
	Risks      []string
	// Note carries validation marks such as "部分证据未通过校验".
	Note string
}

// HostDetail is layer three: one host with its findings.
type HostDetail struct {
	Name       string
	Key        string
	Labels     string
	Score      int
	Status     string
	StatusText string
	DownText   string
	Findings   []FindingLine
}

// FindingLine is one finding with its evidence.
type FindingLine struct {
	CheckID   string
	CheckName string
	Level     string
	LevelText string
	QueryID   string
	PromQL    string
	Value     string
	Threshold string
	Extra     string
	Source    string
}

// TopSection is one Top N ranking.
type TopSection struct {
	Title   string
	Entries []TopRow
}

// TopRow is one ranked host.
type TopRow struct {
	Rank  int
	Host  string
	Value string
	Extra string
}

// MissingLabelHost lists a host and the labels it lacks.
type MissingLabelHost struct {
	Host    string
	Missing string
}

// AssetSection is the change against the previous run.
type AssetSection struct {
	HasPrevious bool
	Summary     string
	Lines       []string
}

// UnclassifiedJob is a job no service rule recognises.
type UnclassifiedJob struct {
	Job     string
	Targets int
	Hosts   string
}

// DownHost is a host with at least one down target.
type DownHost struct {
	Host      string
	Cause     string
	Jobs      string
	LastError string
}

// FailedItem is a check item whose query failed.
type FailedItem struct {
	ID     string
	Name   string
	PromQL string
	Error  string
}

// ItemInfo is one executed check item.
type ItemInfo struct {
	ID       string
	Name     string
	Source   string
	Result   string
	Findings int
}
