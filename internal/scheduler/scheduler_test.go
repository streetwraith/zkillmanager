package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"zkillmanager/internal/config"
	"zkillmanager/internal/sentrytest"
)

type fakeReconciler struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeReconciler) Sync(context.Context, int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return 0, f.err
}

func (f *fakeReconciler) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

type fakePairer struct {
	mu    sync.Mutex
	calls int
}

func (f *fakePairer) PairPods(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return 0, nil
}

type fakePruner struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakePruner) PruneOlderThan(context.Context, int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return 0, f.err
}

func TestSchedulerRunsJobsOnSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &fakeReconciler{}
		pair := &fakePairer{}
		pr := &fakePruner{}
		cfg := config.Config{ReconcileInterval: time.Hour, PruneInterval: 24 * time.Hour, RetentionDays: 90}
		s := New(cfg, rec, pair, pr, slog.New(slog.NewTextHandler(io.Discard, nil)))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()

		time.Sleep(25 * time.Hour) // fake clock: ~25 reconciles, ~1 prune, many pod-pairing passes
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run returned %v", err)
		}

		rec.mu.Lock()
		defer rec.mu.Unlock()
		pair.mu.Lock()
		defer pair.mu.Unlock()
		pr.mu.Lock()
		defer pr.mu.Unlock()
		if rec.calls < 24 {
			t.Errorf("reconcile calls = %d, want >= 24", rec.calls)
		}
		if pair.calls < 24 {
			t.Errorf("pod-pairing calls = %d, want >= 24 (5-min interval over 25h)", pair.calls)
		}
		if pr.calls < 1 {
			t.Errorf("prune calls = %d, want >= 1", pr.calls)
		}
	})
}

func TestSchedulerReportsOncePerFailureStreak(t *testing.T) {
	sink := sentrytest.Capture(t)
	synctest.Test(t, func(t *testing.T) {
		rec := &fakeReconciler{err: errors.New("r2 unreachable")}
		cfg := config.Config{ReconcileInterval: time.Hour, PruneInterval: 24 * time.Hour, RetentionDays: 90}
		s := New(cfg, rec, &fakePairer{}, &fakePruner{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()

		time.Sleep(3 * time.Hour) // three failing reconciles
		synctest.Wait()
		if got := len(sink.Captured()); got != 1 {
			t.Fatalf("events after a 3-tick failure streak = %d, want 1", got)
		}

		rec.setErr(nil) // one success ends the streak
		time.Sleep(time.Hour)
		synctest.Wait()

		rec.setErr(errors.New("r2 unreachable"))
		time.Sleep(2 * time.Hour)
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run returned %v", err)
		}

		events := sink.Captured()
		if len(events) != 2 {
			t.Fatalf("events after a second streak = %d, want 2", len(events))
		}
		for i, e := range events {
			if e.Tags["component"] != "scheduler" || e.Tags["job"] != "reconcile" {
				t.Errorf("event %d tags = %v, want component=scheduler job=reconcile", i, e.Tags)
			}
		}
	})
}

func TestSchedulerDoesNotReportShutdown(t *testing.T) {
	sink := sentrytest.Capture(t)
	synctest.Test(t, func(t *testing.T) {
		rec := &fakeReconciler{err: context.Canceled}
		cfg := config.Config{ReconcileInterval: time.Hour, PruneInterval: 24 * time.Hour, RetentionDays: 90}
		s := New(cfg, rec, &fakePairer{}, &fakePruner{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()

		time.Sleep(2 * time.Hour)
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run returned %v", err)
		}

		if got := len(sink.Captured()); got != 0 {
			t.Errorf("events for a cancelled context = %d, want 0", got)
		}
	})
}

func TestSchedulerStreaksAreIndependentPerJob(t *testing.T) {
	sink := sentrytest.Capture(t)
	synctest.Test(t, func(t *testing.T) {
		rec := &fakeReconciler{err: errors.New("r2 unreachable")}
		pr := &fakePruner{err: errors.New("db unreachable")}
		cfg := config.Config{ReconcileInterval: time.Hour, PruneInterval: 24 * time.Hour, RetentionDays: 90}
		s := New(cfg, rec, &fakePairer{}, pr, slog.New(slog.NewTextHandler(io.Discard, nil)))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()

		time.Sleep(25 * time.Hour) // ~25 failing reconciles, 1 failing prune
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run returned %v", err)
		}

		jobs := map[string]int{}
		for _, e := range sink.Captured() {
			jobs[e.Tags["job"]]++
		}
		if jobs["reconcile"] != 1 || jobs["prune"] != 1 || len(jobs) != 2 {
			t.Errorf("events by job = %v, want exactly one reconcile and one prune", jobs)
		}
	})
}
