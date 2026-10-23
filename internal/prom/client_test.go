package prom

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
)

const emptyVector = `{"status":"success","data":{"resultType":"vector","result":[]}}`
const emptyMatrix = `{"status":"success","data":{"resultType":"matrix","result":[]}}`

func newTestClient(t *testing.T, h http.Handler, cfg Config) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg.URL = srv.URL
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestQueryConcurrencyIsBounded(t *testing.T) {
	const limit, total = 3, 20
	var inflight, peak atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		_, _ = w.Write([]byte(emptyVector))
	}), Config{MaxConcurrency: limit, Timeout: 5 * time.Second})

	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := c.Query(context.Background(), "up", time.Time{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if got := peak.Load(); got > limit {
		t.Fatalf("peak in-flight = %d, want <= %d", got, limit)
	}
	if got := peak.Load(); got < limit {
		t.Errorf("peak in-flight = %d, expected the limit %d to be reached", got, limit)
	}
	if st := c.Stats(); st.Count != total {
		t.Errorf("stats count = %d, want %d", st.Count, total)
	}
}

func TestQueryTimeout(t *testing.T) {
	stop := make(chan struct{})
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}), Config{Timeout: 80 * time.Millisecond})
	t.Cleanup(func() { close(stop) })

	start := time.Now()
	_, _, err := c.Query(context.Background(), "up", time.Time{})
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want ErrTimeout wrapping DeadlineExceeded", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("timeout took %s", time.Since(start))
	}
	if st := c.Stats(); st.Count != 1 || st.Errors != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestQueueWaitDoesNotCountAgainstTimeout(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond)
		_, _ = w.Write([]byte(emptyVector))
	}), Config{MaxConcurrency: 1, Timeout: 150 * time.Millisecond})

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := c.Query(context.Background(), "up", time.Time{}); err != nil {
				t.Errorf("queued query failed: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestCallerCancelWhileQueued(t *testing.T) {
	release := make(chan struct{})
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte(emptyVector))
	}), Config{MaxConcurrency: 1, Timeout: 5 * time.Second})

	go func() { _, _, _ = c.Query(context.Background(), "up", time.Time{}) }()
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := c.Query(ctx, "up", time.Time{})
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestRangeCaps(t *testing.T) {
	var gotStep atomic.Value
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotStep.Store(r.Form.Get("step"))
		_, _ = w.Write([]byte(emptyMatrix))
	}), Config{MaxRange: 24 * time.Hour, MinStep: 60 * time.Second})

	now := time.Now()
	tests := []struct {
		name     string
		r        promv1.Range
		wantErr  error
		wantStep string
	}{
		{"within caps", promv1.Range{Start: now.Add(-6 * time.Hour), End: now, Step: 5 * time.Minute}, nil, "300"},
		{"exactly max range", promv1.Range{Start: now.Add(-24 * time.Hour), End: now, Step: time.Minute}, nil, "60"},
		{"step below min is raised", promv1.Range{Start: now.Add(-time.Hour), End: now, Step: time.Second}, nil, "60"},
		{"range above max rejected", promv1.Range{Start: now.Add(-25 * time.Hour), End: now, Step: time.Minute}, ErrRangeTooLarge, ""},
		{"end before start rejected", promv1.Range{Start: now, End: now.Add(-time.Hour), Step: time.Minute}, ErrInvalidRange, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStep.Store("")
			_, _, err := c.QueryRange(context.Background(), "up", tt.r)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := gotStep.Load().(string); got != tt.wantStep {
				t.Errorf("server saw step %q, want %q", got, tt.wantStep)
			}
		})
	}
}

func TestSeriesAndLabelValuesRespectMaxRange(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
	}), Config{MaxRange: time.Hour})
	now := time.Now()
	if _, _, err := c.Series(context.Background(), []string{"up"}, now.Add(-2*time.Hour), now); !errors.Is(err, ErrRangeTooLarge) {
		t.Errorf("series err = %v", err)
	}
	if _, _, err := c.LabelValues(context.Background(), "job", nil, now.Add(-2*time.Hour), now); !errors.Is(err, ErrRangeTooLarge) {
		t.Errorf("label values err = %v", err)
	}
	if _, _, err := c.LabelValues(context.Background(), "job", nil, time.Time{}, time.Time{}); err != nil {
		t.Errorf("unbounded label values: %v", err)
	}
}

