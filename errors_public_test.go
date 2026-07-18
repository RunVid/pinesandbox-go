package pinesandbox_test

import (
	"errors"
	"testing"

	pinesandbox "go.pinesandbox.io/computer"
)

// Compile from the consumer package, not the SDK's internal package. This
// prevents a refactor of the shared error base from accidentally hiding the
// Portal contract fields while internal tests continue to compile.
func TestPortalErrorMetadataIsPublic(_ *testing.T) {
	_ = func(err *pinesandbox.AttachCredentialsError) (string, string) {
		return err.Code, err.Reason
	}
}

func TestCommittedAttachRecoveryCredentialsArePublic(_ *testing.T) {
	_ = func(err *pinesandbox.AttachAuthorizationCommittedError) *pinesandbox.Credentials {
		return err.Credentials
	}
}

func TestDataPlaneErrorSemanticsArePublic(_ *testing.T) {
	_ = func(err error) bool {
		var gone *pinesandbox.SandboxGoneError
		return errors.As(err, &gone) ||
			errors.Is(err, pinesandbox.ErrSessionNotFound) ||
			errors.Is(err, pinesandbox.ErrControlNotHeld) ||
			pinesandbox.IsRetryable(err)
	}
}
