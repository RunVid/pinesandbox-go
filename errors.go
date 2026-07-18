package pinesandbox

import (
	"errors"
	"fmt"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/spec"
	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/bind"
	"go.pinesandbox.io/computer/internal/controlplane"
	"go.pinesandbox.io/computer/internal/coordinator"
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
	// ErrControlNotHeld (409): this session/caller does not hold the control
	// authority required by the attempted mutation.
	ErrControlNotHeld = &problem.APIError{Status: 409, ProblemType: "/errors/control-not-held"}
	// ErrSessionNotFound (404): the named session does not exist on this live
	// Computer. It is deliberately distinct from SandboxGoneError.
	ErrSessionNotFound = &problem.APIError{Status: 404, ProblemType: "/errors/session-not-found"}
	// ErrTaskNotReady (409): a turn is in flight, the result isn't materialized — poll again.
	ErrTaskNotReady = &problem.APIError{Status: 409, ProblemType: "/errors/task-not-ready"}
	// ErrSessionBusy (409): the session already has an active task (one-active-per-session).
	ErrSessionBusy = &problem.APIError{Status: 409, ProblemType: "/errors/session-busy"}
	// ErrSessionLimit (409): the Computer's concurrent-session cap is reached — close an
	// idle/finished session (DestroySession) to free a slot, then retry CreateSession.
	ErrSessionLimit = &problem.APIError{Status: 409, ProblemType: "/errors/session-limit"}
	// ErrTaskNotFound (404, /errors/task-not-found): a task READ on a session that has
	// never run a turn. This is a VALID IDLE SESSION — start a turn on it; do not treat
	// the session as stale and never re-create it (a duplicate CreateSession hits the
	// name-conflict 409).
	ErrTaskNotFound = &problem.APIError{Status: 404, ProblemType: "/errors/task-not-found"}
	// ErrNoActiveTurn (409): a task MUTATION (answer/steer/…) with no active turn to
	// act on. The coordinator emits this as TWO slugs, both covered here: the
	// idle-session gate `/errors/no-active-task` ("no in-flight turn"), and the
	// mid-request race `/errors/no-active-turn` where the turn ends between the
	// in-flight check and the broker mutation. The session and its task history are
	// fine — there is just nothing to steer right now; start a turn with Run.
	ErrNoActiveTurn = &problem.APIError{
		Status:          409,
		ProblemType:     "/errors/no-active-task",
		AltProblemTypes: []string{"/errors/no-active-turn"},
	}
	// ErrNoActiveTask is the deprecated predecessor of the two sentinels above.
	// It is FROZEN at its original {404, /errors/no-active-task} value — do not
	// mutate the field. APIError.Is matches by problem-type slug only (status is
	// ignored), so this sentinel already matches the coordinator's real
	// {409, /errors/no-active-task} mutation error by slug; changing its Status
	// would give no matching benefit and could break code that reads the
	// sentinel's fields. The real gap it never covered — a task READ on a
	// never-run session ({404, /errors/task-not-found}, a different slug) — is
	// filled by ErrTaskNotFound above.
	//
	// Deprecated: use ErrTaskNotFound for reads and ErrNoActiveTurn for mutations.
	ErrNoActiveTask = &problem.APIError{Status: 404, ProblemType: "/errors/no-active-task"}
	// ErrActionNotImplemented (501): the action isn't available here (e.g. no resident agent
	// configured on this pool — agent.Run / delegate-mode turns).
	ErrActionNotImplemented = &problem.APIError{Status: 501, ProblemType: "/errors/action-not-implemented"}
	// ErrLeaseExpired (403): the access lease lapsed and the portal DEFINITIVELY refused a
	// refresh — the project is revoked / out of credits. Control is cut (reads stay open);
	// re-attach the Computer or surface the suspension. Retrying won't help.
	ErrLeaseExpired = &problem.APIError{Status: 403, ProblemType: "/errors/lease-expired"}
	// ErrLeaseRefreshUnavailable (503, retryable): the lease lapsed and the refresh failed
	// TRANSIENTLY (gateway/portal blip) — no revocation verdict. Retry; do NOT re-attach.
	ErrLeaseRefreshUnavailable = &problem.APIError{Status: 503, ProblemType: "/errors/lease-refresh-unavailable"}
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
	// SandboxGoneError means the gateway cannot route the bound sandbox. It
	// wraps APIError, preserving host/op/request-id diagnostics via errors.As.
	SandboxGoneError = coordinator.SandboxGoneError
)

// IsRetryable returns the server's retry judgment for a data-plane API error,
// including errors wrapped by SandboxGoneError or TokenRejectedError. It does
// not guess replay safety for transport failures: callers must apply the
// operation-specific retry policy for timeouts and connection errors.
func IsRetryable(err error) bool {
	var apiErr *problem.APIError
	return errors.As(err, &apiErr) && apiErr.Retryable
}

// AttachAuthorizationCommittedError means Portal committed a new binding
// revision, but the later coordinator bind did not complete. The fresh pod is
// cleaned up best-effort. Persist BindingRevision before retrying so the next
// attach can supersede the failed authorization instead of losing CAS state.
// Credentials is non-nil for CreateComputer, which otherwise has no result in
// which to return identity and capture-key material selected for the committed
// attach. Its String and GoString forms redact secrets.
// The underlying typed bind error remains reachable with errors.Is/errors.As.
type AttachAuthorizationCommittedError struct {
	BindingRevision int64
	SandboxID       string
	Credentials     *Credentials
	Err             error
}

func (e *AttachAuthorizationCommittedError) Error() string {
	return fmt.Sprintf(
		"pinesandbox: attach authorization committed at revision %d for sandbox %s, but coordinator bind failed: %v",
		e.BindingRevision, e.SandboxID, e.Err,
	)
}

func (e *AttachAuthorizationCommittedError) Unwrap() error { return e.Err }

// Bind handshake errors.
type (
	BindError                    = bind.BindError
	BindAuthError                = bind.BindAuthError
	ComputerAlreadyAttachedError = bind.ComputerAlreadyAttachedError
	PodPoisonedError             = bind.PodPoisonedError
	BrokerUnreachableError       = bind.BrokerUnreachableError
	BindTimeoutError             = bind.BindTimeoutError
	// TokenRejectedError: a 401 on a previously-bound ct_/ps_ — a report that the
	// coordinator did not recognize the token, not an instruction to re-attach. On a
	// live current sandbox this is binding_auth_lost; attach only on confirmed
	// sandbox-gone evidence. (Renamed from RebindRequiredError in 0.3.9.)
	TokenRejectedError = bind.TokenRejectedError
)

// Control-token + attach-credential (portal) errors.
type (
	ControlTokenError            = tokens.ControlTokenError
	InvalidClientKey             = tokens.InvalidClientKey
	InsufficientScope            = tokens.InsufficientScope
	ProjectAccessDenied          = tokens.ProjectAccessDenied
	RateLimited                  = tokens.RateLimited
	AttachCredentialsError       = tokens.AttachCredentialsError
	BindingRevisionConflictError = tokens.BindingRevisionConflictError
	ComputerRegistrationError    = tokens.ComputerRegistrationError
	UnknownComputerError         = tokens.UnknownComputerError
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
