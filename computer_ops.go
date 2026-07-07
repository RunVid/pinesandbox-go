package pinesandbox

import (
	"context"
	"encoding/json"
)

// Health returns the bound coord's health document (token-less route).
func (c *Computer) Health(ctx context.Context) (json.RawMessage, error) {
	coord, _, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.Health(ctx)
}

// Metrics returns the bound coord's Prometheus metrics (token-less, text/plain).
//
// Deprecated: pre-gateway operator convenience. The gateway blocks /metrics on
// the public hosts, so this only works with direct in-cluster/local
// addressing; fleet metrics live on the platform telemetry pipeline (SigNoz)
// and per-Computer debugging goes through `./pine debug`. Kept for
// compatibility; do not build new tooling on it.
func (c *Computer) Metrics(ctx context.Context) ([]byte, error) {
	coord, _, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.Metrics(ctx)
}

// LatestSnapshot returns the most-recently-persisted snapshot body, or (nil, nil) if none
// exists yet. The SDK does not decode the snapshot format.
func (c *Computer) LatestSnapshot(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.LatestSnapshot(ctx, ct)
}

// Capture triggers a synchronous durable checkpoint (the explicit durability primitive, not
// coupled to Stop). Returns the snapshot descriptor (raw; {skipped:true} when unchanged).
func (c *Computer) Capture(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.Capture(ctx, ct)
}

// ListOrphanDownloads lists unclaimed browser downloads (raw).
func (c *Computer) ListOrphanDownloads(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ListOrphanDownloads(ctx, ct)
}

// ClaimOrphanDownload attributes an orphan download to a session (raw).
func (c *Computer) ClaimOrphanDownload(ctx context.Context, guid, sessionName, filename string) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ClaimOrphanDownload(ctx, ct, guid, sessionName, filename)
}

// DiscardOrphanDownload drops an orphan download.
func (c *Computer) DiscardOrphanDownload(ctx context.Context, guid string) error {
	coord, ct, err := c.bound()
	if err != nil {
		return err
	}
	return coord.DiscardOrphanDownload(ctx, ct, guid)
}
