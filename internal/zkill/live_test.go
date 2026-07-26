package zkill

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type memStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memStore) GetState(_ context.Context, k string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok, nil
}

func (s *memStore) SetState(_ context.Context, k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[k] = v
	return nil
}

func (s *memStore) get(k string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[k]
}

type chanHandler struct{ ch chan Killmail }

func (h chanHandler) Process(_ context.Context, km Killmail) error {
	h.ch <- km
	return nil
}

// R2Z2 test server: sequence.json returns `latest`; ephemeral files present in `kills`
// return the killmail under "esi"; everything else is 404.
func r2z2Server(latest int64, kills map[int64]int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sequence.json" {
			_ = json.NewEncoder(w).Encode(map[string]int64{"sequence": latest})
			return
		}
		var seq int64
		if _, err := fmt.Sscanf(r.URL.Path, "/%d.json", &seq); err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		id, ok := kills[seq]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"killmail_id": id, "sequence_id": seq, "zkb": map[string]any{"npc": false},
			"esi": map[string]any{
				"killmail_id":     id,
				"solar_system_id": 30000142,
				"victim":          map[string]any{"character_id": 7},
				"attackers":       []map[string]any{{"character_id": 42, "final_blow": true}},
			},
		})
	}))
}

func TestLiveProcessesSequentiallyAndPersists(t *testing.T) {
	srv := r2z2Server(102, map[int64]int64{100: 1000, 101: 1001, 102: 1002})
	defer srv.Close()

	store := &memStore{m: map[string]string{"live_sequence": "100"}} // resume from 100
	h := chanHandler{ch: make(chan Killmail, 10)}
	live := NewLive(srv.URL, "test-ua", h, store)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- live.Run(ctx) }()

	var got []int64
	deadline := time.After(5 * time.Second)
	for len(got) < 3 {
		select {
		case km := <-h.ch:
			got = append(got, km.KillmailID)
		case <-deadline:
			t.Fatalf("timeout; processed %v", got)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}

	if got[0] != 1000 || got[1] != 1001 || got[2] != 1002 {
		t.Errorf("processed ids = %v, want [1000 1001 1002]", got)
	}
	if s := store.get("live_sequence"); s != "103" {
		t.Errorf("persisted sequence = %q, want 103 (advanced past the last processed)", s)
	}
}

func TestLiveStartsAtHeadWithNoCursor(t *testing.T) {
	// No stored cursor -> start at sequence.json head (500); 500 is a 404 (nothing new yet)
	// -> caught up. Nothing should be processed; cancel unblocks the idle sleep.
	srv := r2z2Server(500, map[int64]int64{})
	defer srv.Close()

	store := &memStore{}
	h := chanHandler{ch: make(chan Killmail, 1)}
	live := NewLive(srv.URL, "test-ua", h, store)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- live.Run(ctx) }()

	select {
	case km := <-h.ch:
		t.Fatalf("should not have processed anything, got %d", km.KillmailID)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func TestLiveSkipsMissingSequence(t *testing.T) {
	// seq 100 is a hole (404), 101 exists; head is 101. The feed must skip 100 and go on,
	// not stall on the missing sequence.
	srv := r2z2Server(101, map[int64]int64{101: 2001})
	defer srv.Close()

	store := &memStore{m: map[string]string{"live_sequence": "100"}}
	h := chanHandler{ch: make(chan Killmail, 4)}
	live := NewLive(srv.URL, "test-ua", h, store)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- live.Run(ctx) }()

	select {
	case km := <-h.ch:
		if km.KillmailID != 2001 {
			t.Fatalf("processed %d, want 2001 (should have skipped the missing 100)", km.KillmailID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out — feed stalled on the missing sequence instead of skipping it")
	}
	cancel()
	<-done
}

func TestLiveJumpsPastExpiredGap(t *testing.T) {
	// Cursor is ~99900 behind the head with no fetchable files -> the expired-gap jump
	// should fast-forward the cursor to the head rather than crawling one 404 at a time.
	srv := r2z2Server(100000, map[int64]int64{})
	defer srv.Close()

	store := &memStore{m: map[string]string{"live_sequence": "100"}}
	live := NewLive(srv.URL, "test-ua", chanHandler{ch: make(chan Killmail, 1)}, store)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- live.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for store.get("live_sequence") != "100000" {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("cursor did not jump to head; got %q", store.get("live_sequence"))
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	cancel()
	<-done
}
