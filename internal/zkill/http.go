package zkill

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Response-size caps: bound how much we decode from an external body so a compromised or
// misbehaving endpoint can't OOM the daemon. Generous vs. real payloads (a day dump is
// ~30 MB, totals.json ~0.1 MB, one killmail a few KB) -- these only stop a runaway.
const (
	maxMetaBytes     = 8 << 20   // sequence.json / totals.json
	maxKillmailBytes = 4 << 20   // one ephemeral killmail
	maxDumpBytes     = 256 << 20 // a full day's raw dump
)

// httpGet issues a GET carrying the descriptive User-Agent (zKillboard etiquette). The
// caller owns the response body and status handling.
func httpGet(ctx context.Context, client *http.Client, userAgent, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return client.Do(req)
}

// httpGetJSON GETs a URL and decodes a 200 response into v (reading at most maxBytes);
// any non-200 is an error.
func httpGetJSON(ctx context.Context, client *http.Client, userAgent, url string, maxBytes int64, v any) error {
	resp, err := httpGet(ctx, client, userAgent, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxBytes)).Decode(v)
}
