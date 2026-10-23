package mock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
)

const (
	maxPointsPerSeries = 11000
	mockVersion        = "3.0.0-yunwei-mock"
)

// Handler returns the HTTP API of the mock Prometheus.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query", s.counted(&s.stats.instant, s.handleQuery))
	mux.HandleFunc("/api/v1/query_range", s.counted(&s.stats.rangeQ, s.handleQueryRange))
	mux.HandleFunc("/api/v1/targets", s.counted(&s.stats.targets, s.handleTargets))
	mux.HandleFunc("/api/v1/label/{name}/values", s.counted(&s.stats.labels, s.handleLabelValues))
	mux.HandleFunc("/api/v1/labels", s.counted(&s.stats.labels, s.handleLabels))
	mux.HandleFunc("/api/v1/series", s.counted(&s.stats.series, s.handleSeries))
	mux.HandleFunc("/api/v1/status/buildinfo", s.counted(&s.stats.other, s.handleBuildinfo))
	mux.HandleFunc("/-/healthy", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Prometheus Server is Healthy.\n"))
	})
	mux.HandleFunc("/-/ready", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Prometheus Server is Ready.\n"))
	})
	mux.HandleFunc("/mock/stats", s.handleStats)
	return mux
}

// apiError is a client-visible failure with its Prometheus error type.
type apiError struct {
	code    int
	typ     string
	message string
}

func badData(format string, a ...any) *apiError {
	return &apiError{http.StatusBadRequest, "bad_data", fmt.Sprintf(format, a...)}
}

func (e *apiError) write(w http.ResponseWriter) { writeError(w, e.code, e.typ, e.message) }

// engineError maps a PromQL engine error the way the Prometheus API does.
func engineError(err error) *apiError {
	var (
		perr    parser.ParseErrors
		timeout promql.ErrQueryTimeout
		cancel  promql.ErrQueryCanceled
		storage promql.ErrStorage
	)
	switch {
	case errors.As(err, &perr):
		return &apiError{http.StatusBadRequest, "bad_data", err.Error()}
	case errors.As(err, &timeout):
		return &apiError{http.StatusServiceUnavailable, "timeout", err.Error()}
	case errors.As(err, &cancel):
		return &apiError{http.StatusServiceUnavailable, "canceled", err.Error()}
	case errors.As(err, &storage):
		return &apiError{http.StatusInternalServerError, "internal", err.Error()}
	}
	return &apiError{http.StatusUnprocessableEntity, "execution", err.Error()}
}

func parseTimeParam(name, raw string) (time.Time, *apiError) {
	if f, err := strconv.ParseFloat(raw, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		sec, frac := math.Modf(f)
		return time.Unix(int64(sec), int64(math.Round(frac*1000))*int64(time.Millisecond)).UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, nil
	}
	return time.Time{}, badData("invalid parameter %q: cannot parse %q to a valid timestamp", name, raw)
}

func parseDurationParam(name, raw string) (time.Duration, *apiError) {
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		ns := f * float64(time.Second)
		if ns > float64(math.MaxInt64) || ns < float64(math.MinInt64) {
			return 0, badData("invalid parameter %q: cannot parse %q to a valid duration. It overflows int64", name, raw)
		}
		return time.Duration(ns), nil
	}
	if d, err := model.ParseDuration(raw); err == nil {
		return time.Duration(d), nil
	}
	return 0, badData("invalid parameter %q: cannot parse %q to a valid duration", name, raw)
}

func (s *Server) requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), engineQueryBudget)
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if !methodIn(w, r, http.MethodGet, http.MethodPost) || !parseForm(w, r) {
		return
	}
	expr := r.FormValue("query")
	ts := time.Now()
	if raw := r.FormValue("time"); raw != "" {
		var perr *apiError
		if ts, perr = parseTimeParam("time", raw); perr != nil {
			perr.write(w)
			return
		}
	}
	ctx, cancel := s.requestContext(r)
	defer cancel()
	q, err := s.store.eng.NewInstantQuery(ctx, s.store.db, nil, expr, ts)
	if err != nil {
		engineError(err).write(w)
		return
	}
	defer q.Close()
	s.writeResult(w, q.Exec(ctx))
}

