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

// Wire / transport / spec-version (coordinator + control-plane responses).
type (
	// APIError is the RFC-9457 wire error from the coordinator data plane. Callers read
	// {Status, ProblemType, Detail, RequestID, Retryable} and switch on ProblemType.
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
