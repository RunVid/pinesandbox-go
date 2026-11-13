package pinesandbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.pinesandbox.io/computer/internal/base/wait"
	"go.pinesandbox.io/computer/internal/controlplane"
)

// sandboxControl is the control-plane subset a SandboxHandle needs (faked in tests).
type sandboxControl interface {
	SetIdleTimeout(context.Context, string, int, string) (*controlplane.SandboxInfo, error)
	Get(ctx context.Context, sandboxID string) (*controlplane.SandboxInfo, error)
	WaitForChange(ctx context.Context, sandboxID string, previous *controlplane.SandboxInfo) (*controlplane.SandboxInfo, error)
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
	liveStates = map[string]bool{"running": true, "pending": true, "stopping": true}
)

// SandboxHandle is the lifecycle handle for a started Computer. An attached Computer
// holds one; it only starts, observes and shuts the Computer down.
type SandboxHandle struct {
	cp      sandboxControl
	id      string
	status  string
	reason  string
	clock   func() time.Time
	sleeper func(context.Context, time.Duration) error
}

func newSandboxHandle(cp sandboxControl, sandboxID, status string) *SandboxHandle {
	return &SandboxHandle{cp: cp, id: sandboxID, status: status, clock: time.Now, sleeper: wait.Sleep}
}

// ID is the sandbox id.
func (h *SandboxHandle) ID() string { return h.id }

// WaitUntilRunning blocks until the Computer reports Running, returning *SandboxFailedError on a
// terminal state or *ReadyTimeoutError if it never becomes Ready within timeout. A
// create-time Running status (a prewarmed Computer) skips the first poll.
func (h *SandboxHandle) WaitUntilRunning(ctx context.Context, timeout, interval time.Duration) error {
	return h.waitUntilRunning(ctx, newRuntimeReady(h.clock()), timeout, interval)
}

func (h *SandboxHandle) waitUntilRunning(ctx context.Context, w readyWait, timeout, interval time.Duration) error {
	if decideNewRuntime(h.status) == newRuntimeDone {
		return nil
	}
	return h.waitUntilState(ctx, w, timeout, interval)
}

func (h *SandboxHandle) waitUntilState(ctx context.Context, w readyWait, timeout, interval time.Duration) error {
	if interval <= 0 {
		interval = defaultPollInterval
	}
	deadline := h.clock().Add(timeout)
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var info *controlplane.SandboxInfo
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !h.clock().Before(deadline) {
			return h.readyTimeout(w)
		}
		current, delay, err := h.observe(waitCtx, info, interval)
		if err != nil {
			return readinessBudgetError(ctx, waitCtx, err, h, w)
		}
		info = current
		state := info.Status
		h.status, h.reason = state, info.Reason
		done := state == w.target
		if w.newRuntime {
			done = decideNewRuntime(state) == newRuntimeDone
		}
		if done {
			return nil
		}
		if decideNewRuntime(state) == newRuntimeFail {
			return &SandboxFailedError{SandboxID: h.id, State: state, Reason: info.Reason, wait: w}
		}
		if delay > 0 {
			if err := h.sleeper(waitCtx, max(0, min(delay, deadline.Sub(h.clock())))); err != nil {
				return readinessBudgetError(ctx, waitCtx, err, h, w)
			}
		}
	}
}

func (h *SandboxHandle) readyTimeout(w readyWait) *ReadyTimeoutError {
	return &ReadyTimeoutError{SandboxID: h.id, LastState: h.status, LastReason: h.reason, Waited: h.clock().Sub(w.started), wait: w}
}

