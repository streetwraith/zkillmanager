package scheduler

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"zkillmanager/internal/config"
)

type fakeReconciler struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeReconciler) Sync(context.Context, int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return 0, nil
}

type fakePruner struct {
	mu    sync.Mutex
	calls int
}

func (f *fakePruner) PruneOlderThan(context.Context, int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return 0, nil
}

func TestSchedulerRunsJobsOnSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &fakeReconciler{}
		pr := &fakePruner{}
		cfg := config.Config{ReconcileInterval: time.Hour, PruneInterval: 24 * time.Hour, RetentionDays: 90}
		s := New(cfg, rec, pr, slog.New(slog.NewTextHandler(io.Discard, nil)))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()

		time.Sleep(25 * time.Hour) // fake clock: ~25 reconciles, ~1 prune
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run returned %v", err)
		}

		rec.mu.Lock()
		defer rec.mu.Unlock()
		pr.mu.Lock()
		defer pr.mu.Unlock()
		if rec.calls < 24 {
			t.Errorf("reconcile calls = %d, want >= 24", rec.calls)
		}
		if pr.calls < 1 {
			t.Errorf("prune calls = %d, want >= 1", pr.calls)
		}
	})
}
