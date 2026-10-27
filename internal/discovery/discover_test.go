package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"yunwei/internal/config"
	"yunwei/internal/mock"
	"yunwei/internal/prom"
)

type fakeSource struct {
	targets    []promv1.ActiveTarget
	targetsErr error
	// answers maps a substring of the query to its vector.
	answers map[string]model.Vector
	queries []string
}

func (f *fakeSource) Query(_ context.Context, q string, _ time.Time) (model.Value, promv1.Warnings, error) {
	f.queries = append(f.queries, q)
	for sub, vec := range f.answers {
		if strings.Contains(q, sub) {
			return vec, nil, nil
		}
	}
	return model.Vector{}, nil, nil
}

func (f *fakeSource) Targets(context.Context) (promv1.TargetsResult, error) {
	return promv1.TargetsResult{Active: f.targets}, f.targetsErr
}

func tgt(job, app, instance string, up bool, lastErr string, extra map[string]string) promv1.ActiveTarget {
	ls := model.LabelSet{"job": model.LabelValue(job), "instance": model.LabelValue(instance)}
	if app != "" {
		ls["app"] = model.LabelValue(app)
	}
	for k, v := range extra {
		ls[model.LabelName(k)] = model.LabelValue(v)
	}
	h := promv1.HealthGood
	if !up {
		h = promv1.HealthBad
	}
	return promv1.ActiveTarget{Labels: ls, Health: h, LastError: lastErr}
}

func vecOf(samples ...model.Metric) model.Vector {
	v := model.Vector{}
	for _, m := range samples {
		v = append(v, &model.Sample{Metric: m, Value: 1})
	}
	return v
}

func testServices() *config.Services {
	return &config.Services{
		Services: []config.ServiceRule{
			{ID: "node", Match: config.MatchRule{Jobs: []string{"node"}, Apps: []string{"node-exporter"}, MetricPrefixes: []string{"node_"}}},
			{ID: "mysql", Match: config.MatchRule{Jobs: []string{"MySQL"}, MetricPrefixes: []string{"mysql_"}}},
			{ID: "redis", Match: config.MatchRule{Jobs: []string{"redis"}, MetricPrefixes: []string{"redis_"}}},
		},
		SystemdUnits: config.SystemdUnits{Metric: "node_systemd_unit_state", Map: map[string]string{"redis.service": "redis"}},
	}
}

var defaultLabels = config.LabelsConfig{Env: "env", Role: "role", Biz: "biz", DC: "dc", Owner: "owner", Hostname: "nodename"}

func fullLabels() map[string]string {
	return map[string]string{"env": "prod", "role": "db", "biz": "pay", "dc": "bj1", "owner": "ops"}
}

func TestHostMergeNamingAndServices(t *testing.T) {
	src := &fakeSource{
		targets: []promv1.ActiveTarget{
			tgt("node", "node-exporter", "10.0.0.1:9100", true, "", fullLabels()),
			tgt("mysql", "mysql_exporter", "10.0.0.1:9104", true, "", fullLabels()),
			tgt("legacy-billing", "billing-exporter", "10.0.0.2:9777", true, "", fullLabels()),
		},
		answers: map[string]model.Vector{
			"node_uname_info": vecOf(model.Metric{"instance": "10.0.0.1:9100", "nodename": "db-bj1-001"}),
			"node_systemd_unit_state": vecOf(
				model.Metric{"instance": "10.0.0.2:9100", "name": "redis.service"},
				model.Metric{"instance": "10.0.0.1:9100", "name": "redis.service"}),
			"node_.+": vecOf(model.Metric{"instance": "10.0.0.1:9100", "job": "node"}),
		},
	}
	res, err := Discover(context.Background(), src, testServices(), defaultLabels, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hosts) != 2 {
		t.Fatalf("hosts = %d, want 2", len(res.Hosts))
	}
	h := res.Host("10.0.0.1")
	if h == nil || len(h.Targets) != 2 {
		t.Fatalf("merged host = %+v", h)
	}
	if h.Name != "db-bj1-001" {
		t.Errorf("name = %q, want uname nodename", h.Name)
	}
	if got := h.ServiceIDs(); !reflect.DeepEqual(got, []string{"mysql", "node", "redis"}) {
		t.Errorf("services = %v", got)
	}
	for _, s := range h.Services {
		switch s.ID {
		case "node":
			if !reflect.DeepEqual(s.Sources, []string{"job", "app", "metric"}) {
				t.Errorf("node sources = %v", s.Sources)
			}
		case "redis":
			if !reflect.DeepEqual(s.Sources, []string{"systemd"}) {
				t.Errorf("redis sources = %v", s.Sources)
			}
		}
	}
	if len(h.MissingLabels) != 0 || !h.Normal() {
		t.Errorf("missing=%v status=%s", h.MissingLabels, h.Status)
	}
	// 10.0.0.2 has no uname data: falls back to its IP; its unknown job is unclassified.
	h2 := res.Host("10.0.0.2")
	if h2.Name != "10.0.0.2" {
		t.Errorf("fallback name = %q, want IP", h2.Name)
	}
	if got := h2.ServiceIDs(); !reflect.DeepEqual(got, []string{"redis"}) {
		t.Errorf("host2 services = %v", got)
	}
	if len(res.Unclassified) != 1 || res.Unclassified[0].Job != "legacy-billing" {
		t.Errorf("unclassified = %+v", res.Unclassified)
	}
}

