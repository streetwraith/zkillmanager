package ingest

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"zkillmanager/internal/zkill"
)

type fakeTotals struct{ m map[time.Time]int }

func (f fakeTotals) Totals(context.Context) (map[time.Time]int, error) { return f.m, nil }

type fakeProgress struct {
	mu sync.Mutex
	m  map[time.Time]int
}

func (p *fakeProgress) LoadDumpProgress(context.Context) (map[time.Time]int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make(map[time.Time]int, len(p.m))
	for k, v := range p.m {
		cp[k] = v
	}
	return cp, nil
}

func (p *fakeProgress) SetDumpProgress(_ context.Context, day time.Time, total int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[day] = total
	return nil
}

func (p *fakeProgress) get(day time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m[day]
}

// recordingFetcher records which days were fetched and returns an empty day (so the
// Ingester's Process never runs) -- these tests are about day selection, not ingest.
type recordingFetcher struct {
	notFound map[time.Time]bool
	fetched  []time.Time
}

func (f *recordingFetcher) FetchDay(_ context.Context, day time.Time) (map[int64]zkill.Killmail, error) {
	f.fetched = append(f.fetched, day)
	if f.notFound[day] {
		return nil, zkill.ErrDumpNotFound
	}
	return map[int64]zkill.Killmail{}, nil
}

type fakePodPairer struct {
	mu    sync.Mutex
	calls int
}

func (f *fakePodPairer) PairPods(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return 7, nil
}

func newSyncerFor(totals, recorded map[time.Time]int, fetcher *recordingFetcher, retention int) (*Syncer, *fakeProgress) {
	prog := &fakeProgress{m: recorded}
	ing := New(fakeResolver{ok: true}, &fakeWriter{}, discardLogger())
	return NewSyncer(fakeTotals{totals}, fetcher, ing, prog, &fakePodPairer{}, retention, discardLogger()), prog
}

func TestSyncSelectsBiggestGapsWithinWindow(t *testing.T) {
	// synctest bubble: fake clock makes the ~10s inter-day spacing instant and pins now().
	synctest.Test(t, func(t *testing.T) {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		day := func(n int) time.Time { return today.AddDate(0, 0, -n) }

		totals := map[time.Time]int{
			today:  500, // excluded: today is never pulled
			day(1): 100, // gap 100
			day(2): 100, // gap 60
			day(3): 100, // gap 0 -> not a candidate
			day(4): 100, // gap 30, but its dump 404s
			day(8): 100, // excluded: before the 7d horizon
		}
		recorded := map[time.Time]int{day(2): 40, day(3): 100, day(4): 70}
		fetcher := &recordingFetcher{notFound: map[time.Time]bool{day(4): true}}

		s, prog := newSyncerFor(totals, recorded, fetcher, 7)
		n, err := s.Sync(context.Background(), 0) // 0 = all gaps
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}

		// biggest-gap-first: day1 (100), day2 (60), day4 (30); day3 gap 0, day8 pre-horizon, today excluded.
		want := []time.Time{day(1), day(2), day(4)}
		if len(fetcher.fetched) != len(want) {
			t.Fatalf("fetched %v, want %v", fetcher.fetched, want)
		}
		for i := range want {
			if !fetcher.fetched[i].Equal(want[i]) {
				t.Errorf("fetched[%d] = %s, want %s", i, fetcher.fetched[i].Format("2006-01-02"), want[i].Format("2006-01-02"))
			}
		}
		if n != 2 {
			t.Errorf("pulled = %d, want 2 (day4 skipped: dump not found)", n)
		}
		// progress recorded for the two that succeeded; the 404 day is left untouched.
		if prog.get(day(1)) != 100 || prog.get(day(2)) != 100 {
			t.Errorf("progress not recorded for pulled days: day1=%d day2=%d", prog.get(day(1)), prog.get(day(2)))
		}
		if prog.get(day(4)) != 70 {
			t.Errorf("progress for the 404 day changed to %d, want 70 (unchanged)", prog.get(day(4)))
		}
	})
}

// PairPods must not run while a Sync holds the lock: a bulk day insert writes ship kills and
// pods in arbitrary order, so pairing concurrently could delete a pod before its ship kill
// lands. The pass skips and runs next tick instead.
func TestPairPodsSkipsWhileSyncing(t *testing.T) {
	pairer := &fakePodPairer{}
	ing := New(fakeResolver{ok: true}, &fakeWriter{}, discardLogger())
	s := NewSyncer(fakeTotals{}, &recordingFetcher{}, ing, &fakeProgress{m: map[time.Time]int{}}, pairer, 7, discardLogger())

	s.mu.Lock() // stand in for an in-progress Sync
	n, err := s.PairPods(context.Background())
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("PairPods while locked: %v", err)
	}
	if n != 0 {
		t.Errorf("pods paired = %d, want 0 (skipped)", n)
	}
	if pairer.calls != 0 {
		t.Errorf("underlying pairer called %d times while syncing, want 0", pairer.calls)
	}

	if _, err := s.PairPods(context.Background()); err != nil {
		t.Fatalf("PairPods unlocked: %v", err)
	}
	if pairer.calls != 1 {
		t.Errorf("underlying pairer called %d times when free, want 1", pairer.calls)
	}
}

func TestSyncRespectsLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		day := func(n int) time.Time { return today.AddDate(0, 0, -n) }

		totals := map[time.Time]int{day(1): 100, day(2): 100, day(3): 100} // gaps 100, 60, 20
		recorded := map[time.Time]int{day(2): 40, day(3): 80}
		fetcher := &recordingFetcher{}

		s, _ := newSyncerFor(totals, recorded, fetcher, 7)
		n, err := s.Sync(context.Background(), 1)
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if n != 1 || len(fetcher.fetched) != 1 || !fetcher.fetched[0].Equal(day(1)) {
			t.Fatalf("limit 1 should pull only the biggest gap (day1); got n=%d fetched=%v", n, fetcher.fetched)
		}
	})
}
