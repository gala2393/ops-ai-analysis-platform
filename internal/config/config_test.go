package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodApp = `
prometheus:
  url: "http://127.0.0.1:18090"
llm:
  model: "${YW_TEST_MODEL}"
  api_key: "${YW_TEST_KEY}"
`

const goodServices = `
services:
  - id: node
    name: Linux
    match: { jobs: [node] }
  - id: mysql
    name: MySQL
    match: { jobs: [mysql] }
`

const goodInspection = `
items:
  - id: disk
    name: 磁盘
    applies_to: [node]
    query: 'up'
    compare: ">"
    warn: 80
    crit: 90
`

func writeBundle(t *testing.T, app, insp, svc string) string {
	t.Helper()
	dir := t.TempDir()
	appPath := writeFile(t, dir, "app.yaml", app)
	writeFile(t, dir, "inspection.yaml", insp)
	writeFile(t, dir, "services.yaml", svc)
	return appPath
}

func TestExpandEnv(t *testing.T) {
	env := map[string]string{"A": "alpha", "EMPTY": ""}
	lookup := func(n string) (string, bool) { v, ok := env[n]; return v, ok }
	tests := []struct {
		in, want string
		missing  []string
	}{
		{"plain", "plain", nil},
		{"${A}", "alpha", nil},
		{"x-${A}-y", "x-alpha-y", nil},
		{"${NOPE}", "", []string{"NOPE"}},
		{"${NOPE:-fallback}", "fallback", nil},
		{"${EMPTY:-d}", "d", nil},
		{"${A:-d}", "alpha", nil},
		{"$A and $ {A}", "$A and $ {A}", nil},
		{"${A}${A}", "alphaalpha", nil},
	}
	for _, tt := range tests {
		got, miss := ExpandEnv(tt.in, lookup)
		if got != tt.want || strings.Join(miss, ",") != strings.Join(tt.missing, ",") {
			t.Errorf("ExpandEnv(%q) = %q, %v; want %q, %v", tt.in, got, miss, tt.want, tt.missing)
		}
	}
}

func TestParseDotEnv(t *testing.T) {
	content := "\ufeff# 注释\n\nFOO=bar\nexport BAZ=\"quoted value\"\nQ='single'\nTRAIL=val # comment\nEMPTY=\n"
	got, err := ParseDotEnv(content)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"FOO": "bar", "BAZ": "quoted value", "Q": "single", "TRAIL": "val", "EMPTY": ""}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, err := ParseDotEnv("not a pair"); err == nil {
		t.Error("expected error for malformed line")
	}
}

func TestLoadDotEnvRealEnvWins(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, ".env", "# 注释\nYW_DOT_FROM_FILE=file\nYW_DOT_OVERRIDDEN=file\n")
	t.Setenv("YW_DOT_OVERRIDDEN", "real")
	os.Unsetenv("YW_DOT_FROM_FILE")
	t.Cleanup(func() { os.Unsetenv("YW_DOT_FROM_FILE") })

	applied, err := LoadDotEnv(p)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("YW_DOT_FROM_FILE") != "file" {
		t.Error("value from .env was not applied")
	}
	if os.Getenv("YW_DOT_OVERRIDDEN") != "real" {
		t.Error("real environment variable must win over .env")
	}
	if len(applied) != 1 || applied[0] != "YW_DOT_FROM_FILE" {
		t.Errorf("applied = %v", applied)
	}
	if _, err := LoadDotEnv(filepath.Join(dir, "missing.env")); err != nil {
		t.Errorf("missing .env must not be an error: %v", err)
	}
}

func TestLoadAppExpandsEnvAndRedacts(t *testing.T) {
	t.Setenv("YW_TEST_MODEL", "model-x")
	t.Setenv("YW_TEST_KEY", "sk-super-secret")
	appPath := writeFile(t, t.TempDir(), "app.yaml", goodApp)

	app, err := LoadApp(appPath)
	if err != nil {
		t.Fatal(err)
	}
	if app.LLM.Model != "model-x" || app.LLM.APIKey != "sk-super-secret" {
		t.Fatalf("env not expanded: %+v", app.LLM)
	}
	if app.Prometheus.Timeout != 30*time.Second || app.Prometheus.MaxConcurrency != 4 {
		t.Errorf("defaults not applied: %+v", app.Prometheus)
	}
	if app.Server.BaseURL != "http://127.0.0.1:18080" {
		t.Errorf("base_url default = %q", app.Server.BaseURL)
	}
	red := app.Redacted()
	if red.LLM.APIKey == "sk-super-secret" || red.LLM.Model != "model-x" {
		t.Errorf("redaction wrong: %+v", red.LLM)
	}
	if app.LLM.APIKey != "sk-super-secret" {
		t.Error("Redacted must not mutate the original")
	}
}

