package pinesandbox

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"go.pinesandbox.io/computer/internal/artifacts"
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
	// ErrComputerStopped (409): a Stop has begun, whoever sent it, and the Computer
	// refuses every call until it starts again. Not a conflict to retry: follow the
	// Stop in Status, then Start.
	ErrComputerStopped = &problem.APIError{Status: 409, ProblemType: "/errors/computer-stopped"}
	// ErrTaskNotReady (409): a turn is in flight, the result isn't materialized — poll again.
	ErrTaskNotReady = &problem.APIError{Status: 409, ProblemType: "/errors/task-not-ready"}
	// ErrSessionBusy (409): the session already has an active task (one-active-per-session).
	ErrSessionBusy = &problem.APIError{Status: 409, ProblemType: "/errors/session-busy"}
	// ErrSessionLimit (409): the Computer's concurrent-session cap is reached — close an
	// idle/finished session (DestroySession) to free a slot, then retry CreateSession.
	ErrSessionLimit = &problem.APIError{Status: 409, ProblemType: "/errors/session-limit"}
	// ErrSessionNameTaken (409): CreateSession found a session with this name already.
	// Choose another name.
	ErrSessionNameTaken = &problem.APIError{Status: 409, ProblemType: "/errors/session-name-taken"}
	// ErrTaskNotFound (404, /errors/task-not-found): a task READ on a session that has
	// never run a turn. This is a VALID IDLE SESSION — start a turn on it; do not treat
	// the session as stale and never re-create it (a duplicate CreateSession hits the
	// name-conflict 409).
	ErrTaskNotFound = &problem.APIError{Status: 404, ProblemType: "/errors/task-not-found"}
	// ErrStaleTask (409): a read pinned to a task (ExpectedTaskID) found the session on
	// another one. Its saved cursors belong to the old task; read the current task.
	ErrStaleTask = &problem.APIError{Status: 409, ProblemType: "/errors/stale-task"}
	// ErrStaleTurn (409): the action named a turn that is no longer the current one:
	// newer work started, or the turn was already answered. Read the current turn.
	ErrStaleTurn = &problem.APIError{Status: 409, ProblemType: "/errors/stale-turn"}
	// ErrNoPendingRequest (409): an answer found the turn isn't waiting for one.
	ErrNoPendingRequest = &problem.APIError{Status: 409, ProblemType: "/errors/no-pending-request"}
	// ErrTurnNotFound (404): the session has no turn with this id.
	ErrTurnNotFound = &problem.APIError{Status: 404, ProblemType: "/errors/turn-not-found"}
	// ErrAuthorInFlight (409): a skill is already being written for this session; one
	// authoring run at a time.
	ErrAuthorInFlight = &problem.APIError{Status: 409, ProblemType: "/errors/author-in-flight"}
	// ErrSecretInSkill (422): the skill wasn't saved because it contains something that
	// looks like a secret.
	ErrSecretInSkill = &problem.APIError{Status: 422, ProblemType: "/errors/secret-in-skill"}
	// ErrSkillsUnavailable (503): skills aren't available on this Computer.
	ErrSkillsUnavailable = &problem.APIError{Status: 503, ProblemType: "/errors/skills-unavailable"}
	// ErrHistoryExpired (410): the cursor is older than the history the Computer keeps;
	// the problem's pruned_through says where the kept history begins.
	ErrHistoryExpired = &problem.APIError{Status: 410, ProblemType: "/errors/history-expired"}
	// ErrEventsUnavailable (503, retryable): the Computer couldn't read its event history
	// just now. Retry with the same cursor.
	ErrEventsUnavailable = &problem.APIError{Status: 503, ProblemType: "/errors/events-unavailable"}
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
	// ErrFeatureDisabled (403): the project, or this Computer's binding, doesn't have a
	// feature the call needs, such as the shell grant for Exec or the artifacts feature
	// for the artifact methods. Retrying won't help.
	ErrFeatureDisabled = &problem.APIError{Status: 403, ProblemType: "/errors/feature-disabled"}
	// ErrLeaseExpired (403): the access lease lapsed and Pine DEFINITIVELY refused to
	// renew it — the project is revoked / out of credits. Control is cut (reads stay open);
	// re-attach the Computer or surface the suspension. Retrying won't help.
	ErrLeaseExpired = &problem.APIError{Status: 403, ProblemType: "/errors/lease-expired"}
	// ErrLeaseRefreshUnavailable (503, retryable): the lease lapsed and the refresh failed
	// TRANSIENTLY (a temporary Pine outage) — no revocation verdict. Retry; do NOT re-attach.
	ErrLeaseRefreshUnavailable = &problem.APIError{Status: 503, ProblemType: "/errors/lease-refresh-unavailable"}
)

