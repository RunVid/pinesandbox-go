package pinesandbox_test

import (
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
