package mock

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/tsdb"
)

const (
	retention         = 7 * 24 * time.Hour
	outOfOrderWindow  = 24 * time.Hour
	maxQuerySamples   = 50_000_000
	engineQueryBudget = 30 * time.Second

	defaultSubqueryStep = time.Minute
)

// Store couples the TSDB holding the synthetic series with the PromQL engine.
type Store struct {
	db   *tsdb.DB
	eng  *promql.Engine
	dir  string
	defs []*seriesDef
	last atomic.Int64 // unix ms of the latest scrape
}

// OpenStore creates an empty TSDB in a fresh temp directory.
func OpenStore(defs []*seriesDef) (*Store, error) {
	dir, err := os.MkdirTemp("", "yunwei-mockprom-")
	if err != nil {
		return nil, err
	}
	opts := tsdb.DefaultOptions()
	opts.RetentionDuration = retention.Milliseconds()
	opts.OutOfOrderTimeWindow = outOfOrderWindow.Milliseconds()
	opts.WALSegmentSize = -1 // nothing to recover after a restart, skip the WAL
	db, err := tsdb.Open(dir, nil, nil, opts, nil)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("open tsdb: %w", err)
	}
	eng := promql.NewEngine(promql.EngineOpts{
		MaxSamples:           maxQuerySamples,
		Timeout:              engineQueryBudget,
		LookbackDelta:        5 * time.Minute,
		EnableAtModifier:     true,
		EnableNegativeOffset: true,
		// Required for subqueries without a step such as metric[1h:]; the
		// engine panics on a nil callback.
		NoStepSubqueryIntervalFn: func(int64) int64 { return defaultSubqueryStep.Milliseconds() },
	})
	return &Store{db: db, eng: eng, dir: dir, defs: defs}, nil
}

// AppendAt writes one scrape of every series at timestamp t. Timestamps must
// not go backwards between calls.
func (s *Store) AppendAt(ctx context.Context, t time.Time) error {
	app := s.db.Appender(ctx)
	for _, d := range s.defs {
		v, ok := d.fn(t)
		if !ok {
			continue
		}
		ref, err := app.Append(d.ref, d.lbls, t.UnixMilli(), v)
		if err != nil {
			_ = app.Rollback()
			return fmt.Errorf("append %s: %w", d.lbls, err)
		}
		d.ref = ref
	}
	if err := app.Commit(); err != nil {
		return err
	}
	s.last.Store(t.UnixMilli())
	return nil
}

// Backfill writes history ending at end (inclusive) at the given step.
func (s *Store) Backfill(ctx context.Context, end time.Time, history, step time.Duration) error {
	for t := end.Add(-history); !t.After(end); t = t.Add(step) {
		if err := s.AppendAt(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

// Close shuts the TSDB down and removes its temp directory.
func (s *Store) Close() error {
	err := s.db.Close()
	if rerr := os.RemoveAll(s.dir); err == nil {
		err = rerr
	}
	return err
}