// Local refusals.
var (
	// ErrNotBound: the Computer handle has no binding. It was never attached, or Stop, Kill
	// or Delete on this handle removed its binding. Calls that need a binding return a
	// *BindError that matches errors.Is(err, ErrNotBound); the error is never ErrNotBound
	// itself, so compare with errors.Is, not ==.
	ErrNotBound = bind.ErrNotBound
	// ErrIdleTimeoutUnsupported: the Computer was started without a lifetime policy, so
	// SetIdleTimeout cannot change its idle timeout; choose it on the next Start.
	ErrIdleTimeoutUnsupported = errors.New("pinesandbox: Computer was started without a lifetime policy; choose the idle timeout on the next Start")
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
	// SandboxGoneError means the Computer can't be reached at its host, which is also
	// true while it's paused or recovering. It wraps APIError, preserving
	// host/op/request-id diagnostics via errors.As.
	SandboxGoneError = coordinator.SandboxGoneError
	// AgentProtocolUnsupportedError is a local capability refusal, not an APIError.
	AgentProtocolUnsupportedError = coordinator.AgentProtocolUnsupportedError
	// RunUnconfirmedError is a run whose response never arrived; send it again
	// with its IdempotencyKey. It wraps the error that ended the run, the
	// context's error when the context ended first. After a lost response that
	// can be any error, a SandboxGoneError included.
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
// revision, but the attach did not complete. The SDK shuts the Computer down
// best-effort. Persist BindingRevision before retrying so the next
// attach can supersede the failed authorization instead of losing CAS state.
// Credentials is non-nil for CreateComputer, which otherwise has no result in
// which to return identity and capture-key material selected for the committed
// attach. Its String and GoString forms redact secrets.
// The underlying typed attach error remains reachable with errors.Is/errors.As.
type AttachAuthorizationCommittedError struct {
	BindingRevision int64
	// SandboxID is the id of the Computer sandbox the committed authorization named.
	SandboxID   string
	Credentials *Credentials
	Err         error
}

func (e *AttachAuthorizationCommittedError) Error() string {
	return fmt.Sprintf(
		"pinesandbox: attach authorization committed at revision %d for Computer sandbox %s, but the attach did not complete: %v",
		e.BindingRevision, e.SandboxID, e.Err,
	)
}

func (e *AttachAuthorizationCommittedError) Unwrap() error { return e.Err }

// Errors from attaching to a started Computer.
type (
	// BindError is an attach failure the other attach errors don't cover, or a call that
	// needs a binding on a handle that has none (it matches
	// errors.Is(err, ErrNotBound)).
	BindError = bind.BindError
	// BindAuthError means the Computer rejected the attach authorization (401/403).
	BindAuthError = bind.BindAuthError
	// ComputerAlreadyAttachedError means this Computer is already attached elsewhere.
	ComputerAlreadyAttachedError = bind.ComputerAlreadyAttachedError
	// SandboxUnusableError means this Computer sandbox is attached to a different Computer
	// or can't be reused. Start the Computer again; don't retry this attach.
	SandboxUnusableError = bind.SandboxUnusableError
	// PodPoisonedError is the former name of SandboxUnusableError.
	//
	// Deprecated: use SandboxUnusableError.
	PodPoisonedError = SandboxUnusableError
	// SavedStateUnavailableError means the Computer's saved state couldn't be restored
	// because the state service is unavailable.
	SavedStateUnavailableError = bind.SavedStateUnavailableError
	// BrokerUnreachableError is the former name of SavedStateUnavailableError.
	//
	// Deprecated: use SavedStateUnavailableError.
	BrokerUnreachableError = SavedStateUnavailableError
	// BindTimeoutError means the Computer wasn't ready to attach within the readiness
	// budget.
	BindTimeoutError = bind.BindTimeoutError
	// TokenRejectedError: a 401 on the binding's Computer token (ct_) — a report that
	// the Computer no longer recognizes the binding, not an instruction to re-attach. On
	// a running Computer this is binding_auth_lost; attach again only once the Computer
	// is confirmed stopped. A 401 on a session token (ps_) is a plain *APIError.
	// (Renamed from RebindRequiredError in 0.3.9.)
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

// Errors from a Computer's artifacts. Other storage refusals are an *APIError with the
// storage status and its message.
type (
	// ArtifactNotFoundError means the Computer's artifacts have no file at Path, the
	// path the call named. It wraps the storage *APIError.
	ArtifactNotFoundError = artifacts.NotFoundError
	// ArtifactStorageFullError means the Computer stores more than its storage limit
	// (UsedBytes of LimitBytes), so Upload and Copy wrote nothing. Delete files to make
	// room.
	ArtifactStorageFullError = artifacts.StorageFullError
)

// Errors from lifecycle requests.
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

// SpendLimitReachedError is the refusal behind ErrSpendLimitReached.
type SpendLimitReachedError = problem.SpendLimitReachedError

// ErrSpendLimitReached (403 SPEND_LIMIT_REACHED): starting, attaching or resuming a Computer, or
// changing its idle timeout, was refused because the project reached its monthly spend limit
// or its organization is out of credits. The same error whichever call was refused. Nothing
// was started or changed; reading, stopping, pausing and deleting still work. Not retried by
// the SDK. errors.As reaches SpendLimitReachedError, whose Msg says which limit it is.
var ErrSpendLimitReached = problem.ErrSpendLimitReached

// ErrCapacityExceeded (429 COMPUTER_CAPACITY_EXCEEDED): create/attach was refused because the
// project is at its concurrent Computer limit, so nothing was provisioned. Not retried by the
// SDK; stop another of the project's Computers first. errors.As reaches CapacityExceededError.
var ErrCapacityExceeded = controlplane.ErrCapacityExceeded

// ErrCreateRateLimited (429 COMPUTER_CREATE_RATE_LIMITED): create/attach was refused because the
// project has started as many new Computers in the last minute as it may, so nothing was
// provisioned. Not retried by the SDK; errors.As reaches CreateRateLimitedError, whose RetryAfter
// says how long to wait before starting another.
var ErrCreateRateLimited = controlplane.ErrCreateRateLimited

// SandboxFailedError: the runtime reported failed, terminated or unavailable (with
// Reason) before completing the lifecycle operation. Unavailable may be retryable.
type SandboxFailedError struct {
	SandboxID string
	State     string
	Reason    string
	wait      readyWait
}

func (e *SandboxFailedError) Error() string {
	return runtimeMessage(pickMessage(admissionMessageRuntimeFailed, e.Reason),
		runtimeText{sandboxID: e.SandboxID, state: e.State, reason: e.Reason, wait: e.wait})
}

// ReadyTimeoutError: the ReadyTimeout budget ran out before the current runtime was
// released (nothing was started) or the new one ran; also Pause's and Resume's deadline.
// Nothing was attached and the binding is unchanged; Waited is the time spent. It is
// errors.Is context.DeadlineExceeded, but only the SDK's budget yields this type.
type ReadyTimeoutError struct {
	SandboxID  string
	LastState  string
	LastReason string
	Waited     time.Duration
	wait       readyWait
}

func (e *ReadyTimeoutError) Error() string {
	text := pickMessage(admissionMessageReadyTimedOut, e.LastReason)
	switch e.wait.target {
	case targetReleased:
		text = decideAdmission(e.wait.column, e.LastState, e.LastReason).Message
	case targetCurrent:
		text = admissionMessageCurrentReadTimedOut.withoutReason
	}
	return runtimeMessage(text, runtimeText{sandboxID: e.SandboxID, state: e.LastState, reason: e.LastReason,
		wait: e.wait, waited: e.Waited})
}

// Is reports the SDK's readiness budget as a deadline.
func (e *ReadyTimeoutError) Is(target error) bool { return target == context.DeadlineExceeded }

// SandboxRetainedError means Start or Recover refused the Computer's unreleased current
// runtime and started nothing.
type SandboxRetainedError struct {
	SandboxID string
	State     string
	Reason    string
	column    admissionColumn // whose refusal: Start's (the zero value) or Recover's
}

func (e *SandboxRetainedError) Error() string {
	return runtimeMessage(decideAdmission(e.column, e.State, e.Reason).Message,
		runtimeText{sandboxID: e.SandboxID, state: e.State, reason: e.Reason})
}

// ComputerAlreadyStartedError means Start or Recover found the Computer's current runtime
// already started, and started nothing. It is not a *SandboxRetainedError.
type ComputerAlreadyStartedError struct {
	SandboxID string
	State     string
	Reason    string
	column    admissionColumn
}

func (e *ComputerAlreadyStartedError) Error() string {
	return runtimeMessage(decideAdmission(e.column, e.State, e.Reason).Message,
		runtimeText{sandboxID: e.SandboxID, state: e.State, reason: e.Reason})
}

type runtimeText struct {
	sandboxID, state, reason string
	wait                     readyWait
	waited                   time.Duration
}

// runtimeMessage fills an admission-table text with t; waited is in whole seconds, rounded half up.
func runtimeMessage(text string, t runtimeText) string {
	fresh := ""
	if t.wait.newRuntime {
		fresh = "New "
	}
	return "pinesandbox: " + strings.NewReplacer("{sandbox_id}", t.sandboxID, "{state}", t.state,
		"{reason}", t.reason, "{new}", fresh, "{target}", t.wait.target,
		"{waited}", strconv.FormatInt(int64(math.Round(t.waited.Seconds())), 10)).Replace(text)
}

func pickMessage(m admissionMessage, reason string) string {
	if reason == "" {
		return m.withoutReason
	}
	return m.withReason
}

// readyWait names what a readiness budget waits for, for its errors' messages.
type readyWait struct {
	target     string          // the state waited for, or targetReleased
	newRuntime bool            // the runtime this call created, not one it waited on
	column     admissionColumn // the release wait's: whose table text its timeout reports
	started    time.Time
}

// targetReleased is the old runtime's release, which Start and Recover wait for.
const targetReleased = "released"

// targetCurrent is the read of the Computer's binding and current runtime, which Start and
// Recover make first.
const targetCurrent = "current"

// currentReadError reports Start's budget running out inside a read of the current runtime as
// *ReadyTimeoutError; any other failure, the caller's own deadline included, stays as it is.
func currentReadError(callerCtx, readCtx context.Context, err error, sandboxID string, w readyWait, now time.Time) error {
	if callerCtx.Err() == nil && errors.Is(readCtx.Err(), context.DeadlineExceeded) {
		return &ReadyTimeoutError{SandboxID: sandboxID, Waited: now.Sub(w.started), wait: w}
	}
	return err
}

func newRuntimeReady(started time.Time) readyWait {
	return readyWait{target: statusRunning, newRuntime: true, started: started}
}
