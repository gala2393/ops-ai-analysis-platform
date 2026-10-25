package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"yunwei/internal/config"
	"yunwei/internal/discovery"
	"yunwei/internal/prom"
	"yunwei/internal/store"
)

// DefaultLockTTL is how long a run lock stays valid without a heartbeat.
const DefaultLockTTL = 2 * time.Minute

// ErrRunning is returned when another inspection is already in progress.
var ErrRunning = store.ErrRunning

// Detail is the run document stored next to the run record: everything the
// UI and reports need besides findings, groups and the host snapshot.
type Detail struct {
	Items        []ItemRun                   `json:"items"`
	Hosts        []*HostHealth               `json:"host_health"`
	ByBiz        []Aggregate                 `json:"by_biz"`
	ByEnv        []Aggregate                 `json:"by_env"`
	Top          []TopList                   `json:"top"`
	Unclassified []discovery.UnclassifiedJob `json:"unclassified_jobs"`
	Warnings     []string                    `json:"warnings,omitempty"`
	Diff         *discovery.AssetDiff        `json:"asset_diff"`
	Unmapped     int                         `json:"unmapped"`
}

// ReportFiles are the report paths written for a completed run.
type ReportFiles struct {
	Markdown string
	HTML     string
}

// Outcome is the in-memory result of a completed run.
type Outcome struct {
	Run       *store.Run
	Result    *Result
	Discovery *discovery.Result
	Diff      *discovery.AssetDiff
	Detail    Detail
	// ReportFiles and ReportErr come from the Runner.OnSuccess hook, when set.
	ReportFiles *ReportFiles
	ReportErr   error
}

// Runner orchestrates complete inspection runs: lock, discovery, evaluation,
// asset diff, persistence and retention.
type Runner struct {
	Store  *store.Store
	Prom   *prom.Client
	Config *config.Bundle
	// LockTTL defaults to DefaultLockTTL.
	LockTTL time.Duration
	Log     *slog.Logger

	// OnSuccess, when set, runs after a run is persisted as successful. It is
	// how report files get written for every trigger (schedule, manual, CLI);
	// the runner itself cannot import internal/report (import cycle). Its
	// result lands on the outcome and its error is logged, never turning a
	// stored success into a failure.
	OnSuccess func(ctx context.Context, out *Outcome) (*ReportFiles, error)

	// inProcess rejects a second run in this process without touching the DB.
	inProcess sync.Mutex
}

