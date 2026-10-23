package prom

import (
	"math"
	"sort"
	"sync"
	"time"
)

// QueryStats summarises the queries issued through a Client (or a scoped view of it).
type QueryStats struct {
	Count  int           `json:"count"`
	Errors int           `json:"errors"`
	Total  time.Duration `json:"total_ns"`
	P95    time.Duration `json:"p95_ns"`
	Max    time.Duration `json:"max_ns"`
}

// TotalMS is the summed query time in milliseconds.
func (s QueryStats) TotalMS() int64 { return s.Total.Milliseconds() }

// P95MS is the 95th percentile query time in milliseconds.
func (s QueryStats) P95MS() int64 { return s.P95.Milliseconds() }

type statsCollector struct {
	mu        sync.Mutex
	durations []time.Duration
	errors    int
}

func (c *statsCollector) record(d time.Duration, err error) {
	c.mu.Lock()
	c.durations = append(c.durations, d)
	if err != nil {
		c.errors++
	}
	c.mu.Unlock()
}

func (c *statsCollector) reset() {
	c.mu.Lock()
	c.durations, c.errors = nil, 0
	c.mu.Unlock()
}

func (c *statsCollector) snapshot() QueryStats {
	c.mu.Lock()
	ds := append([]time.Duration(nil), c.durations...)
	errs := c.errors
	c.mu.Unlock()

	s := QueryStats{Count: len(ds), Errors: errs}
	if len(ds) == 0 {
		return s
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	for _, d := range ds {
		s.Total += d
	}
	s.Max = ds[len(ds)-1]
	// Nearest-rank percentile.
	s.P95 = ds[int(math.Ceil(0.95*float64(len(ds))))-1]
	return s
}
