package zkill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/getsentry/sentry-go"
)

// Handler ingests a live killmail. Defined here (not imported from ingest) so the zkill
// package stays a leaf; ingest.Ingester satisfies it.
type Handler interface {
	Process(ctx context.Context, km Killmail) error
}

// SequenceStore persists the live-feed cursor (last processed R2Z2 sequence).
type SequenceStore interface {
	GetState(ctx context.Context, key string) (string, bool, error)
	SetState(ctx context.Context, key, value string) error
}

const (
	liveSeqKey     = "live_sequence"
	liveIdleSleep  = 6 * time.Second        // R2Z2 etiquette: >=6s once caught up
	liveReqDelay   = 100 * time.Millisecond // ~10 req/s, comfortably under the 15 req/s cap
	liveErrBackoff = 6 * time.Second
	liveGapJump    = 500 // if this far behind on a 404, jump to head (expired gap; dumps cover it)

	// liveStallThreshold is how many consecutive failed R2Z2 requests mean the feed is
	// stalled rather than blipping -- about a minute at liveErrBackoff.
	liveStallThreshold = 10
)

// Live tails zKillboard's R2Z2 ephemeral feed -- the replacement for the (sunset) RedisQ.
// It GETs /ephemeral/{seq}.json in order, ingesting each, and sleeps when caught up (a 404
// at the head). Position is persisted per killmail, so a restart resumes where it left off.
type Live struct {
	baseURL string
	ua      string
	handler Handler
	store   SequenceStore
	client  *http.Client
	// Failure streaks for error reporting. Run is the only writer, so no lock is needed.
	fetchFails   int
	ingestFailed bool
}

func NewLive(baseURL, userAgent string, handler Handler, store SequenceStore) *Live {
	return &Live{
		baseURL: baseURL,
		ua:      userAgent,
		handler: handler,
		store:   store,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (l *Live) Run(ctx context.Context) error {
	seq, err := l.startSequence(ctx)
	if err != nil {
		return fmt.Errorf("live: start sequence: %w", err)
	}
	slog.Info("live feed started (R2Z2)", "sequence", seq)

	for {
		if err := ctx.Err(); err != nil {
			slog.Info("live feed stopped")
			return nil
		}

		km, status, err := l.fetch(ctx, seq)
		switch {
		case err != nil:
			slog.Warn("live fetch error", "sequence", seq, "err", err)
			l.noteFetch(err)
			if !wait(ctx, liveErrBackoff) {
				return nil
			}
		case status == http.StatusOK:
			l.noteFetch(nil)
			perr := l.handler.Process(ctx, km)
			if perr != nil {
				slog.Error("live ingest error", "sequence", seq, "err", perr)
			}
			l.noteIngest(seq, perr)
			seq++
			l.saveSequence(ctx, seq)
			if !wait(ctx, liveReqDelay) {
				return nil
			}
		case status == http.StatusNotFound:
			if cont := l.onMissing(ctx, &seq); !cont {
				return nil
			}
		default:
			slog.Warn("live unexpected status", "sequence", seq, "status", status)
			l.noteFetch(fmt.Errorf("live: unexpected status %d", status))
			if !wait(ctx, liveErrBackoff) {
				return nil
			}
		}
	}
}

// onMissing handles a 404: caught up (sleep), a single missing sequence (skip), or a large
// expired gap (jump to head). Returns false if ctx was cancelled.
func (l *Live) onMissing(ctx context.Context, seq *int64) bool {
	latest, err := l.latest(ctx)
	l.noteFetch(err)
	if err != nil {
		slog.Warn("live sequence.json error", "err", err)
		return wait(ctx, liveErrBackoff)
	}
	switch {
	case *seq > latest:
		return wait(ctx, liveIdleSleep) // caught up
	case latest-*seq > liveGapJump:
		slog.Warn("live jumping past expired gap", "from", *seq, "to", latest, "skipped", latest-*seq)
		*seq = latest
		l.saveSequence(ctx, *seq)
		return true
	default:
		*seq++ // a single missing sequence within range
		return wait(ctx, liveReqDelay)
	}
}

// noteFetch records the outcome of one R2Z2 request. It counts consecutive failures and
// reports once when the run reaches liveStallThreshold. One failed request is normal -- the
// back-off handles it, so only a sustained stall needs a person.
func (l *Live) noteFetch(err error) {
	if err == nil {
		l.fetchFails = 0
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	l.fetchFails++
	if l.fetchFails != liveStallThreshold {
		return
	}
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("component", "live")
		sentry.CaptureException(fmt.Errorf("live feed stalled: %w", err))
	})
}

// noteIngest records the outcome of one ingest and reports the first failure of a streak. A
// broken store fails every sequence, and the loop runs about ten times a second, so one event
// per streak is the only useful volume.
func (l *Live) noteIngest(seq int64, err error) {
	if err == nil {
		l.ingestFailed = false
		return
	}
	if errors.Is(err, context.Canceled) || l.ingestFailed {
		return
	}
	l.ingestFailed = true
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("component", "live")
		scope.SetContext("live", sentry.Context{"sequence": seq})
		sentry.CaptureException(err)
	})
}

// startSequence resumes from the stored cursor, or starts at the current head.
func (l *Live) startSequence(ctx context.Context) (int64, error) {
	v, ok, err := l.store.GetState(ctx, liveSeqKey)
	if err != nil {
		return 0, err
	}
	if ok {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			return n, nil
		}
	}
	return l.latest(ctx)
}

func (l *Live) saveSequence(ctx context.Context, seq int64) {
	if err := l.store.SetState(ctx, liveSeqKey, strconv.FormatInt(seq, 10)); err != nil {
		slog.Warn("live: persist sequence failed", "err", err)
	}
}

func (l *Live) latest(ctx context.Context) (int64, error) {
	var body struct {
		Sequence int64 `json:"sequence"`
	}
	if err := httpGetJSON(ctx, l.client, l.ua, l.baseURL+"/sequence.json", maxMetaBytes, &body); err != nil {
		return 0, err
	}
	return body.Sequence, nil
}

// fetch GETs one ephemeral killmail. km is valid only when the returned status is 200.
func (l *Live) fetch(ctx context.Context, seq int64) (Killmail, int, error) {
	resp, err := httpGet(ctx, l.client, l.ua, fmt.Sprintf("%s/%d.json", l.baseURL, seq))
	if err != nil {
		return Killmail{}, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Killmail{}, resp.StatusCode, nil
	}
	// R2Z2 wraps the ESI killmail under "esi" (alongside hash, zkb, sequence_id, ...).
	var env struct {
		Esi Killmail `json:"esi"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxKillmailBytes)).Decode(&env); err != nil {
		return Killmail{}, resp.StatusCode, fmt.Errorf("decode: %w", err)
	}
	return env.Esi, http.StatusOK, nil
}

// wait sleeps d or until ctx is done; returns false if ctx was cancelled.
func wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