func (r *Runner) ttl() time.Duration {
	if r.LockTTL > 0 {
		return r.LockTTL
	}
	return DefaultLockTTL
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Run executes one inspection. A concurrent run, in this process or any
// other sharing the database, is rejected with ErrRunning.
func (r *Runner) Run(ctx context.Context, trigger string) (*Outcome, error) {
	if !r.inProcess.TryLock() {
		return nil, ErrRunning
	}
	defer r.inProcess.Unlock()

	host, _ := os.Hostname()
	run, err := r.Store.StartRun(ctx, trigger, fmt.Sprintf("%s:%d", host, os.Getpid()), r.ttl(), time.Now())
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopBeat := r.heartbeat(runCtx, cancel, run.ID)

	out, err := r.execute(runCtx, run)
	stopBeat()

	// Bookkeeping must survive a cancelled run context.
	bg, bgCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer bgCancel()
	if err != nil {
		if ferr := r.Store.FailRun(bg, run.ID, err.Error(), time.Now()); ferr != nil {
			r.log().Error("mark run failed", "run", run.ID, "err", ferr)
		}
	}
	if rerr := r.Store.ReleaseLock(bg, run.ID); rerr != nil {
		r.log().Error("release run lock", "run", run.ID, "err", rerr)
	}
	if err != nil {
		return nil, err
	}
	r.purge(bg)
	if out.Run, err = r.Store.GetRun(bg, run.ID); err != nil {
		return nil, err
	}
	r.onSuccess(out)
	return out, nil
}

// onSuccess runs the report hook with a fresh context so a cancelled run
// context cannot block report writing.
func (r *Runner) onSuccess(out *Outcome) {
	if r.OnSuccess == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	files, err := r.OnSuccess(ctx, out)
	out.ReportFiles, out.ReportErr = files, err
	switch {
	case err != nil:
		r.log().Error("报告文件写入失败 (report write failed)", "run", out.Run.ID, "err", err)
	case files != nil:
		r.log().Info("报告文件已写入 (report files written)", "run", out.Run.ID,
			"markdown", files.Markdown, "html", files.HTML)
	}
}

// heartbeat renews the lock until stopped; losing the lock cancels the run.
func (r *Runner) heartbeat(ctx context.Context, cancel context.CancelFunc, id int64) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(r.ttl() / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if err := r.Store.RenewLock(ctx, id, r.ttl(), time.Now()); err != nil && ctx.Err() == nil {
					r.log().Error("run lock lost", "run", id, "err", err)
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

func (r *Runner) execute(ctx context.Context, run *store.Run) (*Outcome, error) {
	app := r.Config.App
	now := time.Now()
	scoped := r.Prom.Scoped()

	disc, err := discovery.Discover(ctx, scoped, r.Config.Services, app.Labels, now)
	if err != nil {
		return nil, fmt.Errorf("发现主机失败 (discovery): %w", err)
	}
	total := len(r.Config.Inspection.EnabledItems())
	_ = r.Store.SetProgress(ctx, run.ID, 0, total)

	history, err := r.Store.HostHistory(ctx)
	if err != nil {
		return nil, err
	}
	res, err := Run(ctx, scoped, r.Config.Inspection, disc, Options{
		Concurrency: app.Prometheus.MaxConcurrency,
		MaxRange:    app.Prometheus.MaxRange,
		MinStep:     app.Prometheus.MinStep,
		TopN:        app.Report.TopN,
		Now:         now,
		History:     history,
		OnProgress: func(done, total int) {
			_ = r.Store.SetProgress(ctx, run.ID, done, total)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("执行巡检项失败: %w", err)
	}
	if len(res.Items) > 0 && len(res.Errors()) == len(res.Items) {
		return nil, fmt.Errorf("所有巡检项均执行失败 (all items failed): %s", res.Items[0].Error)
	}

	diff, err := r.assetDiff(ctx, disc)
	if err != nil {
		return nil, err
	}
	st := scoped.Stats()
	res.Queries = st.Count
	detail := Detail{
		Items: res.Items, Hosts: res.Hosts, ByBiz: res.ByBiz, ByEnv: res.ByEnv, Top: res.Top,
		Unclassified: disc.Unclassified, Warnings: disc.Warnings, Diff: diff, Unmapped: res.Unmapped,
	}
	sr, err := toStoreResult(res, disc, detail, st, time.Now())
	if err != nil {
		return nil, err
	}
	if err := r.Store.CompleteRun(ctx, run.ID, sr); err != nil {
		return nil, fmt.Errorf("保存巡检结果失败: %w", err)
	}
	return &Outcome{Result: res, Discovery: disc, Diff: diff, Detail: detail}, nil
}

// assetDiff compares with the newest successful run; the first run has no diff.
func (r *Runner) assetDiff(ctx context.Context, disc *discovery.Result) (*discovery.AssetDiff, error) {
	prev, err := r.Store.LatestSuccess(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return discovery.NoPreviousDiff(), nil
	}
	if err != nil {
		return nil, err
	}
	d, err := r.Store.LoadRun(ctx, prev.ID)
	if err != nil {
		return nil, err
	}
	var hosts []*discovery.Host
	for _, raw := range d.Hosts {
		var h discovery.Host
		if err := json.Unmarshal(raw, &h); err != nil {
			return nil, fmt.Errorf("读取上次主机快照失败: %w", err)
		}
		hosts = append(hosts, &h)
	}
	return discovery.Diff(hosts, disc.Hosts, prev.ID), nil
}

// purge drops data past retention; failures are logged, never fatal.
func (r *Runner) purge(ctx context.Context) {
	days := r.Config.App.Storage.RetentionDays
	if days < 1 {
		return
	}
	n, err := r.Store.PurgeBefore(ctx, time.Now().AddDate(0, 0, -days))
	if err != nil {
		r.log().Error("retention purge", "err", err)
		return
	}
	if n > 0 {
		r.log().Info("retention purge", "runs_removed", n, "retention_days", days)
	}
}

func toStoreResult(res *Result, disc *discovery.Result, detail Detail, st prom.QueryStats, at time.Time) (store.Result, error) {
	sr := store.Result{
		Hosts:      store.HostCounts{Total: res.Counts.Total, OK: res.Counts.OK, Warn: res.Counts.Warn, Crit: res.Counts.Crit},
		Query:      store.QueryStats{Count: st.Count, TotalMS: st.TotalMS(), P95MS: st.P95MS()},
		FinishedAt: at,
	}
	var err error
	if sr.Detail, err = json.Marshal(detail); err != nil {
		return sr, err
	}
	for _, f := range res.Findings {
		raw, err := json.Marshal(f)
		if err != nil {
			return sr, err
		}
		sr.Findings = append(sr.Findings, store.FindingRow{CheckID: f.CheckID, HostKey: f.HostKey, Host: f.Host,
			Level: f.Level, Severity: f.Severity, Value: f.Value, Finding: raw})
	}
	for _, g := range res.Groups {
		raw, err := json.Marshal(g)
		if err != nil {
			return sr, err
		}
		sr.Groups = append(sr.Groups, store.GroupRow{CheckID: g.CheckID, Level: g.Level, Severity: g.Severity,
			Title: g.Title, HostCount: g.Count, Group: raw})
	}
	for _, h := range disc.Hosts {
		raw, err := json.Marshal(h)
		if err != nil {
			return sr, err
		}
		sr.Snapshot = append(sr.Snapshot, store.HostRow{Key: h.Key, Name: h.Name, Host: raw})
	}
	return sr, nil
}
