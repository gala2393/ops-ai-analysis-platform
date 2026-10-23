package mock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"yunwei/internal/config"
	"yunwei/internal/prom"
)

func testScenario() *config.Scenario {
	return &config.Scenario{
		Hosts: 12, Seed: 7, History: 2 * time.Hour, Step: time.Minute, LiveInterval: 0,
		MissingLabelFraction: 0.25,
		MySQLHosts:           3, RedisHosts: 2, NginxHosts: 2, UnknownHosts: 1,
	}
}

func startMock(t *testing.T, sc *config.Scenario, mutate func(*Fleet)) (*Server, *httptest.Server) {
	t.Helper()
	fleet := BuildFleet(sc, time.Now())
	if mutate != nil {
		mutate(fleet)
	}
	s, err := newServer(sc, fleet, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = s.Close()
	})
	return s, ts
}

type envelope struct {
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
}

func get(t *testing.T, base, path string, params url.Values) (int, envelope) {
	t.Helper()
	resp, err := http.Get(base + path + "?" + params.Encode())
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, resp)
}

func post(t *testing.T, base, path string, params url.Values) (int, envelope) {
	t.Helper()
	resp, err := http.PostForm(base+path, params)
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, resp)
}

func decode(t *testing.T, resp *http.Response) (int, envelope) {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var e envelope
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("not JSON (%d): %s", resp.StatusCode, b)
	}
	return resp.StatusCode, e
}

type vectorData struct {
	ResultType string `json:"resultType"`
	Result     []struct {
		Metric map[string]string `json:"metric"`
		Value  []any             `json:"value"`
		Values [][]any           `json:"values"`
	} `json:"result"`
}

