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

// Session and agent sentinels: the API errors that carry control meaning, as
// errors.Is-able values so callers branch on intent instead of raw status ints. Each is
// a *APIError carrying just its problem-type slug; APIError.Is matches by slug, so
// errors.Is(err, ErrTaskNotReady) is true for the live wire error AND errors.As(err,
// &apiErr) still reaches its full detail (status, request id, retryable).
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
	// act on. The API reports this as TWO slugs, both covered here:
	// `/errors/no-active-task` when the session is idle, and `/errors/no-active-turn`
	// when the turn ends while the request is being handled. The session and its task history are
	// fine — there is just nothing to steer right now; start a turn with Run.
	ErrNoActiveTurn = &problem.APIError{
		Status:          409,
		ProblemType:     "/errors/no-active-task",
		AltProblemTypes: []string{"/errors/no-active-turn"},
	}
	// ErrNoActiveTask is the deprecated predecessor of the two sentinels above.
	// It is FROZEN at its original {404, /errors/no-active-task} value — do not
	// mutate the field. APIError.Is matches by problem-type slug only (status is
	// ignored), so this sentinel already matches the API's real
	// {409, /errors/no-active-task} mutation error by slug; changing its Status
	// would give no matching benefit and could break code that reads the
	// sentinel's fields. The real gap it never covered — a task READ on a
	// never-run session ({404, /errors/task-not-found}, a different slug) — is
	// filled by ErrTaskNotFound above.
	//
	// Deprecated: use ErrTaskNotFound for reads and ErrNoActiveTurn for mutations.
	ErrNoActiveTask = &problem.APIError{Status: 404, ProblemType: "/errors/no-active-task"}
	// ErrActionNotImplemented (501): the action isn't available here (e.g. Pine's agent
	// isn't available in this environment — AgentMode.Run).
	ErrActionNotImplemented = &problem.APIError{Status: 501, ProblemType: "/errors/action-not-implemented"}
	// ErrLeaseExpired (403): the access lease lapsed and Pine DEFINITIVELY refused to
	// renew it — the project is revoked / out of credits. Control is cut (reads stay open);
	// re-attach the Computer or surface the suspension. Retrying won't help.
	ErrLeaseExpired = &problem.APIError{Status: 403, ProblemType: "/errors/lease-expired"}
	// ErrLeaseRefreshUnavailable (503, retryable): the lease lapsed and the refresh failed
	// TRANSIENTLY (a temporary Pine outage) — no revocation verdict. Retry; do NOT re-attach.
	ErrLeaseRefreshUnavailable = &problem.APIError{Status: 503, ProblemType: "/errors/lease-refresh-unavailable"}
)

// API, transport and version errors.
type (
	// APIError is an RFC 9457 problem returned by a Computer. Callers read
	// {Status, ProblemType, Detail, RequestID, Retryable}, errors.Is the control sentinels
	// above, or switch on ProblemType.
	APIError = problem.APIError
	// TimeoutError is a normalized request timeout (net deadline, context, or 408/504).
	TimeoutError = transport.TimeoutError
	// ConnectionError is a normalized connection failure (dial/DNS/TLS/EOF).
	ConnectionError = transport.ConnectionError
	// SpecVersionMismatch is returned when a Computer serves a different Computer API
	// major version.
	SpecVersionMismatch = spec.MismatchError
	// SandboxGoneError means the runtime the Computer was attached to is gone. It
	// wraps APIError, preserving host/op/request-id diagnostics via errors.As.
	SandboxGoneError = coordinator.SandboxGoneError
	// AgentProtocolUnsupportedError is a local capability refusal, not an APIError.
	AgentProtocolUnsupportedError = coordinator.AgentProtocolUnsupportedError
	// RunUnconfirmedError is a run whose response never arrived; send it again
	// with its IdempotencyKey. It wraps the last transport or gateway error, and
	// the context's error when the context ended first.
	RunUnconfirmedError = coordinator.RunUnconfirmedError
)

// IsRetryable returns the server's retry judgment for a data-plane API error,
// including errors wrapped by SandboxGoneError or TokenRejectedError. It does
// not guess replay safety for transport failures: callers must apply the
// operation-specific retry policy for timeouts and connection errors.
func IsRetryable(err error) bool {
	var stopErr *StopSaveError
	if errors.As(err, &stopErr) {
		return stopErr.Retryable
	}
	var apiErr *problem.APIError
	return errors.As(err, &apiErr) && apiErr.Retryable
}

// AttachAuthorizationCommittedError means Portal committed a new binding
// revision, but attaching the Computer to the runtime did not complete. The
// runtime is cleaned up best-effort. Persist BindingRevision before retrying so the next
// attach can supersede the failed authorization instead of losing CAS state.
// Credentials is non-nil for CreateComputer, which otherwise has no result in
// which to return identity and capture-key material selected for the committed
// attach. Its String and GoString forms redact secrets.
// The underlying typed attach error remains reachable with errors.Is/errors.As.
type AttachAuthorizationCommittedError struct {
	BindingRevision int64
	SandboxID       string
	Credentials     *Credentials
	Err             error
}

