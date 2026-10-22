package pinesandbox

import (
	"context"
	"fmt"
	"time"

	"go.pinesandbox.io/computer/internal/controlplane"
)

// SandboxLifetime is the idle policy and scheduled Stop a Computer was started with.
// Nil on SandboxDetails means the Computer was started without a lifetime policy.
type SandboxLifetime = controlplane.Lifetime

// SandboxDetails describes the Computer sandbox the binding names. Missing policy or
// timestamps remain nil.
type SandboxDetails struct {
	RuntimeRevision string
	Lifecycle       *SandboxLifetime
	ID              string
	State           string
	Reason          string
	Message         string
	Retryable       *bool // nil means the server supplied no retry verdict
	OperationID     string
	CreatedAt       *time.Time
	ExpiresAt       *time.Time
}

// SandboxDetails reads this binding directly through the lifecycle API, even
// while paused. Absence and transport errors never trigger replacement.
func (c *Computer) SandboxDetails(ctx context.Context) (*SandboxDetails, error) {
	c.mu.Lock()
	sandbox := c.sandbox
	c.mu.Unlock()
	if sandbox == nil {
		return nil, ErrNotBound
	}
	info, err := sandbox.cp.Get(ctx, sandbox.id)
	if err != nil {
		return nil, err
	}
	return sandboxDetails(info), nil
}

func sandboxDetails(info *controlplane.SandboxInfo) *SandboxDetails {
	return &SandboxDetails{RuntimeRevision: info.RuntimeRevision, Lifecycle: info.Lifecycle, ID: info.ID, State: info.Status, Reason: info.Reason,
		Message: info.Message, Retryable: info.Retryable, OperationID: info.OperationID,
		CreatedAt: info.CreatedAt, ExpiresAt: info.ExpiresAt}
}

// SetIdleTimeout changes the running Computer's idle window. The server applies it
// atomically, or returns a conflict. An unchanged value is not a heartbeat.
// If the outcome is uncertain, inspect SandboxDetails before another edit.
func (c *Computer) SetIdleTimeout(ctx context.Context, timeout time.Duration) (*SandboxDetails, error) {
	c.mu.Lock()
	sandbox := c.sandbox
	c.mu.Unlock()
	if sandbox == nil {
		return nil, ErrNotBound
	}
	if timeout < time.Minute || timeout%time.Second != 0 {
		return nil, fmt.Errorf("pinesandbox: idle timeout must be whole seconds and at least one minute")
	}
	observed, err := sandbox.cp.Get(ctx, sandbox.id)
	if err != nil {
		return nil, err
	}
	if observed.Lifecycle == nil {
		return nil, ErrIdleTimeoutUnsupported
	}
	updated, err := sandbox.cp.SetIdleTimeout(ctx, sandbox.id, int(timeout/time.Second), observed.ETag)
	if err != nil {
		return nil, err
	}
	return sandboxDetails(updated), nil
}
