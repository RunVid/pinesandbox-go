package pinesandbox

import (
	"context"
	"fmt"

	"go.pinesandbox.io/computer/internal/coordinator"
)

type PasskeyCredential = coordinator.PasskeyCredential
type PasskeyCeremony = coordinator.PasskeyCeremony

const (
	PasskeyDecisionApproveCustodial = "approve_custodial"
	PasskeyDecisionDecline          = "decline"
)

// ListPasskeys returns metadata for durable custodial passkeys held by this
// Computer. Private key material remains inside the coordinator and encrypted
// state component.
func (c *Computer) ListPasskeys(ctx context.Context) ([]PasskeyCredential, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ListPasskeys(ctx, ct)
}

// DeletePasskey durably deletes a custodial credential. Remove the credential
// at the relying party first whenever possible.
func (c *Computer) DeletePasskey(ctx context.Context, credentialID string) error {
	if credentialID == "" {
		return fmt.Errorf("pinesandbox: passkey credential ID is required")
	}
	coord, ct, err := c.bound()
	if err != nil {
		return err
	}
	return coord.DeletePasskey(ctx, ct, credentialID)
}

// PendingPasskeyCeremonies lists non-expired enrollments awaiting owner consent.
func (c *Computer) PendingPasskeyCeremonies(ctx context.Context) ([]PasskeyCeremony, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ListPasskeyCeremonies(ctx, ct)
}

// ApprovePasskeyCeremony saves a pending Pine-custodial credential.
func (c *Computer) ApprovePasskeyCeremony(ctx context.Context, ceremonyID string) error {
	return c.decidePasskeyCeremony(ctx, ceremonyID, PasskeyDecisionApproveCustodial)
}

// DeclinePasskeyCeremony rejects a pending Pine-custodial enrollment.
func (c *Computer) DeclinePasskeyCeremony(ctx context.Context, ceremonyID string) error {
	return c.decidePasskeyCeremony(ctx, ceremonyID, PasskeyDecisionDecline)
}

func (c *Computer) decidePasskeyCeremony(ctx context.Context, ceremonyID, decision string) error {
	if ceremonyID == "" {
		return fmt.Errorf("pinesandbox: passkey ceremony ID is required")
	}
	coord, ct, err := c.bound()
	if err != nil {
		return err
	}
	return coord.DecidePasskeyCeremony(ctx, ct, ceremonyID, decision)
}

// DeclarePasskeyIntent attributes the next matching enrollment to this agent
// session. It grants no consent; use the session-scoped decision methods after
// the site starts WebAuthn.
func (s *Session) DeclarePasskeyIntent(ctx context.Context, rpID string) error {
	if rpID == "" {
		return fmt.Errorf("pinesandbox: passkey RP ID is required")
	}
	return s.coord.DeclarePasskeyIntent(ctx, s.token, s.name, rpID)
}

// PendingPasskeyCeremonies returns non-secret first-enrollment decisions
// attributed to this agent session.
func (s *Session) PendingPasskeyCeremonies(ctx context.Context) ([]PasskeyCeremony, error) {
	return s.coord.ListSessionPasskeyCeremonies(ctx, s.token, s.name)
}

// ApprovePasskeyCeremony saves an enrollment attributed to this agent session.
func (s *Session) ApprovePasskeyCeremony(ctx context.Context, ceremonyID string) error {
	return s.decidePasskeyCeremony(ctx, ceremonyID, PasskeyDecisionApproveCustodial)
}

// DeclinePasskeyCeremony rejects an enrollment attributed to this agent session.
func (s *Session) DeclinePasskeyCeremony(ctx context.Context, ceremonyID string) error {
	return s.decidePasskeyCeremony(ctx, ceremonyID, PasskeyDecisionDecline)
}

func (s *Session) decidePasskeyCeremony(ctx context.Context, ceremonyID, decision string) error {
	if ceremonyID == "" {
		return fmt.Errorf("pinesandbox: passkey ceremony ID is required")
	}
	return s.coord.DecideSessionPasskeyCeremony(ctx, s.token, s.name, ceremonyID, decision)
}
