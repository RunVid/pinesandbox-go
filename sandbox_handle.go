package pinesandbox

import (
	"context"
	"errors"
	"time"

	"go.pinesandbox.io/computer/internal/base/wait"
	"go.pinesandbox.io/computer/internal/controlplane"
)

// sandboxControl is the control-plane subset a SandboxHandle needs (faked in tests).
type sandboxControl interface {
	Get(ctx context.Context, sandboxID string) (*controlplane.SandboxInfo, error)
	Destroy(ctx context.Context, sandboxID string) error
}

const (
	statusRunning        = "running"
	defaultPollInterval  = 2 * time.Second
	defaultTerminateWait = 90 * time.Second
)

var (
	terminalStates = map[string]bool{"failed": true, "terminated": true}
	liveStates     = map[string]bool{"running": true, "pending": true, "stopping": true}
)

// SandboxHandle is the lifecycle handle for a provisioned pod, backed by the control plane.
// A powered-on Computer holds one; it carries no data-plane knowledge (the coordinator host
// is derived from the zone).
type SandboxHandle struct {
	cp      sandboxControl
	id      string
	status  string
	clock   func() time.Time
	sleeper func(context.Context, time.Duration) error
}

func newSandboxHandle(cp sandboxControl, sandboxID, status string) *SandboxHandle {
	return &SandboxHandle{cp: cp, id: sandboxID, status: status, clock: time.Now, sleeper: wait.Sleep}
}

// ID is the sandbox id.
func (h *SandboxHandle) ID() string { return h.id }

// WaitUntilRunning blocks until the pod reports Running, returning *SandboxFailedError on a
// terminal state or *ReadyTimeoutError if it never becomes Ready within timeout. A
// create-time Running status (warm-pool claim) skips the first poll.
func (h *SandboxHandle) WaitUntilRunning(ctx context.Context, timeout, interval time.Duration) error {
	if h.status == statusRunning {
		return nil
	}
	if interval <= 0 {
		interval = defaultPollInterval
	}
	deadline := h.clock().Add(timeout)
	for {
		state, err := h.refreshStatus(ctx)
		if err != nil {
			return err
		}
		if state == statusRunning {
			return nil
		}
		if terminalStates[state] {
			return &SandboxFailedError{SandboxID: h.id, State: state}
		}
		if !h.clock().Before(deadline) {
			return &ReadyTimeoutError{SandboxID: h.id, LastState: state}
		}
		if err := h.sleeper(ctx, interval); err != nil {
			return err // context cancelled mid-poll
		}
	}
}

// Terminate gracefully deletes the pod and waits until the Sandbox record is confirmed gone
// (GET 404) or waitTimeout elapses. true = the record is gone (note: the pod's SIGTERM
// final capture may still be draining); false = delete issued but not confirmed gone.
func (h *SandboxHandle) Terminate(ctx context.Context, waitTimeout, interval time.Duration) (bool, error) {
	if waitTimeout <= 0 {
		waitTimeout = defaultTerminateWait
	}
	if interval <= 0 {
		interval = defaultPollInterval
	}
	if err := h.cp.Destroy(ctx, h.id); err != nil {
		return false, err
	}
	deadline := h.clock().Add(waitTimeout)
	for {
		if h.gone(ctx) {
			return true, nil
		}
		if !h.clock().Before(deadline) {
			return false, nil
		}
		if err := h.sleeper(ctx, interval); err != nil {
			return false, err // context cancelled mid-poll
		}
	}
}

// Kill is an ungraceful best-effort delete (no wait); it never returns an error.
func (h *SandboxHandle) Kill(ctx context.Context) bool {
	return h.cp.Destroy(ctx, h.id) == nil
}

// Alive reports whether the pod is in a live state (running/pending/stopping).
func (h *SandboxHandle) Alive(ctx context.Context) bool {
	state, err := h.refreshStatus(ctx)
	if err != nil {
		return false
	}
	return liveStates[state]
}

func (h *SandboxHandle) refreshStatus(ctx context.Context) (string, error) {
	info, err := h.cp.Get(ctx, h.id)
	if err != nil {
		return "", err
	}
	h.status = info.Status
	return info.Status, nil
}

// gone reports whether the Sandbox record is confirmed deleted (a 404). Any other outcome
// (still present, or a transient error) is treated as not-yet-gone.
func (h *SandboxHandle) gone(ctx context.Context) bool {
	_, err := h.cp.Get(ctx, h.id)
	var nf *NotFoundError
	return errors.As(err, &nf)
}
