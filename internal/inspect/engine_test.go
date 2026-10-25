package inspect

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"yunwei/internal/config"
	"yunwei/internal/discovery"
	"yunwei/internal/mock"
	"yunwei/internal/prom"
)

type fakeSource struct {
	mu      sync.Mutex
	answers map[string]model.Vector
	errs    map[string]error
	queries []string
}

func (f *fakeSource) Query(_ context.Context, q string, _ time.Time) (model.Value, promv1.Warnings, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	if err := f.errs[q]; err != nil {
		return nil, nil, err
	}
	return f.answers[q], nil, nil
}

func sample(inst string, v float64, extra ...string) *model.Sample {
	m := model.Metric{"instance": model.LabelValue(inst)}
	for i := 0; i+1 < len(extra); i += 2 {
		m[model.LabelName(extra[i])] = model.LabelValue(extra[i+1])
	}
	return &model.Sample{Metric: m, Value: model.SampleValue(v)}
}

func host(key, name, status string, labels map[string]string, services ...string) *discovery.Host {
	h := &discovery.Host{Key: key, Name: name, Labels: labels, Status: status}
	for _, s := range services {
		h.Services = append(h.Services, discovery.Service{ID: s})
	}
	return h
}

func item(id, q, op string, warn, crit *float64, sev string, applies ...string) config.Item {
	return config.Item{ID: id, Name: id, Query: q, Compare: op, Warn: warn, Crit: crit, Severity: sev, AppliesTo: applies, Unit: "%"}
}