func TestEndpoints(t *testing.T) {
	paths := sync.Map{}
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths.Store(r.URL.Path, true)
		switch r.URL.Path {
		case "/api/v1/status/buildinfo":
			_, _ = w.Write([]byte(`{"status":"success","data":{"version":"3.1.0","revision":"r","branch":"b","buildUser":"u","buildDate":"d","goVersion":"go1"}}`))
		case "/api/v1/targets":
			_, _ = w.Write([]byte(`{"status":"success","data":{"activeTargets":[{"discoveredLabels":{},"labels":{"job":"node","instance":"10.0.0.1:9100"},"scrapePool":"node","scrapeUrl":"http://10.0.0.1:9100/metrics","globalUrl":"","lastError":"boom","lastScrape":"2026-01-01T00:00:00Z","lastScrapeDuration":0.1,"health":"down"}],"droppedTargets":[]}}`))
		case "/api/v1/label/job/values":
			_, _ = w.Write([]byte(`{"status":"success","data":["node","mysql"]}`))
		case "/api/v1/series":
			_, _ = w.Write([]byte(`{"status":"success","data":[{"__name__":"up","job":"node"}]}`))
		default:
			_, _ = w.Write([]byte(emptyVector))
		}
	}), Config{})
	ctx := context.Background()

	bi, err := c.Buildinfo(ctx)
	if err != nil || bi.Version != "3.1.0" {
		t.Errorf("buildinfo = %+v, %v", bi, err)
	}
	tg, err := c.Targets(ctx)
	if err != nil || len(tg.Active) != 1 || tg.Active[0].Health != promv1.HealthBad || tg.Active[0].LastError != "boom" {
		t.Errorf("targets = %+v, %v", tg, err)
	}
	lv, _, err := c.LabelValues(ctx, "job", nil, time.Time{}, time.Time{})
	if err != nil || len(lv) != 2 {
		t.Errorf("label values = %v, %v", lv, err)
	}
	ss, _, err := c.Series(ctx, []string{"up"}, time.Time{}, time.Time{})
	if err != nil || len(ss) != 1 {
		t.Errorf("series = %v, %v", ss, err)
	}
	if _, _, err := c.Query(ctx, "up", time.Time{}); err != nil {
		t.Error(err)
	}
	if st := c.Stats(); st.Count != 5 {
		t.Errorf("stats count = %d, want 5", st.Count)
	}
}

func TestServerErrorIsReportedAndCounted(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"parse error"}`))
	}), Config{})
	_, _, err := c.Query(context.Background(), "up{", time.Time{})
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrTimeout) {
		t.Errorf("a bad_data response must not look like a timeout: %v", err)
	}
	if st := c.Stats(); st.Errors != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestStatsPercentiles(t *testing.T) {
	var c statsCollector
	if s := c.snapshot(); s.Count != 0 || s.P95 != 0 {
		t.Errorf("empty stats = %+v", s)
	}
	// Recorded out of order on purpose.
	for _, ms := range []int{100, 1, 50, 99, 2, 98} {
		c.record(time.Duration(ms)*time.Millisecond, nil)
	}
	for ms := 3; ms <= 97; ms++ {
		if ms == 50 {
			continue
		}
		c.record(time.Duration(ms)*time.Millisecond, nil)
	}
	c.record(7*time.Millisecond, errors.New("x"))
	s := c.snapshot()
	// Values are 1..100 plus an extra 7: 101 samples, nearest-rank P95 is the 96th value.
	if s.Count != 101 || s.Errors != 1 {
		t.Fatalf("count/errors = %d/%d", s.Count, s.Errors)
	}
	if s.P95 != 95*time.Millisecond {
		t.Errorf("P95 = %s, want 95ms", s.P95)
	}
	if s.Max != 100*time.Millisecond || s.P95MS() != 95 {
		t.Errorf("max = %s p95ms = %d", s.Max, s.P95MS())
	}
	if want := (5050 + 7) * time.Millisecond; s.Total != want || s.TotalMS() != 5057 {
		t.Errorf("total = %s, want %s", s.Total, want)
	}

	var single statsCollector
	single.record(12*time.Millisecond, nil)
	if s := single.snapshot(); s.P95 != 12*time.Millisecond {
		t.Errorf("single P95 = %s", s.P95)
	}
}

func TestScopedStatsAreIndependent(t *testing.T) {
	var inflight, peak atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		if n > peak.Load() {
			peak.Store(n)
		}
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(emptyVector))
	}), Config{MaxConcurrency: 2})
	a, b := c.Scoped(), c.Scoped()

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		cl := a
		if i%2 == 1 {
			cl = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = cl.Query(context.Background(), "up", time.Time{})
		}()
	}
	wg.Wait()

	if a.Stats().Count != 3 || b.Stats().Count != 3 || c.Stats().Count != 0 {
		t.Errorf("counts a=%d b=%d parent=%d", a.Stats().Count, b.Stats().Count, c.Stats().Count)
	}
	if peak.Load() > 2 {
		t.Errorf("scoped views must share the concurrency limit, peak = %d", peak.Load())
	}
	a.ResetStats()
	if a.Stats().Count != 0 {
		t.Error("ResetStats did not clear")
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	if _, err := New(Config{URL: "://bad"}); err == nil {
		t.Error("expected error for malformed URL")
	}
}