func queryVector(t *testing.T, base, q string) vectorData {
	t.Helper()
	code, e := get(t, base, "/api/v1/query", url.Values{"query": {q}})
	if code != 200 || e.Status != "success" {
		t.Fatalf("query %q: %d %s %s", q, code, e.Status, e.Error)
	}
	var d vectorData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestInstantQueryShape(t *testing.T) {
	s, ts := startMock(t, testScenario(), nil)
	d := queryVector(t, ts.URL, "up")
	if d.ResultType != "vector" || len(d.Result) != s.fleet.TargetCount() {
		t.Fatalf("type=%s n=%d want %d", d.ResultType, len(d.Result), s.fleet.TargetCount())
	}
	r := d.Result[0]
	if r.Metric["job"] == "" || r.Metric["instance"] == "" {
		t.Errorf("metric lacks job/instance: %v", r.Metric)
	}
	if _, ok := r.Value[0].(float64); !ok || len(r.Value) != 2 {
		t.Errorf("value[0] must be a unix float: %v", r.Value)
	}
	if str, ok := r.Value[1].(string); !ok || str != "1" {
		t.Errorf("value[1] must be a string number: %v", r.Value)
	}
}

func TestInstantQueryGetAndPostAgree(t *testing.T) {
	s, ts := startMock(t, testScenario(), nil)
	for name, f := range map[string]func() envelope{
		"GET": func() envelope { _, e := get(t, ts.URL, "/api/v1/query", url.Values{"query": {"count(up)"}}); return e },
		"POST": func() envelope {
			_, e := post(t, ts.URL, "/api/v1/query", url.Values{"query": {"count(up)"}})
			return e
		},
	} {
		var d vectorData
		e := f()
		if err := json.Unmarshal(e.Data, &d); err != nil || len(d.Result) != 1 {
			t.Fatalf("%s: %v %s", name, err, e.Data)
		}
		if got := d.Result[0].Value[1]; got != strconv.Itoa(s.fleet.TargetCount()) {
			t.Errorf("%s: count(up) = %v, want %d", name, got, s.fleet.TargetCount())
		}
	}
}

func TestRangeQueryPointCount(t *testing.T) {
	_, ts := startMock(t, testScenario(), nil)
	end := time.Now()
	code, e := get(t, ts.URL, "/api/v1/query_range", url.Values{
		"query": {"node_load1"}, "step": {"60"},
		"start": {strconv.FormatInt(end.Add(-time.Hour).Unix(), 10)}, "end": {strconv.FormatInt(end.Unix(), 10)},
	})
	if code != 200 {
		t.Fatalf("%d %s", code, e.Error)
	}
	var d vectorData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.ResultType != "matrix" || len(d.Result) != 12 {
		t.Fatalf("type=%s series=%d", d.ResultType, len(d.Result))
	}
	if n := len(d.Result[0].Values); n < 55 || n > 62 {
		t.Errorf("points = %d, want 55..62", n)
	}
}

func TestPromQLFunctionsEvaluate(t *testing.T) {
	_, ts := startMock(t, testScenario(), nil)
	for _, q := range []string{
		`rate(node_cpu_seconds_total[5m])`,
		`predict_linear(node_filesystem_avail_bytes[1h], 86400)`,
		`max_over_time(node_load1[1h])`,
		`increase(node_vmstat_oom_kill[24h])`,
		`count by (job) (up)`,
		`100 - avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100`,
		`node_load1 / on (instance) group_left count by (instance) (node_cpu_seconds_total{mode="idle"})`,
	} {
		if d := queryVector(t, ts.URL, q); len(d.Result) == 0 {
			t.Errorf("%s: empty result", q)
		}
	}
	// A busy-fraction check: idle CPU usage must be a sane percentage.
	for _, r := range queryVector(t, ts.URL, `100 - avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100`).Result {
		v, _ := strconv.ParseFloat(r.Value[1].(string), 64)
		if v < 1 || v > 60 {
			t.Errorf("%s cpu usage %.2f outside expected band", r.Metric["instance"], v)
		}
	}
}

func TestSubqueryWithOmittedStep(t *testing.T) {
	_, ts := startMock(t, testScenario(), nil)
	const q = `max_over_time(node_load1[1h:])`
	end := time.Now()
	rangeParams := url.Values{
		"query": {q}, "step": {"60"},
		"start": {strconv.FormatInt(end.Add(-30*time.Minute).Unix(), 10)}, "end": {strconv.FormatInt(end.Unix(), 10)},
	}
	for name, f := range map[string]func() (int, envelope){
		"instant GET":  func() (int, envelope) { return get(t, ts.URL, "/api/v1/query", url.Values{"query": {q}}) },
		"instant POST": func() (int, envelope) { return post(t, ts.URL, "/api/v1/query", url.Values{"query": {q}}) },
		"range GET":    func() (int, envelope) { return get(t, ts.URL, "/api/v1/query_range", rangeParams) },
		"range POST":   func() (int, envelope) { return post(t, ts.URL, "/api/v1/query_range", rangeParams) },
	} {
		code, e := f()
		if code != 200 || e.Status != "success" {
			t.Errorf("%s: %d %s %s", name, code, e.Status, e.Error)
			continue
		}
		var d vectorData
		if err := json.Unmarshal(e.Data, &d); err != nil || len(d.Result) != 12 {
			t.Errorf("%s: err=%v series=%d", name, err, len(d.Result))
		}
	}
}

func TestHandlerPanicReturnsPrometheusError(t *testing.T) {
	s, ts := startMock(t, testScenario(), nil)
	s.store.eng = nil // forces a nil dereference inside the query handler
	code, e := get(t, ts.URL, "/api/v1/query", url.Values{"query": {"up"}})
	if code != 500 || e.Status != "error" || e.ErrorType != "internal" || e.Error == "" {
		t.Errorf("panic response: %d %+v", code, e)
	}
}

func TestErrorResponsesUsePrometheusFormat(t *testing.T) {
	_, ts := startMock(t, testScenario(), nil)
	code, e := get(t, ts.URL, "/api/v1/query", url.Values{"query": {"sum("}})
	if code != 400 || e.Status != "error" || e.ErrorType != "bad_data" || e.Error == "" {
		t.Errorf("bad promql: %d %+v", code, e)
	}
	now := strconv.FormatInt(time.Now().Unix(), 10)
	for name, p := range map[string]url.Values{
		"missing step":   {"query": {"up"}, "start": {now}, "end": {now}},
		"negative step":  {"query": {"up"}, "start": {now}, "end": {now}, "step": {"-1"}},
		"end before":     {"query": {"up"}, "start": {now}, "end": {"1"}, "step": {"15"}},
		"too many point": {"query": {"up"}, "start": {"0"}, "end": {now}, "step": {"1"}},
		"bad time":       {"query": {"up"}, "time": {"yesterday"}},
	} {
		path := "/api/v1/query_range"
		if name == "bad time" {
			path = "/api/v1/query"
		}
		code, e := get(t, ts.URL, path, p)
		if code != 400 || e.Status != "error" || e.ErrorType != "bad_data" {
			t.Errorf("%s: %d %+v", name, code, e)
		}
	}
	if code, e := get(t, ts.URL, "/api/v1/series", nil); code != 400 || e.ErrorType != "bad_data" {
		t.Errorf("series without match[]: %d %+v", code, e)
	}
}

func TestTargetsMatchUpSeries(t *testing.T) {
	s, ts := startMock(t, testScenario(), func(f *Fleet) {
		tg := f.Hosts[0].Targets[0]
		tg.Down, tg.LastError = true, "dial tcp: connect: no route to host"
	})
	code, e := get(t, ts.URL, "/api/v1/targets", nil)
	if code != 200 {
		t.Fatalf("%d", code)
	}
	var d struct {
		Active []struct {
			Labels     map[string]string `json:"labels"`
			ScrapeURL  string            `json:"scrapeUrl"`
			Health     string            `json:"health"`
			LastError  string            `json:"lastError"`
			LastScrape string            `json:"lastScrape"`
		} `json:"activeTargets"`
		Dropped []any `json:"droppedTargets"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Active) != s.fleet.TargetCount() || d.Dropped == nil {
		t.Fatalf("active=%d want %d", len(d.Active), s.fleet.TargetCount())
	}
	down := 0
	for _, a := range d.Active {
		if a.LastScrape == "" || a.ScrapeURL == "" {
			t.Errorf("incomplete target %+v", a)
		}
		if a.Health == "down" {
			down++
			if a.LastError == "" {
				t.Error("down target without lastError")
			}
		}
	}
	if down != 1 {
		t.Errorf("down targets = %d, want 1", down)
	}
	if got := queryVector(t, ts.URL, "up == 0").Result; len(got) != 1 {
		t.Errorf("up==0 series = %d, want 1", len(got))
	}
	// A down target exposes only `up`.
	if got := queryVector(t, ts.URL, `node_load1{instance="`+s.fleet.Hosts[0].Targets[0].Instance+`"}`).Result; len(got) != 0 {
		t.Errorf("down node still exposes node_load1")
	}
}

func TestMetadataEndpoints(t *testing.T) {
	_, ts := startMock(t, testScenario(), nil)
	_, e := get(t, ts.URL, "/api/v1/label/__name__/values", nil)
	var names []string
	if err := json.Unmarshal(e.Data, &names); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, want := range []string{"up", "node_cpu_seconds_total", "node_filesystem_avail_bytes", "node_filesystem_files_free",
		"node_uname_info", "node_boot_time_seconds", "node_vmstat_oom_kill", "node_systemd_unit_state", "node_load1",
		"mysql_up", "mysql_global_status_threads_connected", "mysql_global_variables_max_connections", "redis_up"} {
		if !have[want] {
			t.Errorf("metric name %s missing", want)
		}
	}
	_, e = get(t, ts.URL, "/api/v1/label/job/values", nil)
	var jobs []string
	_ = json.Unmarshal(e.Data, &jobs)
	if strings.Join(jobs, ",") != "legacy-billing,mysql,nginx,node,redis" {
		t.Errorf("jobs = %v", jobs)
	}
	_, e = get(t, ts.URL, "/api/v1/series", url.Values{"match[]": {`up{job="mysql"}`}})
	var series []map[string]string
	if err := json.Unmarshal(e.Data, &series); err != nil || len(series) != 3 || series[0]["__name__"] != "up" {
		t.Errorf("series = %v err=%v", series, err)
	}
	_, e = get(t, ts.URL, "/api/v1/status/buildinfo", nil)
	var bi map[string]string
	if err := json.Unmarshal(e.Data, &bi); err != nil || bi["version"] == "" {
		t.Errorf("buildinfo = %v", bi)
	}
	resp, err := http.Get(ts.URL + "/-/healthy")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("healthy: %v %v", resp, err)
	}
	resp.Body.Close()
}

func TestStatsCountsQueries(t *testing.T) {
	_, ts := startMock(t, testScenario(), nil)
	total := func() int64 {
		resp, err := http.Get(ts.URL + "/mock/stats")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var st map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			t.Fatal(err)
		}
		return int64(st["queries_total"].(float64))
	}
	before := total()
	queryVector(t, ts.URL, "up")
	post(t, ts.URL, "/api/v1/query", url.Values{"query": {"count(up)"}})
	get(t, ts.URL, "/api/v1/query_range", url.Values{"query": {"up"}, "start": {"1"}, "end": {"2"}, "step": {"1"}})
	get(t, ts.URL, "/api/v1/targets", nil) // not a PromQL query
	if got := total() - before; got != 3 {
		t.Errorf("queries_total advanced by %d, want 3", got)
	}
}

func TestLiveDataAdvances(t *testing.T) {
	s, ts := startMock(t, testScenario(), nil)
	first := queryVector(t, ts.URL, "max(timestamp(node_load1))").Result[0].Value[1].(string)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.sc.LiveInterval = 20 * time.Millisecond
	go s.RunLive(ctx)
	time.Sleep(1200 * time.Millisecond)

	second := queryVector(t, ts.URL, "max(timestamp(node_load1))").Result[0].Value[1].(string)
	a, _ := strconv.ParseFloat(first, 64)
	b, _ := strconv.ParseFloat(second, 64)
	if b-a < 0.5 {
		t.Errorf("timestamp advanced %.3fs, want >= 0.5", b-a)
	}
	// 2h of history at 60s: ~121 samples per series.
	for _, r := range queryVector(t, ts.URL, "count_over_time(node_load1[2h])").Result {
		if n, _ := strconv.Atoi(r.Value[1].(string)); n < 100 {
			t.Fatalf("history too short: %d samples", n)
		}
	}
}

func TestSameSeedSameFleet(t *testing.T) {
	a := BuildFleet(testScenario(), time.Unix(1_700_000_000, 0))
	b := BuildFleet(testScenario(), time.Unix(1_700_000_000, 0))
	ma, _ := json.Marshal(a.Manifest())
	mb, _ := json.Marshal(b.Manifest())
	if string(ma) != string(mb) {
		t.Error("manifest differs for identical seed")
	}
	sc := testScenario()
	sc.Seed = 8
	c, _ := json.Marshal(BuildFleet(sc, time.Unix(1_700_000_000, 0)).Manifest())
	if string(ma) == string(c) {
		t.Error("different seeds produced identical manifests")
	}
}

func TestFleetLabelsAndExporters(t *testing.T) {
	f := BuildFleet(testScenario(), time.Now())
	if len(f.Hosts) != 12 || f.TargetCount() != 12+3+2+2+1 {
		t.Fatalf("hosts=%d targets=%d", len(f.Hosts), f.TargetCount())
	}
	if len(f.MissingLabelHosts()) != 3 {
		t.Errorf("missing label hosts = %v", f.MissingLabelHosts())
	}
	seen := map[string]bool{}
	for _, h := range f.Hosts {
		if seen[h.Name] || seen[h.IP] {
			t.Errorf("duplicate name/ip %s %s", h.Name, h.IP)
		}
		seen[h.Name], seen[h.IP] = true, true
		if h.Target(KindNode) == nil {
			t.Errorf("%s lacks a node target", h.Name)
		}
	}
	if got := f.Manifest().UnclassifiedJobs; len(got) != 1 || got[0] != "legacy-billing" {
		t.Errorf("unclassified = %v", got)
	}
}

func TestClientGolangWorksUnchanged(t *testing.T) {
	s, ts := startMock(t, testScenario(), nil)
	c, err := prom.New(prom.Config{URL: ts.URL, Timeout: 10 * time.Second, MaxConcurrency: 2, MaxRange: 24 * time.Hour, MinStep: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	v, _, err := c.Query(ctx, "count(up)", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if vec, ok := v.(model.Vector); !ok || len(vec) != 1 || int(vec[0].Value) != s.fleet.TargetCount() {
		t.Errorf("Query = %v", v)
	}
	end := time.Now()
	m, _, err := c.QueryRange(ctx, "node_load1", promv1.Range{Start: end.Add(-30 * time.Minute), End: end, Step: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if mat, ok := m.(model.Matrix); !ok || len(mat) != 12 || len(mat[0].Values) < 25 {
		t.Errorf("QueryRange = %v", m)
	}
	tg, err := c.Targets(ctx)
	if err != nil || len(tg.Active) != s.fleet.TargetCount() {
		t.Errorf("Targets: %v n=%d", err, len(tg.Active))
	}
	lv, _, err := c.LabelValues(ctx, "dc", nil, time.Time{}, time.Time{})
	if err != nil || len(lv) < 2 {
		t.Errorf("LabelValues = %v err=%v", lv, err)
	}
	ser, _, err := c.Series(ctx, []string{"up"}, time.Time{}, time.Time{})
	if err != nil || len(ser) != s.fleet.TargetCount() {
		t.Errorf("Series n=%d err=%v", len(ser), err)
	}
	if bi, err := c.Buildinfo(ctx); err != nil || bi.Version == "" {
		t.Errorf("Buildinfo = %+v err=%v", bi, err)
	}
	if _, _, err := c.Query(ctx, "sum(", time.Now()); err == nil {
		t.Error("bad PromQL must surface as an error")
	}
}
