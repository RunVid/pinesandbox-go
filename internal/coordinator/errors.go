package coordinator

import (
	"errors"
	"fmt"

	"go.pinesandbox.io/computer/internal/base/problem"
)

// SandboxGoneError means the gateway can no longer route the bound sandbox.
// It wraps the original APIError so callers retain host, operation, request id,
// and wire diagnostics through errors.As.
type SandboxGoneError struct {
	cause error
}

func (e *SandboxGoneError) Error() string {
	var apiErr *problem.APIError
	if errors.As(e.cause, &apiErr) {
		return "pinesandbox: bound sandbox is gone" +
			problem.ContextSuffix(apiErr.Host, apiErr.Op, apiErr.RequestID)
	}
	return fmt.Sprintf("pinesandbox: bound sandbox is gone: %v", e.cause)
}

func (e *SandboxGoneError) Unwrap() error { return e.cause }

func newSandboxGoneError(cause error) *SandboxGoneError {
	return &SandboxGoneError{cause: cause}
}

// RunUnconfirmedError is a run whose response never arrived: it may have
// started. Send it again with IdempotencyKey, which returns the turn it
// started, or starts it if it never did.
type RunUnconfirmedError struct {
	IdempotencyKey string
	Err            error
}

func (e *RunUnconfirmedError) Error() string {
	return fmt.Sprintf("pinesandbox: the run may have started; send it again with idempotency key %q: %v", e.IdempotencyKey, e.Err)
}

func (e *RunUnconfirmedError) Unwrap() error { return e.Err }

// AgentProtocolUnsupportedError means the Computer does not serve the public
// agent protocol. The SDK concludes it from capability discovery, so it has no
// HTTP status, and the request that led to discovery is not retried against an
// older protocol.
type AgentProtocolUnsupportedError struct{}

func (*AgentProtocolUnsupportedError) Error() string {
	return "pinesandbox: this Computer does not support the required public agent protocol; Stop this Computer, then Start to update it"
}