func (e *AttachAuthorizationCommittedError) Error() string {
	return fmt.Sprintf(
		"pinesandbox: attach authorization committed at revision %d for sandbox %s, but the attach did not complete: %v",
		e.BindingRevision, e.SandboxID, e.Err,
	)
}

func (e *AttachAuthorizationCommittedError) Unwrap() error { return e.Err }

// Errors from attaching a Computer to a runtime.
type (
	// BindError is an attach failure the other attach errors don't cover.
	BindError = bind.BindError
	// BindAuthError means the runtime rejected the attach authorization (401/403).
	BindAuthError = bind.BindAuthError
	// ComputerAlreadyAttachedError means another runtime already holds this Computer.
	ComputerAlreadyAttachedError = bind.ComputerAlreadyAttachedError
	// PodPoisonedError means the runtime is attached to a different Computer or can't
	// be reused. Start a fresh runtime; don't retry this one.
	PodPoisonedError = bind.PodPoisonedError
	// BrokerUnreachableError means the Computer's saved state couldn't be restored
	// because the state service is unavailable.
	BrokerUnreachableError = bind.BrokerUnreachableError
	// BindTimeoutError means the runtime wasn't ready to attach within the readiness
	// budget.
	BindTimeoutError = bind.BindTimeoutError
	// TokenRejectedError: a 401 on a ct_/ps_ that worked before — a report that the
	// Computer did not recognize the token, not an instruction to re-attach. On a
	// live current runtime this is binding_auth_lost; attach only on confirmed
	// sandbox-gone evidence. (Renamed from RebindRequiredError in 0.3.9.)
	TokenRejectedError = bind.TokenRejectedError
)

// Errors from Portal: project tokens, attach credentials, locations and Computer records.
type (
	ControlTokenError            = tokens.ControlTokenError
	InvalidClientKey             = tokens.InvalidClientKey
	InsufficientScope            = tokens.InsufficientScope
	ProjectAccessDenied          = tokens.ProjectAccessDenied
	RateLimited                  = tokens.RateLimited
	AttachCredentialsError       = tokens.AttachCredentialsError
	LocationDiscoveryError       = tokens.LocationDiscoveryError
	BindingRevisionConflictError = tokens.BindingRevisionConflictError
	ComputerRegistrationError    = tokens.ComputerRegistrationError
	ComputerDeletionError        = tokens.ComputerDeletionError
	UnknownComputerError         = tokens.UnknownComputerError
)

// Errors from runtime lifecycle requests.
type (
	BadRequestError          = controlplane.BadRequestError
	UnauthorizedError        = controlplane.UnauthorizedError
	ForbiddenError           = controlplane.ForbiddenError
	NotFoundError            = controlplane.NotFoundError
	ConflictError            = controlplane.ConflictError
	UnprocessableEntityError = controlplane.UnprocessableEntityError
	ServerError              = controlplane.ServerError
	ControlPlaneError        = controlplane.ControlPlaneError
	// CapacityExceededError is the create refusal behind ErrCapacityExceeded.
	CapacityExceededError = controlplane.CapacityExceededError
	// CreateRateLimitedError is the create refusal behind ErrCreateRateLimited.
	CreateRateLimitedError = controlplane.CreateRateLimitedError
)

// ErrCapacityExceeded (429 COMPUTER_CAPACITY_EXCEEDED): create/attach was refused because the
// project is at its concurrent Computer limit, so nothing was provisioned. Not retried by the
// SDK; stop another of the project's Computers first. errors.As reaches CapacityExceededError.
var ErrCapacityExceeded = controlplane.ErrCapacityExceeded

// ErrCreateRateLimited (429 COMPUTER_CREATE_RATE_LIMITED): create/attach was refused because the
// project has started as many new Computers in the last minute as it may, so nothing was
// provisioned. Not retried by the SDK; errors.As reaches CreateRateLimitedError, whose RetryAfter
// says how long to wait before starting another.
var ErrCreateRateLimited = controlplane.ErrCreateRateLimited

// SandboxFailedError: the runtime reported failed, terminated or unavailable
// before completing the requested lifecycle operation. Unavailable may be retryable.
type SandboxFailedError struct {
	SandboxID string
	State     string
}

func (e *SandboxFailedError) Error() string {
	return fmt.Sprintf("pinesandbox: Computer runtime %s reported %q before completing the lifecycle operation", e.SandboxID, e.State)
}

// ReadyTimeoutError: allocation or a memory lifecycle operation exceeded its budget.
type ReadyTimeoutError struct {
	SandboxID string
	LastState string
}

func (e *ReadyTimeoutError) Error() string {
	return fmt.Sprintf("pinesandbox: Computer runtime %s did not complete the lifecycle operation in time (last state %q)", e.SandboxID, e.LastState)
}

// RuntimeRetainedError means Start observed an existing runtime. Inspect State
// and explicitly Stop it or Resume its retained memory; Start allocated nothing.
type RuntimeRetainedError struct {
	RuntimeID string
	State     string
}

func (e *RuntimeRetainedError) Error() string {
	return fmt.Sprintf("pinesandbox: Computer still has runtime %s (%s); Stop it or Resume retained memory before Start", e.RuntimeID, e.State)
}
