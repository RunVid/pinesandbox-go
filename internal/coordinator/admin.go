package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"go.pinesandbox.io/computer/internal/base/problem"
)

// Health returns the coord's health document (public, token-less).
func (c *Client) Health(ctx context.Context) (json.RawMessage, error) {
	return c.getJSON(ctx, "/health", "")
}

// LatestSnapshot returns the most-recently-persisted snapshot body (ct_), or (nil, nil) if
// none exists yet (404). The SDK does not decode the snapshot format. A 503 (state
// persistence not enabled) propagates as *problem.APIError.
func (c *Client) LatestSnapshot(ctx context.Context, token string) (json.RawMessage, error) {
	raw, err := c.getJSON(ctx, "/state", token)
	if err != nil {
		var ae *problem.APIError
		if errors.As(err, &ae) && ae.Status == 404 {
			return nil, nil
		}
		return nil, err
	}
	return raw, nil
}

// Capture triggers a synchronous durable checkpoint (ct_). Content-hash-gated: an unchanged
// state returns {skipped:true}. Returns the snapshot descriptor (raw).
func (c *Client) Capture(ctx context.Context, token string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/v1/coord/capture", token, map[string]any{})
}

// ListOrphanDownloads lists unclaimed downloads (ct_, raw).
func (c *Client) ListOrphanDownloads(ctx context.Context, token string) (json.RawMessage, error) {
	return c.getJSON(ctx, "/downloads/orphans", token)
}

// ClaimOrphanDownload attributes an orphan download to a session (ct_, raw).
func (c *Client) ClaimOrphanDownload(ctx context.Context, token, guid, sessionName, filename string) (json.RawMessage, error) {
	body := map[string]any{"session_name": sessionName}
	if filename != "" {
		body["filename"] = filename
	}
	return c.postJSON(ctx, "/downloads/orphans/"+url.PathEscape(guid)+"/claim", token, body)
}

// DiscardOrphanDownload drops an orphan download (ct_).
func (c *Client) DiscardOrphanDownload(ctx context.Context, token, guid string) error {
	_, err := c.do(ctx, "DELETE", "/downloads/orphans/"+url.PathEscape(guid), token, nil)
	return err
}

// Finish closes browser work and confirms its terminal save. A failed request
// must not fall back to ordinary capture or sandbox deletion.
func (c *Client) Finish(ctx context.Context, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	raw, err := c.postJSON(ctx, "/v1/coord/finish", token, map[string]any{})
	if err != nil {
		return err
	}
	var result struct {
		Saved bool `json:"saved"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if !result.Saved {
		return fmt.Errorf("stop did not confirm a complete save")
	}
	return nil
}