func (s *Server) handleQueryRange(w http.ResponseWriter, r *http.Request) {
	if !methodIn(w, r, http.MethodGet, http.MethodPost) || !parseForm(w, r) {
		return
	}
	start, perr := parseTimeParam("start", r.FormValue("start"))
	if perr != nil {
		perr.write(w)
		return
	}
	end, perr := parseTimeParam("end", r.FormValue("end"))
	if perr != nil {
		perr.write(w)
		return
	}
	step, perr := parseDurationParam("step", r.FormValue("step"))
	if perr != nil {
		perr.write(w)
		return
	}
	switch {
	case end.Before(start):
		badData("invalid parameter %q: end timestamp must not be before start time", "end").write(w)
		return
	case step <= 0:
		badData("invalid parameter %q: zero or negative query resolution step widths are not accepted. Try a positive integer", "step").write(w)
		return
	case end.Sub(start)/step > maxPointsPerSeries:
		badData("exceeded maximum resolution of %d points per timeseries. Try decreasing the query resolution (?step=XX)", maxPointsPerSeries).write(w)
		return
	}
	ctx, cancel := s.requestContext(r)
	defer cancel()
	q, err := s.store.eng.NewRangeQuery(ctx, s.store.db, nil, r.FormValue("query"), start, end, step)
	if err != nil {
		engineError(err).write(w)
		return
	}
	defer q.Close()
	s.writeResult(w, q.Exec(ctx))
}

func (s *Server) writeResult(w http.ResponseWriter, res *promql.Result) {
	if res.Err != nil {
		engineError(res.Err).write(w)
		return
	}
	var warnings []string
	for _, err := range res.Warnings.AsErrors() {
		warnings = append(warnings, err.Error())
	}
	data := queryData{ResultType: string(res.Value.Type())}
	switch v := res.Value.(type) {
	case promql.Vector:
		out := make([]vectorSample, 0, len(v))
		for _, smp := range v {
			if smp.H != nil {
				continue
			}
			out = append(out, vectorSample{smp.Metric.Map(), floatPair(smp.T, smp.F)})
		}
		data.Result = out
	case promql.Matrix:
		out := make([]matrixSeries, 0, len(v))
		for _, series := range v {
			vals := make([]samplePair, len(series.Floats))
			for i, p := range series.Floats {
				vals[i] = floatPair(p.T, p.F)
			}
			out = append(out, matrixSeries{series.Metric.Map(), vals})
		}
		data.Result = out
	case promql.Scalar:
		data.Result = floatPair(v.T, v.V)
	case promql.String:
		data.Result = samplePair{v.T, v.V}
	default:
		writeError(w, http.StatusInternalServerError, "internal", fmt.Sprintf("unsupported result type %T", res.Value))
		return
	}
	writeData(w, data, warnings)
}

type targetJSON struct {
	DiscoveredLabels   map[string]string `json:"discoveredLabels"`
	Labels             map[string]string `json:"labels"`
	ScrapePool         string            `json:"scrapePool"`
	ScrapeURL          string            `json:"scrapeUrl"`
	GlobalURL          string            `json:"globalUrl"`
	LastError          string            `json:"lastError"`
	LastScrape         string            `json:"lastScrape"`
	LastScrapeDuration float64           `json:"lastScrapeDuration"`
	Health             string            `json:"health"`
	ScrapeInterval     string            `json:"scrapeInterval"`
	ScrapeTimeout      string            `json:"scrapeTimeout"`
}

func (s *Server) handleTargets(w http.ResponseWriter, r *http.Request) {
	if !methodIn(w, r, http.MethodGet) {
		return
	}
	active := []targetJSON{}
	if state := r.URL.Query().Get("state"); state != "dropped" {
		last := time.UnixMilli(s.store.last.Load()).UTC().Format(time.RFC3339Nano)
		for _, h := range s.fleet.Hosts {
			for _, t := range h.Targets {
				active = append(active, s.targetJSON(h, t, last))
			}
		}
	}
	writeData(w, map[string]any{"activeTargets": active, "droppedTargets": []any{}}, nil)
}

func (s *Server) targetJSON(h *Host, t *Target, lastScrape string) targetJSON {
	url := "http://" + t.Instance + "/metrics"
	lbls := map[string]string{}
	pairs := targetLabelPairs(h, t)
	for i := 0; i < len(pairs); i += 2 {
		lbls[pairs[i]] = pairs[i+1]
	}
	health, dur := "up", 0.012
	if t.Down {
		health, dur = "down", 10
	}
	return targetJSON{
		DiscoveredLabels: map[string]string{
			"__address__": t.Instance, "__metrics_path__": "/metrics", "__scheme__": "http",
			"__scrape_interval__": "30s", "__scrape_timeout__": "10s", "job": t.Job,
		},
		Labels: lbls, ScrapePool: t.Job, ScrapeURL: url, GlobalURL: url,
		LastError: t.LastError, LastScrape: lastScrape, LastScrapeDuration: dur,
		Health: health, ScrapeInterval: "30s", ScrapeTimeout: "10s",
	}
}

