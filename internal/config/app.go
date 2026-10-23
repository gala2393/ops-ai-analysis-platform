package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// redactedMark replaces secret values in any printed configuration.
const redactedMark = "***"

type App struct {
	Server     ServerConfig  `yaml:"server"`
	Prometheus PromConfig    `yaml:"prometheus"`
	Labels     LabelsConfig  `yaml:"labels"`
	Storage    StorageConfig `yaml:"storage"`
	Schedule   []string      `yaml:"schedule"`
	Report     ReportConfig  `yaml:"report"`
	LLM        LLMConfig     `yaml:"llm"`
	Alert      AlertConfig   `yaml:"alert"`
	Notify     NotifyConfig  `yaml:"notify"`

	// Path is the file this config was loaded from.
	Path string `yaml:"-"`
	// Warnings lists non-fatal findings, e.g. ${VAR} references that were unset.
	Warnings []string `yaml:"-"`
}

type ServerConfig struct {
	Listen  string `yaml:"listen"`
	BaseURL string `yaml:"base_url"`
}

type PromConfig struct {
	URL            string        `yaml:"url"`
	Timeout        time.Duration `yaml:"timeout"`
	MaxConcurrency int           `yaml:"max_concurrency"`
	MaxRange       time.Duration `yaml:"max_range"`
	MinStep        time.Duration `yaml:"min_step"`
}

// LabelsConfig maps internal host fields to the enterprise's Prometheus label names.
type LabelsConfig struct {
	Env      string `yaml:"env"`
	Role     string `yaml:"role"`
	Biz      string `yaml:"biz"`
	DC       string `yaml:"dc"`
	Owner    string `yaml:"owner"`
	Hostname string `yaml:"hostname"`
}

// Fields returns the internal field names usable in override match blocks.
func (l LabelsConfig) Fields() map[string]string {
	return map[string]string{"env": l.Env, "role": l.Role, "biz": l.Biz, "dc": l.DC, "owner": l.Owner}
}

type StorageConfig struct {
	Path          string `yaml:"path"`
	RetentionDays int    `yaml:"retention_days"`
}

type ReportConfig struct {
	OutDir string `yaml:"out_dir"`
	TopN   int    `yaml:"top_n"`
}

type LLMConfig struct {
	Enabled         bool          `yaml:"enabled"`
	BaseURL         string        `yaml:"base_url"`
	Model           string        `yaml:"model"`
	APIKey          string        `yaml:"api_key"`
	Timeout         time.Duration `yaml:"timeout"`
	MaxGroupsPerRun int           `yaml:"max_groups_per_run"`
	DailyTokenLimit int           `yaml:"daily_token_limit"`
	Mask            bool          `yaml:"mask"`
}

// Configured reports whether the LLM section has everything needed to make a call.
func (l LLMConfig) Configured() bool {
	return l.Enabled && l.BaseURL != "" && l.Model != "" && l.APIKey != ""
}

type AlertConfig struct {
	AggregationWindow time.Duration `yaml:"aggregation_window"`
	ReanalyzeInterval time.Duration `yaml:"reanalyze_interval"`
}

type NotifyConfig struct {
	Feishu       Webhook `yaml:"feishu"`
	DingTalk     Webhook `yaml:"dingtalk"`
	WeCom        Webhook `yaml:"wecom"`
	OnInspection bool    `yaml:"on_inspection"`
}

type Webhook struct {
	Enabled bool   `yaml:"enabled"`
	Webhook string `yaml:"webhook"`
}

func defaultApp() *App {
	return &App{
		Server: ServerConfig{Listen: "127.0.0.1:18080"},
		Prometheus: PromConfig{
			Timeout:        30 * time.Second,
			MaxConcurrency: 4,
			MaxRange:       24 * time.Hour,
			MinStep:        60 * time.Second,
		},
		Labels:   LabelsConfig{Env: "env", Role: "role", Biz: "biz", DC: "dc", Owner: "owner", Hostname: "nodename"},
		Storage:  StorageConfig{Path: "data/yunwei.db", RetentionDays: 90},
		Schedule: []string{"30 8 * * *"},
		Report:   ReportConfig{OutDir: "reports", TopN: 10},
		LLM: LLMConfig{
			Enabled: true, Timeout: 90 * time.Second, MaxGroupsPerRun: 30,
			DailyTokenLimit: 2000000, Mask: true,
		},
		Alert: AlertConfig{AggregationWindow: 30 * time.Second, ReanalyzeInterval: 10 * time.Minute},
	}
}

