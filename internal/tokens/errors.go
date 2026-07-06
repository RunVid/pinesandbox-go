// Package tokens sources the control-plane credential: it exchanges a project client key
// (pk_…) at the portal's POST /v1/control-token for a short-TTL EdDSA project JWS and
// caches it until just before expiry. Domain package (uses internal/base/transport). The
// root SDK re-exports the error types.
package tokens

import (
	"fmt"

	"go.pinesandbox.io/computer/internal/base/problem"
)

// tokenBase carries the common fields of every control-token error: the originating HTTP
// status (0 for a pre-flight failure), the portal's request id (for support), the resource
// context (Host = WHICH portal, Op = WHICH operation "<METHOD> <path>"), and the underlying
// cause (for errors.Unwrap). Host / Op ride the top-level error string so a generic handler
// keeps them even though the wrapping loses the underlying *problem.APIError's message.
type tokenBase struct {
	Msg       string
	Status    int
	RequestID string
	Host      string
	Op        string
	Cause     error
}

func (b tokenBase) Unwrap() error { return b.Cause }

func tokErr(label string, b tokenBase) string {
	var head string
	switch {
	case b.Status != 0:
		head = fmt.Sprintf("pinesandbox: %s (%d): %s", label, b.Status, b.Msg)
	default:
		head = fmt.Sprintf("pinesandbox: %s: %s", label, b.Msg)
	}
	// Resource-first troubleshooting suffix (host → op → request_id), rendered once via the
	// shared helper so the token layer matches the coordinator/control-plane error shape.
	return head + problem.ContextSuffix(b.Host, b.Op, b.RequestID)
}

// tokenBaseFrom builds a tokenBase from a wrapped *problem.APIError, carrying its resource
// context (Host / Op — set by transport.Do) + request id + status through to the top-level
// token error string. The default attach path (portal-as-issuer) is where an integrator sees
// these, so WHICH portal + WHICH operation must survive the wrapping.
func tokenBaseFrom(msg string, ae *problem.APIError) tokenBase {
	return tokenBase{
		Msg:       msg,
		Status:    ae.Status,
		RequestID: ae.RequestID,
		Host:      ae.Host,
		Op:        ae.Op,
		Cause:     ae,
	}
}

// ControlTokenError is a generic / unexpected failure minting the project token.
type ControlTokenError struct{ tokenBase }

func (e *ControlTokenError) Error() string { return tokErr("control-token mint failed", e.tokenBase) }

// InvalidClientKey: the portal rejected the pk_ (401) — unknown or revoked key.
type InvalidClientKey struct{ tokenBase }

func (e *InvalidClientKey) Error() string { return tokErr("invalid project client key", e.tokenBase) }

// InsufficientScope: the pk_ is valid but may not mint control-plane tokens (403) — it
// needs pk_session or pk_admin scope.
type InsufficientScope struct{ tokenBase }

func (e *InsufficientScope) Error() string {
	return tokErr("insufficient client-key scope", e.tokenBase)
}

// RateLimited: the portal rate-limited the mint (429) and retries were exhausted.
type RateLimited struct{ tokenBase }

func (e *RateLimited) Error() string { return tokErr("control-token mint rate-limited", e.tokenBase) }

// AttachCredentialsError is a generic / unexpected failure minting per-attach credentials
// (bind_token + broker_grant) or refreshing a broker grant.
type AttachCredentialsError struct{ tokenBase }

func (e *AttachCredentialsError) Error() string {
	return tokErr("attach-credentials mint failed", e.tokenBase)
}

// ComputerRegistrationError: the portal refused to register the computer_id (409/422 —
// e.g. a cross-project duplicate).
type ComputerRegistrationError struct{ tokenBase }

func (e *ComputerRegistrationError) Error() string {
	return tokErr("computer registration refused", e.tokenBase)
}

// UnknownComputerError: the computer_id is unknown, deleted, or cross-project (404) —
// register it first.
type UnknownComputerError struct{ tokenBase }

func (e *UnknownComputerError) Error() string { return tokErr("unknown computer", e.tokenBase) }