func (h *SandboxHandle) transitionMemory(ctx context.Context, target string) error {
	w := readyWait{target: target, started: h.clock()}
	deadline := w.started.Add(defaultMemoryWait)
	operationCtx, cancel := context.WithTimeout(ctx, defaultMemoryWait)
	defer cancel()
	request := h.cp.Resume
	if target == "paused" {
		request = h.cp.Pause
	}
	err := request(operationCtx, h.id)
	var conflict *ConflictError
	if err != nil && !errors.As(err, &conflict) {
		return readinessBudgetError(ctx, operationCtx, err, h, w)
	}
	// Neither acceptance nor a pre-operation cached status proves completion.
	h.status, h.reason = "", ""
	if conflict != nil {
		state, readErr := h.refreshStatus(operationCtx)
		if readErr != nil {
			return readinessBudgetError(ctx, operationCtx, readErr, h, w)
		}
		if state == target {
			return nil // legacy servers return 409 for an already completed operation
		}
		return err
	}
	err = h.waitUntilState(operationCtx, w, deadline.Sub(h.clock()), defaultPollInterval)
	return readinessBudgetError(ctx, operationCtx, err, h, w)
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
	if info.Status == "save_failed" && (info.Retryable == nil || !*info.Retryable) {
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
		current, delay, err := h.observe(waitCtx, info, defaultPollInterval)
		if errors.As(err, &missing) {
			return true, nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if waitCtx.Err() != nil || !h.clock().Before(deadline) {
				return false, nil
			}
			return false, err
		}
		info = current
		if computeReleased(info) {
			return true, nil
		}
		if info.Status == "save_failed" {
			return false, stopSaveError(info)
		}
		if delay > 0 {
			if err := h.sleeper(waitCtx, min(delay, deadline.Sub(h.clock()))); err != nil {
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
}

func stopSaveError(info *controlplane.SandboxInfo) error {
	return &StopSaveError{Err: fmt.Errorf("Computer sandbox %s: %s", info.ID, info.Message), Retryable: info.Retryable != nil && *info.Retryable}
}

func computeReleased(info *controlplane.SandboxInfo) bool {
	return info.Status != "" && decideAdmission(admissionStart, info.Status, info.Reason).Action == admissionProceed
}

// admit applies the admission table's column to the current runtime (info nil: none)
// and to each new observation while it says wait, until deadline. A release is a
// DELETE fenced by the observed ETag, which discards retained memory.
func (h *SandboxHandle) admit(ctx context.Context, column admissionColumn, info *controlplane.SandboxInfo, w readyWait, deadline time.Time) error {
	w.column = column
	waitCtx, cancel := context.WithTimeout(ctx, deadline.Sub(h.clock()))
	defer cancel()
	released := ""
	var missing *NotFoundError
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, reason := "", ""
		if info != nil {
			state, reason = info.Status, info.Reason
			h.status, h.reason = state, reason
		}
		decision := decideAdmission(column, state, reason)
		switch decision.Action {
		case admissionProceed:
			return nil
		case admissionRefuse:
			if decision.Refusal == admissionRefusalAlreadyStarted {
				return &ComputerAlreadyStartedError{SandboxID: h.id, State: state, Reason: reason, column: column}
			}
			return &SandboxRetainedError{SandboxID: h.id, State: state, Reason: reason, column: column}
		case admissionRelease:
			if info.ETag == "" {
				return errors.New(runtimeMessage(pickMessage(admissionMessageNoVersion, ""), runtimeText{sandboxID: h.id}))
			}
			// Release each observed version once; the wait below sees it go.
			if info.ETag != released {
				if err := h.cp.Release(waitCtx, h.id, info.ETag); err != nil {
					return readinessBudgetError(ctx, waitCtx, err, h, w)
				}
				released = info.ETag
			}
		}
		if !h.clock().Before(deadline) {
			return h.readyTimeout(w)
		}
		current, delay, err := h.observe(waitCtx, info, defaultPollInterval)
		if errors.As(err, &missing) {
			info = nil
			continue
		}
		if err != nil {
			return readinessBudgetError(ctx, waitCtx, err, h, w)
		}
		info = current
		if delay > 0 {
			if err := h.sleeper(waitCtx, max(0, min(delay, deadline.Sub(h.clock())))); err != nil {
				return readinessBudgetError(ctx, waitCtx, err, h, w)
			}
		}
	}
}

// Terminate gracefully shuts the Computer down and waits until Pine confirms it's gone
// (GET 404) or waitTimeout elapses. true = the record is gone (its final save may still be
// finishing); false = delete issued but not confirmed gone.
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
	var info *controlplane.SandboxInfo
	for {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		remaining := deadline.Sub(h.clock())
		if remaining <= 0 || waitCtx.Err() != nil {
			return false, nil
		}
		current, delay, err := h.observe(waitCtx, info, interval)
		var missing *NotFoundError
		if errors.As(err, &missing) || (err == nil && computeReleased(current)) {
			return true, nil
		}
		info = current
		remaining = deadline.Sub(h.clock())
		if remaining <= 0 {
			return false, nil
		}
		if delay > 0 {
			if err := h.sleeper(waitCtx, min(delay, remaining)); err != nil {
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
}

func (h *SandboxHandle) observe(ctx context.Context, previous *controlplane.SandboxInfo, interval time.Duration) (*controlplane.SandboxInfo, time.Duration, error) {
	started := h.clock()
	var info *controlplane.SandboxInfo
	var err error
	if previous != nil && previous.ETag != "" {
		info, err = h.cp.WaitForChange(ctx, h.id, previous)
		var timeout *TimeoutError
		var connection *ConnectionError
		var server *ServerError
		transient := errors.As(err, &timeout) || errors.As(err, &connection) ||
			(errors.As(err, &server) && (server.Status == http.StatusBadGateway ||
				server.Status == http.StatusServiceUnavailable || server.Status == http.StatusGatewayTimeout))
		if ctx.Err() == nil && transient {
			// An observation failure changes no lifecycle state.
			info, err = previous, nil
		}
	} else {
		info, err = h.cp.Get(ctx, h.id)
	}
	if err == nil && info.ETag != "" && (previous == nil || info.ETag != previous.ETag) {
		return info, 0, nil
	}
	// Old servers and immediate failures cannot spin. Time already spent
	// waiting counts; a completed server wait needs no additional sleep.
	return info, max(0, interval-h.clock().Sub(started)), err
}

// Kill is an ungraceful best-effort delete (no wait); it never returns an error.
func (h *SandboxHandle) Kill(ctx context.Context) bool {
	return h.cp.Destroy(ctx, h.id) == nil
}

// Alive reports whether the Computer is in a live state (running/pending/stopping).
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

// gone reports whether the Computer sandbox's record is confirmed deleted (a 404). Any other outcome
// (still present, or a transient error) is treated as not-yet-gone.
func (h *SandboxHandle) gone(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	info, err := h.cp.Get(ctx, h.id)
	var nf *NotFoundError
	return errors.As(err, &nf) || (err == nil && computeReleased(info))
}
