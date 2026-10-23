// Package prom is a read-only, bounded wrapper around the Prometheus HTTP API
// client. It only exposes query endpoints; no admin or write APIs are reachable.
package prom

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

var (
	// ErrTimeout is returned (wrapped) when a single query exceeds Config.Timeout.
	ErrTimeout = errors.New("prometheus query timeout")
	// ErrRangeTooLarge is returned when a range query spans more than Config.MaxRange.
	ErrRangeTooLarge = errors.New("prometheus range exceeds max_range")
	// ErrInvalidRange is returned when end is before start.
	ErrInvalidRange = errors.New("prometheus range end is before start")
)

// Config bounds how the client may load Prometheus.
type Config struct {
	URL string
	// Timeout applies to each query once it holds a concurrency slot.
	Timeout time.Duration
	// MaxConcurrency is the maximum number of requests in flight.
	MaxConcurrency int
	// MaxRange rejects range queries (and series/label lookups) wider than this.
	MaxRange time.Duration
	// MinStep raises smaller range-query steps up to this value.
	MinStep time.Duration
}

func (c Config) withDefaults() Config {
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	if c.MaxConcurrency < 1 {
		c.MaxConcurrency = 4
	}
	if c.MaxRange <= 0 {
		c.MaxRange = 24 * time.Hour
	}
	if c.MinStep <= 0 {
		c.MinStep = 60 * time.Second
	}
	return c
}

// Client is safe for concurrent use.
type Client struct {
	api   promv1.API
	cfg   Config
	sem   chan struct{}
	stats *statsCollector
}

// New builds a Client for cfg.URL.
func New(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        cfg.MaxConcurrency,
		MaxIdleConnsPerHost: cfg.MaxConcurrency,
		IdleConnTimeout:     60 * time.Second,
	}
	hc, err := promapi.NewClient(promapi.Config{Address: cfg.URL, RoundTripper: transport})
	if err != nil {
		return nil, fmt.Errorf("prometheus.url %q: %w", cfg.URL, err)
	}
	return &Client{
		api:   promv1.NewAPI(hc),
		cfg:   cfg,
		sem:   make(chan struct{}, cfg.MaxConcurrency),
		stats: &statsCollector{},
	}, nil
}

// Scoped returns a view that shares the connection and concurrency limit but
// keeps its own QueryStats, so one inspection run can report its own numbers.
func (c *Client) Scoped() *Client {
	cp := *c
	cp.stats = &statsCollector{}
	return &cp
}

// Stats returns a snapshot of the queries recorded so far.
func (c *Client) Stats() QueryStats { return c.stats.snapshot() }

// ResetStats clears the recorded queries.
func (c *Client) ResetStats() { c.stats.reset() }

// Config returns the effective limits.
func (c *Client) Config() Config { return c.cfg }

// do runs fn holding one concurrency slot, under the per-query timeout, and records its duration.
func (c *Client) do(ctx context.Context, op string, fn func(ctx context.Context) error) error {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.sem }()

	qctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	start := time.Now()
	err := fn(qctx)
	c.stats.record(time.Since(start), err)
	if err != nil && errors.Is(qctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return fmt.Errorf("%w: %s exceeded %s: %w", ErrTimeout, op, c.cfg.Timeout, err)
	}
	return err
}

// Query runs an instant query at ts (zero means now).
func (c *Client) Query(ctx context.Context, query string, ts time.Time) (model.Value, promv1.Warnings, error) {
	if ts.IsZero() {
		ts = time.Now()
	}
	var (
		val   model.Value
		warns promv1.Warnings
	)
	err := c.do(ctx, "query", func(ctx context.Context) (err error) {
		val, warns, err = c.api.Query(ctx, query, ts)
		return err
	})
	return val, warns, err
}

// NormalizeRange validates r against the configured caps. A span wider than
// MaxRange is rejected; a step below MinStep is raised to MinStep.
func (c *Client) NormalizeRange(r promv1.Range) (promv1.Range, error) {
	if r.End.Before(r.Start) {
		return r, ErrInvalidRange
	}
	if r.End.Sub(r.Start) > c.cfg.MaxRange {
		return r, fmt.Errorf("%w: %s > %s", ErrRangeTooLarge, r.End.Sub(r.Start), c.cfg.MaxRange)
	}
	if r.Step < c.cfg.MinStep {
		r.Step = c.cfg.MinStep
	}
	return r, nil
}

// QueryRange runs a range query after applying the range and step caps.
func (c *Client) QueryRange(ctx context.Context, query string, r promv1.Range) (model.Value, promv1.Warnings, error) {
	r, err := c.NormalizeRange(r)
	if err != nil {
		return nil, nil, err
	}
	var (
		val   model.Value
		warns promv1.Warnings
	)
	err = c.do(ctx, "query_range", func(ctx context.Context) (err error) {
		val, warns, err = c.api.QueryRange(ctx, query, r)
		return err
	})
	return val, warns, err
}

// Targets returns active and dropped scrape targets.
func (c *Client) Targets(ctx context.Context) (promv1.TargetsResult, error) {
	var res promv1.TargetsResult
	err := c.do(ctx, "targets", func(ctx context.Context) (err error) {
		res, err = c.api.Targets(ctx)
		return err
	})
	return res, err
}

// LabelValues lists values of label, optionally restricted by series matchers
// and a time window (zero times mean unbounded).
func (c *Client) LabelValues(ctx context.Context, label string, matches []string, start, end time.Time) (model.LabelValues, promv1.Warnings, error) {
	if err := c.checkWindow(start, end); err != nil {
		return nil, nil, err
	}
	var (
		vals  model.LabelValues
		warns promv1.Warnings
	)
	err := c.do(ctx, "label_values", func(ctx context.Context) (err error) {
		vals, warns, err = c.api.LabelValues(ctx, label, matches, start, end)
		return err
	})
	return vals, warns, err
}

// Series finds series matching the selectors in the window.
func (c *Client) Series(ctx context.Context, matches []string, start, end time.Time) ([]model.LabelSet, promv1.Warnings, error) {
	if err := c.checkWindow(start, end); err != nil {
		return nil, nil, err
	}
	var (
		set   []model.LabelSet
		warns promv1.Warnings
	)
	err := c.do(ctx, "series", func(ctx context.Context) (err error) {
		set, warns, err = c.api.Series(ctx, matches, start, end)
		return err
	})
	return set, warns, err
}

// Buildinfo returns the server version information.
func (c *Client) Buildinfo(ctx context.Context) (promv1.BuildinfoResult, error) {
	var res promv1.BuildinfoResult
	err := c.do(ctx, "buildinfo", func(ctx context.Context) (err error) {
		res, err = c.api.Buildinfo(ctx)
		return err
	})
	return res, err
}

func (c *Client) checkWindow(start, end time.Time) error {
	if start.IsZero() || end.IsZero() {
		return nil
	}
	_, err := c.NormalizeRange(promv1.Range{Start: start, End: end, Step: c.cfg.MinStep})
	return err
}