// metadataRange resolves the optional start/end window and match[] selectors
// shared by the label and series endpoints.
func (s *Server) metadataRange(r *http.Request) (mint, maxt int64, matcherSets [][]*labels.Matcher, perr *apiError) {
	now := time.Now()
	start, end := now.Add(-retention), now.Add(time.Hour)
	if raw := r.FormValue("start"); raw != "" {
		if start, perr = parseTimeParam("start", raw); perr != nil {
			return
		}
	}
	if raw := r.FormValue("end"); raw != "" {
		if end, perr = parseTimeParam("end", raw); perr != nil {
			return
		}
	}
	for _, m := range r.Form["match[]"] {
		ms, err := parser.ParseMetricSelector(m)
		if err != nil {
			return 0, 0, nil, badData("invalid parameter %q: %v", "match[]", err)
		}
		matcherSets = append(matcherSets, ms)
	}
	return start.UnixMilli(), end.UnixMilli(), matcherSets, nil
}

func (s *Server) handleLabelValues(w http.ResponseWriter, r *http.Request) {
	if !methodIn(w, r, http.MethodGet) || !parseForm(w, r) {
		return
	}
	name := r.PathValue("name")
	if !model.LabelNameRE.MatchString(name) {
		badData("invalid label name: %q", name).write(w)
		return
	}
	s.writeLabelNames(w, r, name)
}

func (s *Server) handleLabels(w http.ResponseWriter, r *http.Request) {
	if !methodIn(w, r, http.MethodGet, http.MethodPost) || !parseForm(w, r) {
		return
	}
	s.writeLabelNames(w, r, "")
}

// writeLabelNames answers label values for name, or label names when name is "".
func (s *Server) writeLabelNames(w http.ResponseWriter, r *http.Request, name string) {
	mint, maxt, sets, perr := s.metadataRange(r)
	if perr != nil {
		perr.write(w)
		return
	}
	q, err := s.store.db.Querier(mint, maxt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	defer q.Close()
	ctx, cancel := s.requestContext(r)
	defer cancel()
	if len(sets) == 0 {
		sets = [][]*labels.Matcher{nil}
	}
	seen := map[string]struct{}{}
	for _, ms := range sets {
		var vals []string
		if name == "" {
			vals, _, err = q.LabelNames(ctx, nil, ms...)
		} else {
			vals, _, err = q.LabelValues(ctx, name, nil, ms...)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		for _, v := range vals {
			seen[v] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	writeData(w, out, nil)
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	if !methodIn(w, r, http.MethodGet, http.MethodPost) || !parseForm(w, r) {
		return
	}
	mint, maxt, sets, perr := s.metadataRange(r)
	if perr != nil {
		perr.write(w)
		return
	}
	if len(sets) == 0 {
		badData("no match[] parameter provided").write(w)
		return
	}
	q, err := s.store.db.Querier(mint, maxt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	defer q.Close()
	ctx, cancel := s.requestContext(r)
	defer cancel()
	out := []map[string]string{}
	seen := map[uint64]struct{}{}
	for _, ms := range sets {
		ss := q.Select(ctx, false, nil, ms...)
		for ss.Next() {
			l := ss.At().Labels()
			if _, dup := seen[l.Hash()]; dup {
				continue
			}
			seen[l.Hash()] = struct{}{}
			out = append(out, l.Map())
		}
		if err := ss.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
	}
	writeData(w, out, nil)
}

func (s *Server) handleBuildinfo(w http.ResponseWriter, r *http.Request) {
	if !methodIn(w, r, http.MethodGet) {
		return
	}
	writeData(w, map[string]string{
		"version": mockVersion, "revision": "mock", "branch": "mock",
		"buildUser": "yunwei", "buildDate": "20260101-00:00:00", "goVersion": "go1.24",
	}, nil)
}

func methodIn(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	for _, m := range allowed {
		if r.Method == m {
			return true
		}
	}
	writeError(w, http.StatusMethodNotAllowed, "bad_data", fmt.Sprintf("method %s not allowed", r.Method))
	return false
}

func parseForm(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		badData("error parsing form values: %v", err).write(w)
		return false
	}
	return true
}
