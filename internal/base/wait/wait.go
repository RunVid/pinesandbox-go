// Package wait provides a context-cancellable sleep — the cancellation point for the SDK's
// retry/poll loops (bind handshake, token mint, sandbox readiness). A bare time.Sleep keeps
// a loop running for its full local budget after the caller's context is already cancelled;
// Sleep returns ctx.Err() the instant the context is done instead. Generic base primitive
// (Computer-agnostic, stdlib only — must not import a domain package, §3).
package wait

import (
	"context"
	"time"
)

// Sleep blocks for d, returning early with ctx.Err() if ctx is cancelled first. A
// non-positive d does not block but still surfaces an already-cancelled context (returns
// ctx.Err(), which is nil when the context is live).
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
