// Package scheduler runs the app's internal periodic jobs: dump reconciliation and
// retention prune.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"zkillmanager/internal/config"
)

// reconcileLimit caps how many biggest-gap days one reconciliation pass pulls. The new
// daily dump is usually the biggest gap, so this comfortably covers "grab the new day plus
// a couple of late-submission catch-ups" and self-heals a backlog over successive runs.
const reconcileLimit = 3

// Reconciler re-pulls the biggest-gap daily dumps (satisfied by *ingest.Syncer). It also
// serves as the "poll for the new daily dump": a not-yet-published day is a gap whose fetch
// 404s and is skipped, retried next tick.
type Reconciler interface {
	Sync(ctx context.Context, limit int) (int, error)
}

// Pruner drops rows older than the retention horizon (satisfied by *store.Store).
type Pruner interface {
	PruneOlderThan(ctx context.Context, retentionDays int) (int64, error)
}

type Scheduler struct {
	reconciler Reconciler
	pruner     Pruner
	cfg        config.Config
	log        *slog.Logger
}

func New(cfg config.Config, reconciler Reconciler, pruner Pruner, log *slog.Logger) *Scheduler {
	return &Scheduler{reconciler: reconciler, pruner: pruner, cfg: cfg, log: log}
}

// Run drives reconciliation and prune on their intervals until ctx is cancelled. Job
// failures are logged, not returned -- a transient fetch/DB error must not tear down the
// service (which errgroup would do on a returned error).
func (s *Scheduler) Run(ctx context.Context) error {
	reconcile := time.NewTicker(s.cfg.ReconcileInterval)
	defer reconcile.Stop()
	prune := time.NewTicker(s.cfg.PruneInterval)
	defer prune.Stop()

	s.log.Info("scheduler started",
		"reconcile_every", s.cfg.ReconcileInterval,
		"prune_every", s.cfg.PruneInterval,
		"retention_days", s.cfg.RetentionDays)

	for {
		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopped")
			return nil
		case <-reconcile.C:
			s.reconcile(ctx)
		case <-prune.C:
			s.prune(ctx)
		}
	}
}

func (s *Scheduler) reconcile(ctx context.Context) {
	n, err := s.reconciler.Sync(ctx, reconcileLimit)
	if err != nil {
		s.log.Error("reconcile failed", "err", err)
		return
	}
	if n > 0 {
		s.log.Info("reconciled", "days", n)
	}
}

func (s *Scheduler) prune(ctx context.Context) {
	n, err := s.pruner.PruneOlderThan(ctx, s.cfg.RetentionDays)
	if err != nil {
		s.log.Error("prune failed", "err", err)
		return
	}
	s.log.Info("pruned", "killmails", n)
}
