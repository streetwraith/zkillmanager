package ingest

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"zkillmanager/internal/zkill"
)

const (
	dumpSpacing = 10 * time.Second // be gentle on R2 between daily dumps
	dayLayout   = "2006-01-02"
)

// TotalsFetcher returns zkill's per-day killmail counts (history/totals.json), keyed by
// UTC day.
type TotalsFetcher interface {
	Totals(ctx context.Context) (map[time.Time]int, error)
}

// DumpProgressStore records, per day, zkill's total at our last pull of that day.
type DumpProgressStore interface {
	LoadDumpProgress(ctx context.Context) (map[time.Time]int, error)
	SetDumpProgress(ctx context.Context, day time.Time, total int) error
}

// Syncer keeps the local store in step with zKillboard's daily dumps by comparing
// totals.json against the totals recorded at our last pull and re-pulling the biggest-gap
// days. This unifies backfill (a fresh DB has every day at "full gap") and daily
// reconciliation (the few days that grew since, including late submissions on old days).
type Syncer struct {
	mu        sync.Mutex // serializes overlapping Sync calls (backfill vs scheduled reconcile)
	totals    TotalsFetcher
	fetcher   DayFetcher
	ingester  *Ingester
	progress  DumpProgressStore
	retention int
	log       *slog.Logger
}

func NewSyncer(totals TotalsFetcher, fetcher DayFetcher, ingester *Ingester, progress DumpProgressStore, retentionDays int, log *slog.Logger) *Syncer {
	return &Syncer{totals: totals, fetcher: fetcher, ingester: ingester, progress: progress, retention: retentionDays, log: log}
}

// Sync re-pulls the biggest-gap days within the retention window, up to limit (<= 0 =
// all; used for backfill). Returns the number of days pulled. Days whose dump isn't
// published yet are skipped and retried next run; other fetch errors stop the sync.
// Today is never pulled (its dump isn't final; the live tail covers it).
func (s *Syncer) Sync(ctx context.Context, limit int) (int, error) {
	// Don't let a scheduled reconcile pile up behind an in-progress backfill (or vice
	// versa); a skipped tick just runs next interval.
	if !s.mu.TryLock() {
		s.log.Info("sync already in progress, skipping")
		return 0, nil
	}
	defer s.mu.Unlock()

	totals, err := s.totals.Totals(ctx)
	if err != nil {
		return 0, err
	}
	recorded, err := s.progress.LoadDumpProgress(ctx)
	if err != nil {
		return 0, err
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	horizon := today.AddDate(0, 0, -s.retention)

	type gap struct {
		day   time.Time
		total int
		delta int
	}
	var gaps []gap
	for day, total := range totals {
		if day.Before(horizon) || !day.Before(today) {
			continue // outside the retention window, or today (still growing)
		}
		if delta := total - recorded[day]; delta > 0 {
			gaps = append(gaps, gap{day: day, total: total, delta: delta})
		}
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].delta > gaps[j].delta })
	if limit > 0 && len(gaps) > limit {
		gaps = gaps[:limit]
	}

	pulled := 0
	for _, g := range gaps {
		if err := ctx.Err(); err != nil {
			return pulled, err
		}
		n, err := s.ingester.Day(ctx, s.fetcher, g.day)
		switch {
		case errors.Is(err, zkill.ErrDumpNotFound):
			s.log.Info("dump not published yet, skipping", "day", g.day.Format(dayLayout))
		case err != nil:
			return pulled, err
		default:
			if err := s.progress.SetDumpProgress(ctx, g.day, g.total); err != nil {
				return pulled, err
			}
			s.log.Info("synced day", "day", g.day.Format(dayLayout), "killmails", n, "gap", g.delta)
			pulled++
		}

		// Space every remote attempt -- including a 404 skip -- so a run of missing
		// dumps can't burst requests at R2.
		select {
		case <-ctx.Done():
			return pulled, ctx.Err()
		case <-time.After(dumpSpacing):
		}
	}
	return pulled, nil
}
