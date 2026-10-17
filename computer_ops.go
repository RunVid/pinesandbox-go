package pinesandbox

import (
	"context"
	"encoding/json"
)

// Snapshot returns the most recently saved snapshot body, or (nil, nil) if none exists
// yet. The SDK does not decode the snapshot format.
func (c *Computer) Snapshot(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.LatestSnapshot(ctx, ct)
}

// Checkpoint saves the Computer's state now and waits for the save (the explicit
// durability primitive, not coupled to Stop). Returns the snapshot descriptor (raw;
// {skipped:true} when nothing changed since the last save).
func (c *Computer) Checkpoint(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.Capture(ctx, ct)
}

// LatestSnapshot returns the most recently saved snapshot body, or (nil, nil).
//
// Deprecated: use Snapshot.
func (c *Computer) LatestSnapshot(ctx context.Context) (json.RawMessage, error) {
	return c.Snapshot(ctx)
}

// Capture saves the Computer's state now and returns the snapshot descriptor (raw).
//
// Deprecated: use Checkpoint.
func (c *Computer) Capture(ctx context.Context) (json.RawMessage, error) {
	return c.Checkpoint(ctx)
}

// OrphanTabs lists the browser tabs no session owns, for example a window a page opened
// after its session closed (raw).
func (c *Computer) OrphanTabs(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ListOrphanTabs(ctx, ct)
}

// CloseOrphanTab closes one tab no session owns.
func (c *Computer) CloseOrphanTab(ctx context.Context, targetID string) error {
	coord, ct, err := c.bound()
	if err != nil {
		return err
	}
	return coord.CloseOrphanTab(ctx, ct, targetID)
}

// OrphanDownloads lists browser downloads no session has claimed (raw).
func (c *Computer) OrphanDownloads(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ListOrphanDownloads(ctx, ct)
}

// ListOrphanDownloads lists unclaimed browser downloads (raw).
//
// Deprecated: use OrphanDownloads.
func (c *Computer) ListOrphanDownloads(ctx context.Context) (json.RawMessage, error) {
	return c.OrphanDownloads(ctx)
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
