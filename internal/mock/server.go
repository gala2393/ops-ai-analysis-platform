package mock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"yunwei/internal/config"
)

type counters struct {
	instant, rangeQ, targets, labels, series, other atomic.Int64
}

// Server is the mock Prometheus: fleet, TSDB, PromQL engine and HTTP API.
type Server struct {
	sc      *config.Scenario
	fleet   *Fleet
	store   *Store
	stats   counters
	started time.Time
	log     *slog.Logger
}

// New builds the fleet for sc, opens the TSDB and backfills the history.
func New(sc *config.Scenario, log *slog.Logger) (*Server, error) {
	return newServer(sc, BuildFleet(sc, time.Now()), log)
}

func newServer(sc *config.Scenario, fleet *Fleet, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	now := fleet.Anchor
	store, err := OpenStore(fleet.buildSeries())
	if err != nil {
		return nil, err
	}
	s := &Server{sc: sc, fleet: fleet, store: store, started: now, log: log}
	if err := store.Backfill(context.Background(), now.Truncate(sc.Step), sc.History, sc.Step); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("backfill: %w", err)
	}
	if err := s.appendLive(context.Background()); err != nil {
		_ = store.Close()
		return nil, err
	}
	return s, nil
}

// Fleet exposes the generated estate (used for the expected manifest and tests).
func (s *Server) Fleet() *Fleet { return s.fleet }

// Close releases the TSDB and its temp directory.
func (s *Server) Close() error { return s.store.Close() }

func (s *Server) appendLive(ctx context.Context) error {
	t := time.Now()
	if t.UnixMilli() <= s.store.last.Load() {
		return nil
	}
	return s.store.AppendAt(ctx, t)
}

// RunLive appends a fresh scrape every LiveInterval until ctx ends.
func (s *Server) RunLive(ctx context.Context) {
	if s.sc.LiveInterval <= 0 {
		return
	}
	tick := time.NewTicker(s.sc.LiveInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := s.appendLive(ctx); err != nil {
				s.log.Error("live append failed", "err", err)
			}
		}
	}
}

// Serve serves the API on ln and keeps the data live until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go s.RunLive(ctx)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func (s *Server) counted(c *atomic.Int64, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.Add(1)
		h(w, r)
	}
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	instant, ranged := s.stats.instant.Load(), s.stats.rangeQ.Load()
	other := s.stats.targets.Load() + s.stats.labels.Load() + s.stats.series.Load() + s.stats.other.Load()
	writeJSON(w, http.StatusOK, map[string]any{
		"queries_total":         instant + ranged,
		"instant_queries":       instant,
		"range_queries":         ranged,
		"targets_requests":      s.stats.targets.Load(),
		"label_requests":        s.stats.labels.Load(),
		"series_requests":       s.stats.series.Load(),
		"other_requests":        s.stats.other.Load(),
		"requests_total":        instant + ranged + other,
		"hosts":                 len(s.fleet.Hosts),
		"targets":               s.fleet.TargetCount(),
		"head_series":           s.store.db.Head().NumSeries(),
		"last_scrape_unix_ms":   s.store.last.Load(),
		"uptime_seconds":        int64(time.Since(s.started).Seconds()),
		"history_seconds":       int64(s.sc.History.Seconds()),
		"live_interval_seconds": int64(s.sc.LiveInterval.Seconds()),
	})
}
