package inspect

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"yunwei/internal/config"
	"yunwei/internal/mock"
	"yunwei/internal/prom"
	"yunwei/internal/store"
)

func startMock(t *testing.T, scenario string) *httptest.Server {
	t.Helper()
	sc, err := config.LoadScenario(scenario)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := mock.New(sc, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func newRunnerFor(t *testing.T, promURL string, st *store.Store) *Runner {
	t.Helper()
	b, err := config.LoadBundle("../../config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	client, err := prom.New(prom.Config{URL: promURL, Timeout: 30 * time.Second, MaxConcurrency: 4,
		MaxRange: 24 * time.Hour, MinStep: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{Store: st, Prom: client, Config: b}
}

func TestRunnerEndToEndAssetDiff(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the 180 host mock twice")
	}
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	base := startMock(t, "../../config/mock-scenario.yaml")
	out1, err := newRunnerFor(t, base.URL, st).Run(ctx, store.TriggerCLI)
	if err != nil {
		t.Fatal(err)
	}
	r := out1.Run
	if r.Status != store.StatusSuccess || r.Trigger != store.TriggerCLI || r.FinishedAt == nil ||
		r.Progress.Done != r.Progress.Total || r.Progress.Total == 0 || r.Hosts.Total != 180 ||
		r.Hosts.OK+r.Hosts.Warn+r.Hosts.Crit != 180 || r.Query.Count < 14 || r.Query.P95MS < 0 {
		t.Errorf("run record: %+v", r)
	}
	if out1.Diff.HasPrevious || !out1.Diff.Empty() {
		t.Errorf("first run diff: %+v", out1.Diff)
	}
	if r.Query.Count != out1.Result.Queries {
		t.Errorf("record queries %d != result %d", r.Query.Count, out1.Result.Queries)
	}

	v2 := startMock(t, "../../config/scenarios/assets-v2.yaml")
	out2, err := newRunnerFor(t, v2.URL, st).Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	d := out2.Diff
	if !d.HasPrevious || d.PreviousRunID != r.ID || len(d.AddedHosts) != 6 || len(d.RemovedHosts) != 5 {
		t.Fatalf("diff: has=%v prev=%d added=%d removed=%d", d.HasPrevious, d.PreviousRunID, len(d.AddedHosts), len(d.RemovedHosts))
	}
	var removed []string
	for _, h := range d.RemovedHosts {
		removed = append(removed, h.Name)
	}
	sort.Strings(removed)
	if want := []string{"app-bj1-001", "app-gz1-011", "app-sh1-001", "cache-bj1-003", "web-bj1-003"}; !reflect.DeepEqual(removed, want) {
		t.Errorf("removed = %v", removed)
	}
	gained, lost := map[string][]string{}, map[string][]string{}
	for _, c := range d.ServicesAdded {
		gained[c.Name] = c.Services
	}
	for _, c := range d.ServicesRemoved {
		lost[c.Name] = c.Services
	}
	for host, svc := range map[string]string{"db-bj1-008": "mysql", "db-bj1-009": "mysql", "app-bj1-002": "redis"} {
		if !reflect.DeepEqual(gained[host], []string{svc}) {
			t.Errorf("%s gained %v, want [%s]", host, gained[host], svc)
		}
	}
	for host, svc := range map[string]string{"cache-bj1-001": "redis", "cache-bj1-002": "redis", "web-bj1-001": "nginx"} {
		if !reflect.DeepEqual(lost[host], []string{svc}) {
			t.Errorf("%s lost %v, want [%s]", host, lost[host], svc)
		}
	}

	// Persisted run documents round-trip.
	det, err := st.LoadRun(ctx, out2.Run.ID)
	if err != nil || len(det.Hosts) != len(out2.Discovery.Hosts) || len(det.Findings) != len(out2.Result.Findings) || len(det.Detail) == 0 {
		t.Fatalf("loaded run: %v hosts=%d findings=%d", err, len(det.Hosts), len(det.Findings))
	}
}

func TestRunnerRejectsConcurrentAndRecoversStale(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// An unreachable Prometheus makes runs fail fast, but the lock is real.
	r := newRunnerFor(t, "http://127.0.0.1:1", st)

	// Another process holds a live lock.
	held, err := st.StartRun(ctx, store.TriggerManual, "other", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, store.TriggerCLI); !errors.Is(err, ErrRunning) {
		t.Fatalf("err = %v, want ErrRunning", err)
	}
	if runs, _ := st.ListRuns(ctx, 0); len(runs) != 1 {
		t.Errorf("rejected run left a record: %d", len(runs))
	}

	// The holder died: its lock expired an hour ago, so the next run proceeds
	// (and fails only because Prometheus is down), marking the zombie failed.
	st2, err := store.Open(filepath.Join(t.TempDir(), "z.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	st, r = st2, newRunnerFor(t, "http://127.0.0.1:1", st2)
	held, err = st.StartRun(ctx, store.TriggerManual, "dead", time.Minute, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, store.TriggerCLI); err == nil || errors.Is(err, ErrRunning) {
		t.Fatalf("stale lock run err = %v, want a discovery failure", err)
	}
	old, _ := st.GetRun(ctx, held.ID)
	if old.Status != store.StatusFailed {
		t.Errorf("zombie status = %s", old.Status)
	}
	runs, _ := st.ListRuns(ctx, 0)
	for _, x := range runs {
		if x.Status == store.StatusRunning {
			t.Errorf("run %d still running", x.ID)
		}
	}
}

func TestRunnerInProcessGuard(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := newRunnerFor(t, "http://127.0.0.1:1", st)
	r.inProcess.Lock()
	defer r.inProcess.Unlock()
	if _, err := r.Run(context.Background(), store.TriggerManual); !errors.Is(err, ErrRunning) {
		t.Errorf("err = %v", err)
	}
}

func TestSchedulerFiresScheduledRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the 180 host mock")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ts := startMock(t, "../../config/mock-scenario.yaml")
	r := newRunnerFor(t, ts.URL, st)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := NewScheduler(ctx, r, []string{"@every 1s", "30 8 * * *"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Entries() != 2 {
		t.Fatalf("entries = %d", s.Entries())
	}
	s.Start()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := st.ListRuns(ctx, 0)
		n := 0
		for _, x := range runs {
			if x.Trigger == store.TriggerSchedule && x.Status == store.StatusSuccess {
				n++
			}
		}
		if n >= 2 {
			s.Stop()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.Stop()
	t.Fatal("fewer than 2 scheduled runs completed")
}

func TestSchedulerRejectsBadSpec(t *testing.T) {
	if _, err := NewScheduler(context.Background(), &Runner{}, []string{"nonsense"}, nil); err == nil {
		t.Error("bad spec accepted")
	}
}
