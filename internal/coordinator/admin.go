package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"go.pinesandbox.io/computer/internal/base/problem"
)

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

// ListOrphanTabs lists the browser tabs no session owns (ct_, raw).
func (c *Client) ListOrphanTabs(ctx context.Context, token string) (json.RawMessage, error) {
	return c.getJSON(ctx, "/orphans", token)
}

// CloseOrphanTab closes one tab no session owns (ct_).
func (c *Client) CloseOrphanTab(ctx context.Context, token, targetID string) error {
	_, err := c.do(ctx, "DELETE", "/orphans/"+url.PathEscape(targetID), token, nil)
	return err
}