func TestLoadAppUnsetEnvWarns(t *testing.T) {
	os.Unsetenv("YW_TEST_MODEL")
	os.Unsetenv("YW_TEST_KEY")
	app, err := LoadApp(writeFile(t, t.TempDir(), "app.yaml", goodApp))
	if err != nil {
		t.Fatal(err)
	}
	if len(app.Warnings) != 2 || app.LLM.Configured() {
		t.Errorf("warnings=%v configured=%v", app.Warnings, app.LLM.Configured())
	}
}

func TestLoadAppRedactsWebhooks(t *testing.T) {
	app, err := LoadApp(writeFile(t, t.TempDir(), "app.yaml", `
prometheus: { url: "http://p:9090" }
notify:
  feishu: { enabled: true, webhook: "https://open.feishu.cn/hook/SECRET" }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := app.Redacted().Notify.Feishu.Webhook; got != redactedMark {
		t.Errorf("webhook not redacted: %q", got)
	}
}

func TestLoadAppInvalid(t *testing.T) {
	tests := []struct {
		name, yaml string
		wantParts  []string
	}{
		{"missing url", "server: {listen: '127.0.0.1:1'}\n", []string{"app.yaml", "prometheus.url"}},
		{"bad url scheme", "prometheus: {url: 'ftp://x'}\n", []string{"prometheus.url"}},
		{"bad yaml", "prometheus: [unclosed\n", []string{"app.yaml", "YAML"}},
		{"unknown field", "prometheus: {url: 'http://p', bogus: 1}\n", []string{"bogus"}},
		{"bad cron", "prometheus: {url: 'http://p'}\nschedule: ['not a cron']\n", []string{"schedule[0]"}},
		{"bad duration", "prometheus: {url: 'http://p', timeout: forever}\n", []string{"app.yaml"}},
		{"zero concurrency", "prometheus: {url: 'http://p', max_concurrency: 0}\n", []string{"prometheus.max_concurrency"}},
		{"enabled webhook empty", "prometheus: {url: 'http://p'}\nnotify: {wecom: {enabled: true}}\n", []string{"notify.wecom.webhook"}},
		{"empty file", "", []string{"app.yaml"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadApp(writeFile(t, t.TempDir(), "app.yaml", tt.yaml))
			if err == nil {
				t.Fatal("expected error")
			}
			for _, part := range tt.wantParts {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q does not mention %q", err, part)
				}
			}
		})
	}
}

func TestLoadAppMissingFile(t *testing.T) {
	_, err := LoadApp(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil || !strings.Contains(err.Error(), "nope.yaml") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadBundleInvalid(t *testing.T) {
	t.Setenv("YW_TEST_MODEL", "m")
	t.Setenv("YW_TEST_KEY", "k")
	tests := []struct {
		name            string
		app, insp, svcs string
		want            []string
	}{
		{"malformed inspection yaml", goodApp, "items: [\n  - id: x", goodServices, []string{"inspection.yaml", "YAML"}},
		{"missing query", goodApp, strings.Replace(goodInspection, "    query: 'up'\n", "", 1), goodServices, []string{"inspection.yaml", "items[0].query"}},
		{"bad compare", goodApp, strings.Replace(goodInspection, `">"`, `"=>"`, 1), goodServices, []string{"items[0].compare", `"=>"`}},
		{"unknown service", goodApp, strings.Replace(goodInspection, "[node]", "[ghost]", 1), goodServices, []string{"inspection.yaml", "items[0].applies_to[0]", "ghost"}},
		{"bad cron", goodApp + "schedule: ['61 * * * *']\n", goodInspection, goodServices, []string{"app.yaml", "schedule[0]"}},
		{"duplicate id", goodApp, goodInspection + strings.TrimPrefix(goodInspection, "\nitems:\n"), goodServices, []string{"items[1].id", "duplicate"}},
		{"crit weaker than warn", goodApp, strings.Replace(goodInspection, "crit: 90", "crit: 70", 1), goodServices, []string{"items[0].crit"}},
		{"no thresholds", goodApp, strings.Replace(strings.Replace(goodInspection, "    warn: 80\n", "", 1), "    crit: 90\n", "", 1), goodServices, []string{"items[0].warn"}},
		{"bad severity", goodApp, goodInspection + "    severity: urgent\n", goodServices, []string{"items[0].severity"}},
		{"override unknown item", goodApp, goodInspection + "overrides:\n  - match: {role: db}\n    items: {nope: {warn: 1}}\n", goodServices, []string{"overrides[0].items.nope"}},
		{"override unknown field", goodApp, goodInspection + "overrides:\n  - match: {region: x}\n    items: {disk: {warn: 1}}\n", goodServices, []string{"overrides[0].match.region"}},
		{"duplicate service", goodApp, goodInspection, goodServices + "  - id: node\n    name: dup\n    match: { jobs: [x] }\n", []string{"services.yaml", "services[2].id"}},
		{"systemd unknown service", goodApp, goodInspection, goodServices + "systemd_units:\n  metric: m\n  map: {\"a.service\": ghost}\n", []string{"systemd_units.map.a.service"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadBundle(writeBundle(t, tt.app, tt.insp, tt.svcs))
			if err == nil {
				t.Fatal("expected error")
			}
			for _, part := range tt.want {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q does not mention %q", err, part)
				}
			}
		})
	}
}

func TestLoadBundleOK(t *testing.T) {
	t.Setenv("YW_TEST_MODEL", "m")
	t.Setenv("YW_TEST_KEY", "k")
	b, err := LoadBundle(writeBundle(t, goodApp, goodInspection, goodServices))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Inspection.Items) != 1 || b.Inspection.Items[0].Severity != "medium" {
		t.Errorf("items = %+v", b.Inspection.Items)
	}
	if got := len(b.Inspection.EnabledItems()); got != 1 {
		t.Errorf("enabled = %d", got)
	}
}

func TestLoadScenario(t *testing.T) {
	dir := t.TempDir()
	sc, err := LoadScenario(writeFile(t, dir, "s.yaml", "hosts: 12\nseed: 7\nextra_section: {a: 1}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if sc.Hosts != 12 || sc.Seed != 7 || sc.History != 6*time.Hour || sc.Step != time.Minute {
		t.Errorf("scenario = %+v", sc)
	}
	for name, y := range map[string]string{
		"zero hosts":       "hosts: 0\n",
		"bad step":         "hosts: 1\nhistory: 1m\nstep: 2m\n",
		"bad frac":         "hosts: 1\nmissing_label_fraction: 2\n",
		"negative anomaly": "hosts: 5\nanomalies: {cpu_high: -1}\n",
		"too many planted": "hosts: 5\nanomalies: {cpu_high: 3, mem_high: 3}\n",
		"down needs mysql": "hosts: 5\nanomalies: {host_down: 1}\n",
	} {
		if _, err := LoadScenario(writeFile(t, dir, "bad.yaml", y)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// The shipped default config must always load and pass validation.
func TestShippedDefaultConfig(t *testing.T) {
	for _, v := range []string{"YUNWEI_LLM_BASE_URL", "YUNWEI_LLM_MODEL", "YUNWEI_LLM_API_KEY"} {
		t.Setenv(v, "http://127.0.0.1:1")
	}
	b, err := LoadBundle(filepath.Join("..", "..", "config", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if b.App.Prometheus.URL != "http://127.0.0.1:18090" {
		t.Errorf("prometheus.url = %q", b.App.Prometheus.URL)
	}
	items := b.Inspection.Items
	if len(items) < 10 {
		t.Fatalf("want >= 10 default items, got %d", len(items))
	}
	for _, it := range items {
		if it.Source != DefaultSource {
			t.Errorf("item %s source = %q", it.ID, it.Source)
		}
		for _, w := range []string{"[2d", "[48h", "[3d", "[7d"} {
			if strings.Contains(it.Query, w) {
				t.Errorf("item %s uses a window over 24h: %s", it.ID, w)
			}
		}
	}
	if _, err := LoadScenario(filepath.Join("..", "..", "config", "mock-scenario.yaml")); err != nil {
		t.Error(err)
	}
}