func TestHostnameLabelBeatsUname(t *testing.T) {
	src := &fakeSource{
		targets: []promv1.ActiveTarget{tgt("node", "", "10.0.0.1:9100", true, "", map[string]string{"hostname": "from-label"})},
		answers: map[string]model.Vector{"node_uname_info": vecOf(model.Metric{"instance": "10.0.0.1:9100", "nodename": "from-uname"})},
	}
	lc := defaultLabels
	lc.Hostname = "hostname"
	res, err := Discover(context.Background(), src, testServices(), lc, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Hosts[0].Name; got != "from-label" {
		t.Errorf("name = %q", got)
	}
}

func TestDownCauseClassification(t *testing.T) {
	src := &fakeSource{targets: []promv1.ActiveTarget{
		tgt("node", "", "10.0.0.1:9100", false, `dial tcp 10.0.0.1:9100: connect: no route to host`, fullLabels()),
		tgt("mysql", "", "10.0.0.1:9104", false, `dial tcp: i/o timeout`, fullLabels()),
		tgt("node", "", "10.0.0.2:9100", false, "connection refused", fullLabels()),
		tgt("mysql", "", "10.0.0.2:9104", true, "", fullLabels()),
		tgt("node", "", "10.0.0.3:9100", true, "", fullLabels()),
	}}
	res, err := Discover(context.Background(), src, testServices(), defaultLabels, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	whole, part, ok := res.Host("10.0.0.1"), res.Host("10.0.0.2"), res.Host("10.0.0.3")
	if whole.DownCause != CauseHostDown || whole.Status != StatusHostDown || !whole.NetworkHint || whole.DownText != CauseHostDownText {
		t.Errorf("whole host = %+v", whole)
	}
	if part.DownCause != CauseExporterDown || part.Normal() || !reflect.DeepEqual(part.DownJobs, []string{"node"}) ||
		!strings.Contains(part.DownError, "refused") || part.NetworkHint {
		t.Errorf("exporter down host = %+v", part)
	}
	if !ok.Normal() || ok.DownCause != "" {
		t.Errorf("healthy host = %+v", ok)
	}
}

func TestDownHostWithoutUnameFallsBackToIP(t *testing.T) {
	src := &fakeSource{targets: []promv1.ActiveTarget{
		tgt("node", "", "10.0.0.22:9100", false, "no route to host", nil),
		tgt("mysql", "", "10.0.0.22:9104", false, "no route to host", nil),
	}}
	res, err := Discover(context.Background(), src, testServices(), defaultLabels, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	h := res.Hosts[0]
	if h.Name != "10.0.0.22" || h.Status != StatusHostDown || len(h.MissingLabels) != len(LabelFields) {
		t.Errorf("host = %+v", h)
	}
}

func TestMissingLabelsAndMappingChange(t *testing.T) {
	partial := map[string]string{"env": "prod", "role": "app", "dc": "sh1", "owner": "ops"}
	src := &fakeSource{targets: []promv1.ActiveTarget{
		tgt("node", "", "10.0.0.1:9100", true, "", fullLabels()),
		tgt("node", "", "10.0.0.2:9100", true, "", partial),
	}}
	run := func(lc config.LabelsConfig) *Result {
		res, err := Discover(context.Background(), src, testServices(), lc, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := run(defaultLabels)
	if got := res.MissingLabelHosts(); len(got) != 1 || got[0].Key != "10.0.0.2" || !reflect.DeepEqual(got[0].MissingLabels, []string{"biz"}) {
		t.Errorf("missing = %+v", got)
	}
	if v, fb := res.Host("10.0.0.2").GroupValue("biz"); !fb || v != "node" {
		t.Errorf("GroupValue fallback = %q,%v", v, fb)
	}
	if v, fb := res.Host("10.0.0.1").GroupValue("biz"); fb || v != "pay" {
		t.Errorf("GroupValue = %q,%v", v, fb)
	}

	lc := defaultLabels
	lc.Biz = "business_unit"
	if got := run(lc).MissingLabelHosts(); len(got) != 2 {
		t.Errorf("mapped to absent label: %d hosts flagged, want 2", len(got))
	}
	lc.Biz = "env" // mapping decides which source label feeds the field
	if h := run(lc).Host("10.0.0.2"); len(h.MissingLabels) != 0 || h.Labels["biz"] != "prod" {
		t.Errorf("remapped host = %+v", h)
	}
	if got := run(defaultLabels).MissingLabelHosts(); len(got) != 1 {
		t.Errorf("restored mapping: %d flagged, want 1", len(got))
	}
}

func TestQueryCountDependsOnRulesNotHosts(t *testing.T) {
	count := func(n int) int {
		src := &fakeSource{}
		for i := 0; i < n; i++ {
			src.targets = append(src.targets, tgt("node", "", fmt.Sprintf("10.0.%d.%d:9100", i/250, i%250), true, "", fullLabels()))
		}
		res, err := Discover(context.Background(), src, testServices(), defaultLabels, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Queries != len(src.queries)+1 { // +1 for the targets call
			t.Errorf("Queries=%d, issued=%d", res.Queries, len(src.queries))
		}
		return res.Queries
	}
	if a, b := count(5), count(300); a != b {
		t.Errorf("query count grew with hosts: %d vs %d", a, b)
	}
}

func TestFallbackToUpWhenTargetsFail(t *testing.T) {
	src := &fakeSource{
		targetsErr: errors.New("boom"),
		answers: map[string]model.Vector{"up": {
			{Metric: model.Metric{"job": "node", "instance": "10.0.0.9:9100"}, Value: 0},
		}},
	}
	res, err := Discover(context.Background(), src, testServices(), defaultLabels, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if h := res.Host("10.0.0.9"); h == nil || h.Status != StatusHostDown {
		t.Errorf("host = %+v", h)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a warning about targets")
	}
}

func TestHostKeyStripsPort(t *testing.T) {
	for in, want := range map[string]string{"10.0.0.1:9100": "10.0.0.1", "10.0.0.1": "10.0.0.1", "[::1]:9100": "::1", "h.example:80": "h.example"} {
		if got := HostKey(in); got != want {
			t.Errorf("HostKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDefaultMockMatchesManifest runs discovery against the in-process mock
// and compares every manifest fact.
func TestDefaultMockMatchesManifest(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the 180 host mock")
	}
	sc, err := config.LoadScenario("../../config/mock-scenario.yaml")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := mock.New(sc, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
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

	res, err := Discover(context.Background(), client, svc, app.Labels, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m := srv.Fleet().Manifest()
	if len(res.Hosts) != m.Hosts {
		t.Fatalf("hosts = %d, want %d", len(res.Hosts), m.Hosts)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings: %v", res.Warnings)
	}

	byName := map[string]*Host{}
	for _, h := range res.Hosts {
		byName[h.Name] = h
	}
	sources := map[string]bool{}
	for name, want := range m.Services {
		h := byName[name]
		if h == nil {
			t.Errorf("host %s not discovered by name", name)
			continue
		}
		got := h.ServiceIDs()
		w := append([]string{}, want...)
		sort.Strings(w)
		if !reflect.DeepEqual(got, w) {
			t.Errorf("%s services = %v, want %v", name, got, w)
		}
		for _, s := range h.Services {
			for _, src := range s.Sources {
				sources[src] = true
			}
		}
	}
	for _, s := range []string{SourceJob, SourceApp, SourceMetric, SourceSystemd} {
		if !sources[s] {
			t.Errorf("no service detected via %s", s)
		}
	}

	for _, d := range m.Down {
		h := byName[d.Host]
		if h == nil || h.DownCause != d.Cause || h.Normal() {
			t.Errorf("down %s: got %+v, want cause %s", d.Host, h, d.Cause)
		}
	}
	downs := 0
	for _, h := range res.Hosts {
		if !h.Normal() {
			downs++
		}
	}
	if downs != len(m.Down) {
		t.Errorf("non-normal hosts = %d, want %d", downs, len(m.Down))
	}

	var jobs []string
	for _, u := range res.Unclassified {
		jobs = append(jobs, u.Job)
	}
	if !reflect.DeepEqual(jobs, m.UnclassifiedJobs) {
		t.Errorf("unclassified = %v, want %v", jobs, m.UnclassifiedJobs)
	}

	var missing []string
	for _, h := range res.MissingLabelHosts() {
		missing = append(missing, h.Name)
	}
	want := append([]string{}, m.MissingLabelHosts...)
	sort.Strings(missing)
	sort.Strings(want)
	if !reflect.DeepEqual(missing, want) {
		t.Errorf("missing label hosts = %v, want %v", missing, want)
	}

	wantQueries := 3 // targets, up, uname
	if svc.SystemdUnits.Metric != "" {
		wantQueries++
	}
	for _, r := range svc.Services {
		if len(r.Match.MetricPrefixes) > 0 {
			wantQueries++
		}
	}
	if res.Queries != wantQueries {
		t.Errorf("queries = %d, want %d", res.Queries, wantQueries)
	}
}