func TestEngineFindingsGroupsHealth(t *testing.T) {
	lab := func(role, dc string) map[string]string {
		return map[string]string{"env": "prod", "role": role, "biz": "pay", "dc": dc}
	}
	disc := &discovery.Result{Hosts: []*discovery.Host{
		host("10.0.0.1", "app-1", discovery.StatusNormal, lab("app", "sh1"), "node"),
		host("10.0.0.2", "app-2", discovery.StatusNormal, lab("app", "sh1"), "node"),
		host("10.0.0.3", "db-1", discovery.StatusNormal, lab("db", "bj1"), "node", "mysql"),
		host("10.0.0.4", "ok-1", discovery.StatusNormal, lab("web", "bj1"), "node"),
		host("10.0.0.5", "dead-1", discovery.StatusHostDown, lab("web", "bj1"), "node"),
		host("10.0.0.6", "nomysql", discovery.StatusNormal, lab("web", "bj1"), "node"),
	}}
	diskItem := item("disk", "q_disk", ">", f64(80), f64(90), "high", "node")
	diskItem.GroupByExtra = []string{"mountpoint"}
	in := &config.Inspection{
		Items: []config.Item{
			diskItem,
			item("conn", "q_conn", ">", f64(80), f64(90), "medium", "mysql"),
			item("down", "q_down", "==", nil, f64(0), "critical"),
			item("fill", "q_fill", "<", f64(7), f64(3), "high", "node"),
		},
		Overrides: []config.Override{{Match: map[string]string{"role": "db"}, Items: map[string]config.ThresholdOverride{"disk": {Warn: f64(75), Crit: f64(85)}}}},
	}
	src := &fakeSource{answers: map[string]model.Vector{
		"q_disk": {
			sample("10.0.0.1:9100", 95, "mountpoint", "/"), sample("10.0.0.1:9100", 97, "mountpoint", "/data"),
			sample("10.0.0.2:9100", 92, "mountpoint", "/"),
			sample("10.0.0.3:9100", 77, "mountpoint", "/"), // warn only for db via override
			sample("10.0.0.4:9100", 79, "mountpoint", "/"), // below default warn
			sample("10.9.9.9:9100", 99),                    // unknown host
		},
		"q_conn": {sample("10.0.0.3:9104", 91), sample("10.0.0.6:9104", 99)},
		"q_down": {sample("10.0.0.5:9100", 0), sample("10.0.0.5:9104", 0), sample("10.0.0.1:9100", 1)},
		"q_fill": {sample("10.0.0.4:9100", 2, "mountpoint", "/")},
	}}

	res, err := Run(context.Background(), src, in, disc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Queries != 4 || len(src.queries) != 4 {
		t.Fatalf("queries = %d/%d, want 4", res.Queries, len(src.queries))
	}
	if res.Unmapped != 1 {
		t.Errorf("unmapped = %d, want 1", res.Unmapped)
	}

	got := map[string]*Finding{}
	for _, f := range res.Findings {
		got[f.CheckID+"/"+f.Host] = f
	}
	want := map[string]string{
		"disk/app-1": LevelCrit, "disk/app-2": LevelCrit, "disk/db-1": LevelWarn,
		"conn/db-1": LevelCrit, "down/dead-1": LevelCrit, "fill/ok-1": LevelCrit,
	}
	if len(got) != len(want) {
		t.Errorf("findings = %d, want %d: %v", len(got), len(want), keys(got))
	}
	for k, lv := range want {
		if f := got[k]; f == nil || f.Level != lv {
			t.Errorf("finding %s = %+v, want level %s", k, f, lv)
		}
	}
	if f := got["conn/nomysql"]; f != nil {
		t.Error("mysql item produced a finding for a host without mysql")
	}

	d := got["disk/app-1"]
	if d.Value != 97 || d.Extra["mountpoint"] != "/data" || d.Breaching != 2 {
		t.Errorf("worst series not kept: %+v", d)
	}
	if e := d.Evidence; e.QueryID != "Q1" || e.PromQL != "q_disk" || e.Value != 97 || e.Labels["mountpoint"] != "/data" ||
		e.Threshold.Crossed != 90 || *e.Threshold.Warn != 80 || *e.Threshold.Crit != 90 || e.Threshold.Compare != ">" {
		t.Errorf("evidence = %+v", e)
	}
	if e := got["disk/db-1"].Evidence.Threshold; *e.Warn != 75 || *e.Crit != 85 || !e.Overridden {
		t.Errorf("override threshold not shown: %+v", e)
	}
	if got["disk/db-1"].Severity != "medium" {
		t.Errorf("warn severity = %s, want lowered to medium", got["disk/db-1"].Severity)
	}

	// Groups: one per (check, level), ordered by severity then count.
	wantGroups := []struct {
		check, level string
		count        int
	}{{"down", LevelCrit, 1}, {"disk", LevelCrit, 2}, {"fill", LevelCrit, 1}, {"conn", LevelCrit, 1}, {"disk", LevelWarn, 1}}
	if len(res.Groups) != len(wantGroups) {
		t.Fatalf("groups = %d, want %d", len(res.Groups), len(wantGroups))
	}
	for i, w := range wantGroups {
		g := res.Groups[i]
		if g.CheckID != w.check || g.Level != w.level || g.Count != w.count {
			t.Errorf("group[%d] = %s/%s/%d, want %s/%s/%d", i, g.CheckID, g.Level, g.Count, w.check, w.level, w.count)
		}
	}
	dg := res.Groups[1]
	if dg.Title != "disk超过阈值（2 台）" {
		t.Errorf("title = %q", dg.Title)
	}
	if fmt.Sprint(dg.Common) != "[{env prod} {role app} {biz pay} {dc sh1}]" {
		t.Errorf("common = %v", dg.Common)
	}
	if res.Groups[2].Title != "fill低于阈值（1 台）" {
		t.Errorf("title = %q", res.Groups[2].Title)
	}

	hh := map[string]*HostHealth{}
	for _, h := range res.Hosts {
		hh[h.Name] = h
	}
	checks := []struct {
		name   string
		score  int
		status string
	}{{"nomysql", 100, StatusGreen}, {"dead-1", 0, StatusRed}, {"app-1", 75, StatusRed}, {"db-1", 85, StatusRed}, {"ok-1", 75, StatusRed}}
	for _, c := range checks {
		if h := hh[c.name]; h.Score != c.score || h.Status != c.status {
			t.Errorf("%s = %d/%s, want %d/%s", c.name, h.Score, h.Status, c.score, c.status)
		}
	}
	if res.Counts.Total != 6 || res.Counts.OK != 1 || res.Counts.Crit != 5 {
		t.Errorf("counts = %+v", res.Counts)
	}
	if len(res.ByBiz) != 1 || res.ByBiz[0].Name != "pay" || res.ByBiz[0].Hosts != 6 {
		t.Errorf("by biz = %+v", res.ByBiz)
	}
	if len(res.ByEnv) != 1 || res.ByEnv[0].Status != StatusRed {
		t.Errorf("by env = %+v", res.ByEnv)
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestWarnOnlyHostIsYellow(t *testing.T) {
	disc := &discovery.Result{Hosts: []*discovery.Host{host("h1", "h1", discovery.StatusNormal, map[string]string{"biz": "a"}, "node")}}
	in := &config.Inspection{Items: []config.Item{item("m", "q", ">", f64(80), f64(90), "high", "node")}}
	src := &fakeSource{answers: map[string]model.Vector{"q": {sample("h1:9100", 85)}}}
	res, err := Run(context.Background(), src, in, disc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if h := res.Hosts[0]; h.Status != StatusYellow || h.Score != 95 {
		t.Errorf("host = %+v", h)
	}
	if res.Counts.Warn != 1 || res.ByEnv[0].Name != unlabeled {
		t.Errorf("counts/env = %+v / %+v", res.Counts, res.ByEnv)
	}
}

func TestPartialFailureContinues(t *testing.T) {
	disc := &discovery.Result{Hosts: []*discovery.Host{host("h1", "h1", discovery.StatusNormal, nil, "node")}}
	in := &config.Inspection{Items: []config.Item{
		item("bad", "q_bad", ">", f64(1), nil, "low", "node"),
		item("good", "q_good", ">", f64(1), nil, "low", "node"),
		item("wide", "increase(x[48h])", ">", f64(1), nil, "low", "node"),
		{ID: "off", Name: "off", Query: "q_off", Compare: ">", Warn: f64(1), Enabled: new(bool)},
	}}
	src := &fakeSource{
		answers: map[string]model.Vector{"q_good": {sample("h1:9100", 5)}},
		errs:    map[string]error{"q_bad": errors.New("boom")},
	}
	var progress []int
	res, err := Run(context.Background(), src, in, disc, Options{Concurrency: 1, OnProgress: func(d, _ int) { progress = append(progress, d) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("items = %d, want 3 (disabled skipped)", len(res.Items))
	}
	errs := res.Errors()
	if len(errs) != 2 || errs[0].ID != "bad" || errs[1].ID != "wide" {
		t.Fatalf("errors = %+v", errs)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != "good" {
		t.Errorf("findings = %+v", res.Findings)
	}
	if res.Queries != 2 {
		t.Errorf("queries = %d, want 2 (window-rejected item is never sent)", res.Queries)
	}
	if len(progress) != 3 || progress[2] != 3 {
		t.Errorf("progress = %v", progress)
	}
}

func TestTopNLists(t *testing.T) {
	disc := &discovery.Result{}
	var disk, fill model.Vector
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("10.0.0.%d", i)
		disc.Hosts = append(disc.Hosts, host(key, fmt.Sprintf("n%d", i), discovery.StatusNormal, nil, "node"))
		disk = append(disk, sample(key+":9100", float64(50+i*8), "mountpoint", "/"))
		fill = append(fill, sample(key+":9100", float64(20-i*3), "mountpoint", "/"))
	}
	in := &config.Inspection{Items: []config.Item{
		{ID: "disk_usage", Name: "d", Query: "q_d", Compare: ">", Warn: f64(80), Crit: f64(90), Unit: "%"},
		{ID: "disk_days_to_full", Name: "f", Query: "q_f", Compare: "<", Warn: f64(7), Crit: f64(3), Unit: "天"},
	}}
	src := &fakeSource{answers: map[string]model.Vector{"q_d": disk, "q_f": fill}}
	res, err := Run(context.Background(), src, in, disc, Options{TopN: 3, History: map[string]int{"10.0.0.5": 1, "10.0.0.4": 3, "10.0.0.1": 5}})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]TopList{}
	for _, l := range res.Top {
		byID[l.ID] = l
	}
	if len(res.Top) != 4 {
		t.Fatalf("top lists = %d, want 4", len(res.Top))
	}
	d := byID[TopDiskUsage].Entries
	if len(d) != 3 || d[0].Host != "n5" || d[0].Value != 90 || d[2].Host != "n3" || d[0].Extra["mountpoint"] != "/" {
		t.Errorf("disk top = %+v", d)
	}
	f := byID[TopDiskFill].Entries
	if len(f) != 3 || f[0].Host != "n5" || f[0].Value != 5 || f[1].Host != "n4" {
		t.Errorf("fill top = %+v", f)
	}
	r := byID[TopRepeat].Entries
	// n5 (disk 90 crit? 90 is not > 90) is warn; hosts n4 (82 warn) and n5 have findings; n1 has none.
	if len(r) == 0 || r[0].Host != "n4" || r[0].Value != 4 {
		t.Errorf("repeat top = %+v", r)
	}
	for _, e := range r {
		if e.Host == "n1" {
			t.Error("host without findings in this run listed as repeating")
		}
	}
	if len(byID[TopReboot].Entries) != 0 {
		t.Errorf("reboot top should be empty without the item: %+v", byID[TopReboot])
	}
}

func TestConcurrencyBound(t *testing.T) {
	disc := &discovery.Result{Hosts: []*discovery.Host{host("h1", "h1", discovery.StatusNormal, nil)}}
	in := &config.Inspection{}
	for i := 0; i < 12; i++ {
		in.Items = append(in.Items, item(fmt.Sprintf("i%d", i), fmt.Sprintf("q%d", i), ">", f64(1), nil, "low"))
	}
	src := &slowSource{}
	if _, err := Run(context.Background(), src, in, disc, Options{Concurrency: 3}); err != nil {
		t.Fatal(err)
	}
	if src.max > 3 || src.max < 2 {
		t.Errorf("max in flight = %d, want within 2..3", src.max)
	}
}

type slowSource struct {
	mu       sync.Mutex
	cur, max int
}

func (s *slowSource) Query(context.Context, string, time.Time) (model.Value, promv1.Warnings, error) {
	s.mu.Lock()
	s.cur++
	if s.cur > s.max {
		s.max = s.cur
	}
	s.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	s.cur--
	s.mu.Unlock()
	return model.Vector{}, nil, nil
}

// runAgainstMock runs discovery plus inspection on a fresh in-process mock and
// returns the result with the number of queries the mock served.
func runAgainstMock(t *testing.T, mutate func(*config.Scenario)) (*Result, *mock.Manifest, int, *config.Inspection) {
	t.Helper()
	sc, err := config.LoadScenario("../../config/mock-scenario.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(sc)
	}
	srv, err := mock.New(sc, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	client, err := prom.New(prom.Config{URL: ts.URL, Timeout: 30 * time.Second, MaxConcurrency: 4, MaxRange: 24 * time.Hour, MinStep: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := config.LoadServices("../../config/services.yaml")
	if err != nil {
		t.Fatal(err)
	}
	app, err := config.LoadApp("../../config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	in, err := config.LoadInspection("../../config/inspection.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disc, err := discovery.Discover(context.Background(), client, svc, app.Labels, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	scoped := client.Scoped()
	res, err := Run(context.Background(), scoped, in, disc, Options{MaxRange: app.Prometheus.MaxRange, MinStep: app.Prometheus.MinStep, TopN: app.Report.TopN})
	if err != nil {
		t.Fatal(err)
	}
	if got := scoped.Stats().Count; got != res.Queries {
		t.Errorf("client saw %d queries, result says %d", got, res.Queries)
	}
	return res, srv.Fleet().Manifest(), disc.Queries + res.Queries, in
}

func TestEngineAgainstMockManifest(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the 180 host mock")
	}
	res, m, _, in := runAgainstMock(t, nil)
	for _, it := range res.Errors() {
		t.Errorf("item %s failed: %s", it.ID, it.Error)
	}

	type key struct{ check, host string }
	got := map[key]string{}
	for _, f := range res.Findings {
		got[key{f.CheckID, f.Host}] = f.Level
		if f.Evidence.PromQL == "" || f.Evidence.QueryID == "" {
			t.Errorf("finding without evidence: %+v", f)
		}
	}
	want := map[key]string{}
	for _, a := range m.Anomalies {
		want[key{a.CheckID, a.Host}] = a.Level
	}
	for _, d := range m.Down {
		want[key{"target_down", d.Host}] = LevelCrit
	}
	for k, lv := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("missed %v (%s)", k, lv)
		} else if g != lv {
			t.Errorf("%v level = %s, want %s", k, g, lv)
		}
	}
	for k, lv := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("false positive %v (%s)", k, lv)
		}
	}

	pairs := map[[2]string]bool{}
	for _, f := range res.Findings {
		pairs[[2]string{f.CheckID, f.Level}] = true
	}
	if len(res.Groups) != len(pairs) {
		t.Errorf("groups = %d, distinct (check, level) = %d", len(res.Groups), len(pairs))
	}
	var disk *ProblemGroup
	for _, g := range res.Groups {
		if g.CheckID == "disk_usage" && g.Level == LevelCrit {
			disk = g
		}
	}
	if disk == nil || disk.Count != 15 || disk.Title != "磁盘使用率超过阈值（15 台）" {
		t.Fatalf("disk group = %+v", disk)
	}
	feat := map[string]string{}
	for _, f := range disk.Common {
		feat[f.Field] = f.Value
	}
	if feat["role"] != "app" || feat["dc"] != "sh1" {
		t.Errorf("disk common features = %v", disk.Common)
	}
	for i := 1; i < len(res.Groups); i++ {
		a, b := res.Groups[i-1], res.Groups[i]
		if ra, rb := SeverityRank(a.Severity), SeverityRank(b.Severity); ra < rb || (ra == rb && a.Count < b.Count) {
			t.Errorf("groups out of order: %s/%s(%d) before %s/%s(%d)", a.CheckID, a.Level, a.Count, b.CheckID, b.Level, b.Count)
		}
	}
	for _, f := range res.Findings {
		if f.CheckID == "mysql_connections" || f.CheckID == "mysql_up" {
			if !contains(m.Services[f.Host], "mysql") {
				t.Errorf("%s finding on host without mysql: %s", f.CheckID, f.Host)
			}
		}
	}

	for _, h := range res.Hosts {
		for _, d := range m.Down {
			if d.Host == h.Name && d.Cause == "host_down" && (h.Score != 0 || h.Status != StatusRed) {
				t.Errorf("down host = %+v", h)
			}
		}
	}
	if res.Counts.Total != m.Hosts || res.Counts.OK == 0 {
		t.Errorf("counts = %+v", res.Counts)
	}
	if len(res.ByBiz) == 0 || len(res.ByEnv) == 0 {
		t.Error("missing biz/env aggregates")
	}
	if res.Queries > len(in.EnabledItems()) {
		t.Errorf("queries = %d for %d items", res.Queries, len(in.EnabledItems()))
	}

	fill := map[string]bool{}
	for _, a := range m.Anomalies {
		if a.CheckID == "disk_days_to_full" {
			fill[a.Host] = true
		}
	}
	for _, l := range res.Top {
		switch l.ID {
		case TopDiskFill:
			for i := 0; i < 3 && i < len(l.Entries); i++ {
				if !fill[l.Entries[i].Host] {
					t.Errorf("growth top[%d] = %s, not a planted fast-fill host", i, l.Entries[i].Host)
				}
			}
		case TopDiskUsage:
			if len(l.Entries) != 10 || l.Entries[0].Value < l.Entries[9].Value {
				t.Errorf("disk top = %+v", l.Entries)
			}
		}
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func TestQueryCountIndependentOfHosts(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two mocks")
	}
	small, _, _, _ := runAgainstMock(t, func(sc *config.Scenario) { sc.Hosts = 100 })
	large, _, _, in := runAgainstMock(t, nil)
	if small.Queries != large.Queries || large.Queries != len(in.EnabledItems()) {
		t.Errorf("queries: 100 hosts=%d, 180 hosts=%d, items=%d", small.Queries, large.Queries, len(in.EnabledItems()))
	}
}
