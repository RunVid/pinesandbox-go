package pinesandbox

import (
	"fmt"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/spec"
	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/bind"
	"go.pinesandbox.io/computer/internal/controlplane"
	"go.pinesandbox.io/computer/internal/tokens"
)

// The SDK's public error vocabulary. Each is an alias of the internal type that produces
// it, so callers use errors.As(err, &pinesandbox.NotFoundError{}) etc. without importing
// internal packages. The aliases keep one source of truth per error (the layer that
// raises it) while presenting a single flat surface.

// Agent-lane control-flow sentinels: the coordinator statuses that carry control meaning,
// as errors.Is-able values so callers branch on intent instead of raw status ints. Each is
// a *APIError carrying just its problem-type slug; APIError.Is matches by slug, so
// errors.Is(err, ErrTaskNotReady) is true for the live wire error AND errors.As(err,
// &apiErr) still reaches its full detail (status, request id, retryable). Slugs are pinned
// to the coordinator's error taxonomy (validated in errors_test.go).
var (
	// ErrTaskNotReady (409): a turn is in flight, the result isn't materialized — poll again.
	ErrTaskNotReady = &problem.APIError{Status: 409, ProblemType: "/errors/task-not-ready"}
	// ErrSessionBusy (409): the session already has an active task (one-active-per-session).
	ErrSessionBusy = &problem.APIError{Status: 409, ProblemType: "/errors/session-busy"}
	// ErrSessionLimit (409): the Computer's concurrent-session cap is reached — close an
	// idle/finished session (DestroySession) to free a slot, then retry CreateSession.
	ErrSessionLimit = &problem.APIError{Status: 409, ProblemType: "/errors/session-limit"}
	// ErrNoActiveTask (404): no task exists for this session yet (nothing to read/steer).
	ErrNoActiveTask = &problem.APIError{Status: 404, ProblemType: "/errors/no-active-task"}
	// ErrActionNotImplemented (501): the action isn't available here (e.g. no resident agent
	// configured on this pool — agent.Run / delegate-mode turns).
	ErrActionNotImplemented = &problem.APIError{Status: 501, ProblemType: "/errors/action-not-implemented"}
)

// Wire / transport / spec-version (coordinator + control-plane responses).
type (
	// APIError is the RFC-9457 wire error from the coordinator data plane. Callers read
	// {Status, ProblemType, Detail, RequestID, Retryable}, errors.Is the control sentinels
	// above, or switch on ProblemType.
	APIError = problem.APIError
	// TimeoutError is a normalized request timeout (net deadline, context, or 408/504).
	TimeoutError = transport.TimeoutError
	// ConnectionError is a normalized connection failure (dial/DNS/TLS/EOF).
	ConnectionError = transport.ConnectionError
	// SpecVersionMismatch is raised when the gateway serves a different Computer-API major.
	SpecVersionMismatch = spec.MismatchError
)

// Bind handshake errors.
type (
	BindError                    = bind.BindError
	BindAuthError                = bind.BindAuthError
	ComputerAlreadyAttachedError = bind.ComputerAlreadyAttachedError
	PodPoisonedError             = bind.PodPoisonedError
	BrokerUnreachableError       = bind.BrokerUnreachableError
	BindTimeoutError             = bind.BindTimeoutError
	RebindRequiredError          = bind.RebindRequiredError
)

// Control-token + attach-credential (portal) errors.
type (
	ControlTokenError         = tokens.ControlTokenError
	InvalidClientKey          = tokens.InvalidClientKey
	InsufficientScope         = tokens.InsufficientScope
	RateLimited               = tokens.RateLimited
	AttachCredentialsError    = tokens.AttachCredentialsError
	ComputerRegistrationError = tokens.ComputerRegistrationError
	UnknownComputerError      = tokens.UnknownComputerError
)

// Control-plane (lifecycle) errors.
type (
	BadRequestError          = controlplane.BadRequestError
	UnauthorizedError        = controlplane.UnauthorizedError
	ForbiddenError           = controlplane.ForbiddenError
	NotFoundError            = controlplane.NotFoundError
	ConflictError            = controlplane.ConflictError
	UnprocessableEntityError = controlplane.UnprocessableEntityError
	ServerError              = controlplane.ServerError
	ControlPlaneError        = controlplane.ControlPlaneError
)

// SandboxFailedError: the pod entered a terminal state (failed/terminated) before it
// became Ready during attach.
type SandboxFailedError struct {
	SandboxID string
	State     string
}

func (e *SandboxFailedError) Error() string {
	return fmt.Sprintf("pinesandbox: Computer pod %s entered terminal state %q before becoming Ready", e.SandboxID, e.State)
}

// ReadyTimeoutError: the pod did not become Ready within the attach timeout.
type ReadyTimeoutError struct {
	SandboxID string
	LastState string
}

func (e *ReadyTimeoutError) Error() string {
	return fmt.Sprintf("pinesandbox: Computer pod %s did not become Ready in time (last state %q)", e.SandboxID, e.LastState)
}
