// Package scheduler runs the app's internal periodic jobs: dump reconciliation, pod
// pairing, and retention prune.
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

// podPairInterval is how often the pod-pairing pass runs. Pods become eligible 15 min after
// the kill, so a few minutes is frequent enough to keep unpaired pods (and their disk use)
// low without pulling the full table often.
const podPairInterval = 5 * time.Minute

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

// PodPairer pairs pods with the ship kill they followed and deletes them (satisfied by
// *ingest.Syncer, which serializes it against dump ingest).
type PodPairer interface {
	PairPods(ctx context.Context) (int, error)
}

type Scheduler struct {
	reconciler Reconciler
	pairer     PodPairer
	pruner     Pruner
	cfg        config.Config
	log        *slog.Logger
}

func New(cfg config.Config, reconciler Reconciler, pairer PodPairer, pruner Pruner, log *slog.Logger) *Scheduler {
	return &Scheduler{reconciler: reconciler, pairer: pairer, pruner: pruner, cfg: cfg, log: log}
}

// Run drives reconciliation and prune on their intervals until ctx is cancelled. Job
// failures are logged, not returned -- a transient fetch/DB error must not tear down the
// service (which errgroup would do on a returned error).
func (s *Scheduler) Run(ctx context.Context) error {
	reconcile := time.NewTicker(s.cfg.ReconcileInterval)
	defer reconcile.Stop()
	pairPods := time.NewTicker(podPairInterval)
	defer pairPods.Stop()
	prune := time.NewTicker(s.cfg.PruneInterval)
	defer prune.Stop()

	s.log.Info("scheduler started",
		"reconcile_every", s.cfg.ReconcileInterval,
		"pair_pods_every", podPairInterval,
		"prune_every", s.cfg.PruneInterval,
		"retention_days", s.cfg.RetentionDays)

	for {
		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopped")
			return nil
		case <-reconcile.C:
			s.reconcile(ctx)
		case <-pairPods.C:
			s.pairPods(ctx)
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

func (s *Scheduler) pairPods(ctx context.Context) {
	n, err := s.pairer.PairPods(ctx)
	if err != nil {
		s.log.Error("pod pairing failed", "err", err)
		return
	}
	if n > 0 {
		s.log.Info("paired pods", "deleted", n)
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