// LoadApp loads and validates app.yaml. Omitted fields take their defaults.
func LoadApp(path string) (*App, error) {
	app := defaultApp()
	missing, err := readYAML(path, app, true)
	if err != nil {
		return nil, err
	}
	app.Path = path
	for _, m := range missing {
		app.Warnings = append(app.Warnings, fmt.Sprintf("%s: 环境变量 %s 未设置 (environment variable not set)", path, m))
	}
	if app.Server.BaseURL == "" {
		app.Server.BaseURL = "http://" + app.Server.Listen
	}
	if err := app.validate(path); err != nil {
		return nil, err
	}
	return app, nil
}

func (a *App) validate(file string) error {
	p := &problems{file: file}

	if strings.TrimSpace(a.Server.Listen) == "" {
		p.add("server.listen", "不能为空 (must not be empty)")
	}
	checkHTTPURL(p, "server.base_url", a.Server.BaseURL, true)

	checkHTTPURL(p, "prometheus.url", a.Prometheus.URL, false)
	if a.Prometheus.Timeout <= 0 {
		p.add("prometheus.timeout", "必须大于 0 (must be > 0), 例如 30s")
	}
	if a.Prometheus.MaxConcurrency < 1 {
		p.add("prometheus.max_concurrency", "必须 >= 1 (must be >= 1)")
	}
	if a.Prometheus.MaxRange <= 0 {
		p.add("prometheus.max_range", "必须大于 0 (must be > 0), 例如 24h")
	}
	if a.Prometheus.MinStep <= 0 {
		p.add("prometheus.min_step", "必须大于 0 (must be > 0), 例如 60s")
	}

	for field, v := range map[string]string{
		"labels.env": a.Labels.Env, "labels.role": a.Labels.Role, "labels.biz": a.Labels.Biz,
		"labels.dc": a.Labels.DC, "labels.owner": a.Labels.Owner,
	} {
		if strings.TrimSpace(v) == "" {
			p.add(field, "标签名不能为空 (label name must not be empty)")
		}
	}

	if strings.TrimSpace(a.Storage.Path) == "" {
		p.add("storage.path", "不能为空 (must not be empty)")
	}
	if a.Storage.RetentionDays < 1 {
		p.add("storage.retention_days", "必须 >= 1 (must be >= 1)")
	}

	for i, spec := range a.Schedule {
		if _, err := cron.ParseStandard(spec); err != nil {
			p.add(fmt.Sprintf("schedule[%d]", i), "无效的 cron 表达式 %q (invalid cron spec): %v", spec, err)
		}
	}

	if strings.TrimSpace(a.Report.OutDir) == "" {
		p.add("report.out_dir", "不能为空 (must not be empty)")
	}
	if a.Report.TopN < 1 {
		p.add("report.top_n", "必须 >= 1 (must be >= 1)")
	}

	if a.LLM.Timeout <= 0 {
		p.add("llm.timeout", "必须大于 0 (must be > 0)")
	}
	if a.LLM.MaxGroupsPerRun < 1 {
		p.add("llm.max_groups_per_run", "必须 >= 1 (must be >= 1)")
	}
	if a.LLM.DailyTokenLimit < 0 {
		p.add("llm.daily_token_limit", "不能为负数 (must not be negative)")
	}
	if a.LLM.BaseURL != "" {
		checkHTTPURL(p, "llm.base_url", a.LLM.BaseURL, false)
	}

	if a.Alert.AggregationWindow < 0 {
		p.add("alert.aggregation_window", "不能为负数 (must not be negative)")
	}
	if a.Alert.ReanalyzeInterval < 0 {
		p.add("alert.reanalyze_interval", "不能为负数 (must not be negative)")
	}

	for name, w := range map[string]Webhook{"feishu": a.Notify.Feishu, "dingtalk": a.Notify.DingTalk, "wecom": a.Notify.WeCom} {
		if !w.Enabled {
			continue
		}
		if strings.TrimSpace(w.Webhook) == "" {
			p.add("notify."+name+".webhook", "启用后 webhook 不能为空 (webhook required when enabled)")
		} else {
			checkHTTPURL(p, "notify."+name+".webhook", w.Webhook, false)
		}
	}
	return p.err()
}

func checkHTTPURL(p *problems, field, raw string, optional bool) {
	if strings.TrimSpace(raw) == "" {
		if !optional {
			p.add(field, "不能为空 (must not be empty)")
		}
		return
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		p.add(field, "必须是 http(s):// 开头的有效地址 (must be a valid http(s) URL), 值已隐藏 (value hidden)")
	}
}

// Redacted returns a copy that is safe to print or log: the LLM key and every
// webhook URL are masked, and the LLM base URL is kept (it is not a secret).
func (a *App) Redacted() App {
	c := *a
	c.Warnings = nil
	if c.LLM.APIKey != "" {
		c.LLM.APIKey = redactedMark
	}
	for _, w := range []*Webhook{&c.Notify.Feishu, &c.Notify.DingTalk, &c.Notify.WeCom} {
		if w.Webhook != "" {
			w.Webhook = redactedMark
		}
	}
	return c
}
