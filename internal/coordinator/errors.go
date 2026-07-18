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
