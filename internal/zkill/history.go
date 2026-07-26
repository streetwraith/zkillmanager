package zkill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrDumpNotFound means a day's raw dump isn't published yet (404) -- distinct from a
// real fetch failure, so callers can skip and retry later.
var ErrDumpNotFound = errors.New("zkill: dump not found")

// History fetches the daily R2 raw dumps used for backfill and catch-up.
type History struct {
	baseURL   string
	userAgent string
	client    *http.Client
}

func NewHistory(baseURL, userAgent string) *History {
	return &History{
		baseURL:   baseURL,
		userAgent: userAgent,
		client:    &http.Client{Timeout: 2 * time.Minute},
	}
}

// FetchDay returns every killmail for the given UTC day, keyed by killmail_id.
// The dump is a single JSON object {"<id>": {killmail}, ...}.
func (h *History) FetchDay(ctx context.Context, day time.Time) (map[int64]Killmail, error) {
	url := fmt.Sprintf("%s/raw/%s.json", h.baseURL, day.UTC().Format("20060102"))
	resp, err := httpGet(ctx, h.client, h.userAgent, url)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrDumpNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: unexpected status %s", url, resp.Status)
	}

	var out map[int64]Killmail
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxDumpBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode %s: %w", url, err)
	}
	return out, nil
}

// Totals fetches history/totals.json (zkill's per-day killmail counts), keyed by UTC day.
func (h *History) Totals(ctx context.Context) (map[time.Time]int, error) {
	var raw map[string]int
	if err := httpGetJSON(ctx, h.client, h.userAgent, h.baseURL+"/totals.json", maxMetaBytes, &raw); err != nil {
		return nil, err
	}
	out := make(map[time.Time]int, len(raw))
	for k, v := range raw {
		day, err := time.Parse("20060102", k)
		if err != nil {
			continue // ignore any non-date keys
		}
		out[day] = v
	}
	return out, nil
}
