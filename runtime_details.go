package pinesandbox

import (
	"context"
	"fmt"
	"time"

	"go.pinesandbox.io/computer/internal/controlplane"
)

// RuntimeLifetime is the admitted idle policy and scheduled Stop for one runtime.
// Nil on RuntimeDetails means a legacy runtime without the new policy.
type RuntimeLifetime = controlplane.Lifetime

// RuntimeDetails observes one runtime. Missing policy or timestamps remain nil.
type RuntimeDetails struct {
	RuntimeRevision string
	Lifecycle       *RuntimeLifetime
	ID              string
	State           string
	Reason          string
	Message         string
	Retryable       *bool // nil means the server supplied no retry verdict
	OperationID     string
	CreatedAt       *time.Time
	ExpiresAt       *time.Time
}

// RuntimeDetails reads this binding directly through the lifecycle API, even
// while paused. Absence and transport errors never trigger replacement.
func (c *Computer) RuntimeDetails(ctx context.Context) (*RuntimeDetails, error) {
	c.mu.Lock()
	sandbox := c.sandbox
	c.mu.Unlock()
	if sandbox == nil {
		return nil, fmt.Errorf("pinesandbox: Computer has no runtime binding")
	}
	info, err := sandbox.cp.Get(ctx, sandbox.id)
	if err != nil {
		return nil, err
	}
	return runtimeDetails(info), nil
}

func runtimeDetails(info *controlplane.SandboxInfo) *RuntimeDetails {
	return &RuntimeDetails{RuntimeRevision: info.RuntimeRevision, Lifecycle: info.Lifecycle, ID: info.ID, State: info.Status, Reason: info.Reason,
		Message: info.Message, Retryable: info.Retryable, OperationID: info.OperationID,
		CreatedAt: info.CreatedAt, ExpiresAt: info.ExpiresAt}
}

// SetIdleTimeout changes this runtime's idle window. The server applies it
// atomically, or returns a conflict. An unchanged value is not a heartbeat.
// If the outcome is uncertain, inspect RuntimeDetails before another edit.
func (c *Computer) SetIdleTimeout(ctx context.Context, timeout time.Duration) (*RuntimeDetails, error) {
	c.mu.Lock()
	sandbox := c.sandbox
	c.mu.Unlock()
	if sandbox == nil {
		return nil, fmt.Errorf("pinesandbox: Computer has no runtime binding")
	}
	if timeout < time.Minute || timeout%time.Second != 0 {
		return nil, fmt.Errorf("pinesandbox: idle timeout must be whole seconds and at least one minute")
	}
	observed, err := sandbox.cp.Get(ctx, sandbox.id)
	if err != nil {
		return nil, err
	}
	if observed.Lifecycle == nil {
		return nil, fmt.Errorf("pinesandbox: runtime has no admitted lifetime policy; choose idle timeout on the next Start")
	}
	updated, err := sandbox.cp.SetIdleTimeout(ctx, sandbox.id, int(timeout/time.Second), observed.ETag)
	if err != nil {
		return nil, err
	}
	return runtimeDetails(updated), nil
}
