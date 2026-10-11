package pinesandbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.pinesandbox.io/computer/internal/base/wait"
	"go.pinesandbox.io/computer/internal/controlplane"
)

// sandboxControl is the control-plane subset a SandboxHandle needs (faked in tests).
type sandboxControl interface {
	Get(ctx context.Context, sandboxID string) (*controlplane.SandboxInfo, error)
	Destroy(ctx context.Context, sandboxID string) error
	Pause(ctx context.Context, sandboxID string) error
	Resume(ctx context.Context, sandboxID string) error
	Stop(ctx context.Context, sandboxID, operationID string) error
	Release(ctx context.Context, sandboxID, etag string) error
}

const (
	statusRunning        = "running"
	defaultPollInterval  = 2 * time.Second
	defaultTerminateWait = 90 * time.Second
	defaultMemoryWait    = 6 * time.Minute
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
// create-time Running status (a prewarmed Computer) skips the first poll.
func (h *SandboxHandle) WaitUntilRunning(ctx context.Context, timeout, interval time.Duration) error {
	if h.status == statusRunning {
		return nil
	}
	return h.waitUntilState(ctx, statusRunning, timeout, interval)
}

func (h *SandboxHandle) waitUntilState(ctx context.Context, target string, timeout, interval time.Duration) error {
	if interval <= 0 {
		interval = defaultPollInterval
	}
	deadline := h.clock().Add(timeout)
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !h.clock().Before(deadline) {
			return &ReadyTimeoutError{SandboxID: h.id, LastState: h.status}
		}
		state, err := h.refreshStatus(waitCtx)
		if err != nil {
			return readinessBudgetError(ctx, waitCtx, err, h)
		}
		if state == target {
			return nil
		}
		if terminalStates[state] || state == "unavailable" {
			return &SandboxFailedError{SandboxID: h.id, State: state}
		}
		if err := h.sleeper(waitCtx, max(0, min(interval, deadline.Sub(h.clock())))); err != nil {
			return readinessBudgetError(ctx, waitCtx, err, h)
		}
	}
}

func (h *SandboxHandle) transitionMemory(ctx context.Context, target string) error {
	deadline := h.clock().Add(defaultMemoryWait)
	operationCtx, cancel := context.WithTimeout(ctx, defaultMemoryWait)
	defer cancel()
	request := h.cp.Resume
	if target == "paused" {
		request = h.cp.Pause
	}
	err := request(operationCtx, h.id)
	var conflict *ConflictError
	if err != nil && !errors.As(err, &conflict) {
		return readinessBudgetError(ctx, operationCtx, err, h)
	}
	// Neither acceptance nor a pre-operation cached status proves completion.
	h.status = ""
	if conflict != nil {
		state, readErr := h.refreshStatus(operationCtx)
		if readErr != nil {
			return readinessBudgetError(ctx, operationCtx, readErr, h)
		}
		if state == target {
			return nil // legacy servers return 409 for an already completed operation
		}
		return err
	}
	err = h.waitUntilState(operationCtx, target, deadline.Sub(h.clock()), defaultPollInterval)
	return readinessBudgetError(ctx, operationCtx, err, h)
}

// Stop observes existing intent first, so a saved handle works on another backend.
func (h *SandboxHandle) Stop(ctx context.Context) (bool, error) {
	const timeout = 390 * time.Second
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline := h.clock().Add(timeout)
	info, err := h.cp.Get(waitCtx, h.id)
	var missing *NotFoundError
	if errors.As(err, &missing) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if computeReleased(info) {
		return true, nil
	}
	if info.Status == "save_failed" && !info.Retryable {
		return false, stopSaveError(info)
	}
	if info.Status != "saving" && info.Status != "stopping" {
		id, err := uuidV7()
		if err != nil {
			return false, err
		}
		if err := h.cp.Stop(waitCtx, h.id, id); err != nil {
			var conflict *ConflictError
			if !errors.As(err, &conflict) {
				return false, err
			}
			current, readErr := h.cp.Get(waitCtx, h.id)
			if readErr != nil {
				return false, readErr
			}
			if current.Status != "saving" && current.Status != "stopping" {
				return false, err
			}
		}
	}
	for {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if waitCtx.Err() != nil || !h.clock().Before(deadline) {
			return false, nil
		}
		info, err := h.cp.Get(waitCtx, h.id)
		if errors.As(err, &missing) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if computeReleased(info) {
			return true, nil
		}
		if info.Status == "save_failed" {
			return false, stopSaveError(info)
		}
		if err := h.sleeper(waitCtx, min(defaultPollInterval, deadline.Sub(h.clock()))); err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if waitCtx.Err() != nil {
				return false, nil
			}
			return false, err
		}
	}
}

func stopSaveError(info *controlplane.SandboxInfo) error {
	return &StopSaveError{Err: fmt.Errorf("runtime %s: %s", info.ID, info.Message), Retryable: info.Retryable}
}

func computeReleased(info *controlplane.SandboxInfo) bool {
	return info.Status == "terminated" && info.Reason == "COMPUTE_RELEASED"
}

func (h *SandboxHandle) releaseFailedRuntime(ctx context.Context, info *controlplane.SandboxInfo) (bool, error) {
	waitCtx, cancel := context.WithTimeout(ctx, defaultTerminateWait)
	defer cancel()
	if info.Status != "stopping" {
		if info.Reason == "CaptureTimeout" {
			return false, fmt.Errorf("pinesandbox: snapshot capture is still being observed; wait for it to settle")
		}
		switch info.Status {
		case "save_failed", "unavailable", "failed", "terminated":
		default:
			return false, fmt.Errorf("pinesandbox: runtime has no recorded failure; use Stop or Resume")
		}
		if info.ETag == "" {
			return false, fmt.Errorf("pinesandbox: server did not supply a conditional release version")
		}
		if err := h.cp.Release(waitCtx, h.id, info.ETag); err != nil {
			return false, err
		}
	}
	for {
		if err := waitCtx.Err(); err != nil {
			return false, err
		}
		if h.gone(waitCtx) {
			return true, nil
		}
		if err := h.sleeper(waitCtx, defaultPollInterval); err != nil {
			return false, err
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
	deadline := h.clock().Add(waitTimeout)
	waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	if err := h.cp.Destroy(waitCtx, h.id); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if waitCtx.Err() != nil {
			return false, nil
		}
		return false, err
	}
	for {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		remaining := deadline.Sub(h.clock())
		if remaining <= 0 || waitCtx.Err() != nil {
			return false, nil
		}
		if h.gone(waitCtx) {
			return true, nil
		}
		remaining = deadline.Sub(h.clock())
		if remaining <= 0 {
			return false, nil
		}
		if err := h.sleeper(waitCtx, min(interval, remaining)); err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if waitCtx.Err() != nil {
				return false, nil
			}
			return false, err
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
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	info, err := h.cp.Get(ctx, h.id)
	var nf *NotFoundError
	return errors.As(err, &nf) || (err == nil && computeReleased(info))
}
