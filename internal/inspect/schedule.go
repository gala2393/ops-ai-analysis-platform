package inspect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/robfig/cron/v3"

	"yunwei/internal/store"
)

// Scheduler fires scheduled inspections for every configured cron spec.
type Scheduler struct {
	cron *cron.Cron
}

// NewScheduler registers one job per standard 5-field cron spec. A tick that
// finds a run in progress is skipped, not queued.
func NewScheduler(ctx context.Context, runner *Runner, specs []string, log *slog.Logger) (*Scheduler, error) {
	if log == nil {
		log = slog.Default()
	}
	c := cron.New(cron.WithParser(cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)))
	for _, spec := range specs {
		spec := spec
		if _, err := c.AddFunc(spec, func() {
			out, err := runner.Run(ctx, store.TriggerSchedule)
			switch {
			case errors.Is(err, ErrRunning):
				log.Warn("scheduled inspection skipped: already running", "spec", spec)
			case err != nil:
				log.Error("scheduled inspection failed", "spec", spec, "err", err)
			default:
				log.Info("scheduled inspection finished", "spec", spec, "run", out.Run.ID,
					"hosts", out.Run.Hosts.Total, "crit", out.Run.Hosts.Crit)
			}
		}); err != nil {
			return nil, fmt.Errorf("schedule %q: %w", spec, err)
		}
	}
	return &Scheduler{cron: c}, nil
}

// Start begins firing in the background.
func (s *Scheduler) Start() { s.cron.Start() }

// Stop halts new ticks and waits for a running job to finish.
func (s *Scheduler) Stop() { <-s.cron.Stop().Done() }

// Entries returns the number of registered specs.
func (s *Scheduler) Entries() int { return len(s.cron.Entries()) }
