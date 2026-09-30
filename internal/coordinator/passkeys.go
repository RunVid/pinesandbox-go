package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// PasskeyCredential is the metadata-only projection of a durable custodial
// credential. Private key material never crosses the coordinator API.
type PasskeyCredential struct {
	ID              string     `json:"id"`
	RPID            string     `json:"rp_id"`
	UserHandle      string     `json:"user_handle"`
	UserName        string     `json:"user_name,omitempty"`
	UserDisplayName string     `json:"user_display_name,omitempty"`
	ApprovedBy      string     `json:"approved_by,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
}

// PasskeyCeremony is a pending custodial enrollment awaiting owner consent.
type PasskeyCeremony struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	RPID            string     `json:"rp_id"`
	Origin          string     `json:"origin"`
	UserName        string     `json:"user_name,omitempty"`
	UserDisplayName string     `json:"user_display_name,omitempty"`
	Session         string     `json:"session,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	PresentedAt     *time.Time `json:"presented_at,omitempty"`
}

func (c *Client) ListPasskeys(ctx context.Context, token string) ([]PasskeyCredential, error) {
	raw, err := c.getJSON(ctx, "/v1/passkeys", token)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Credentials []PasskeyCredential `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable passkey list: %w", err)
	}
	if envelope.Credentials == nil {
		envelope.Credentials = []PasskeyCredential{}
	}
	return envelope.Credentials, nil
}

func (c *Client) DeletePasskey(ctx context.Context, token, credentialID string) error {
	_, err := c.do(ctx, "DELETE", "/v1/passkeys/"+url.PathEscape(credentialID), token, nil)
	return err
}

func (c *Client) ListPasskeyCeremonies(ctx context.Context, token string) ([]PasskeyCeremony, error) {
	return c.listPasskeyCeremonies(ctx, "/v1/passkeys/ceremonies", token)
}

func (c *Client) ListSessionPasskeyCeremonies(ctx context.Context, token, name string) ([]PasskeyCeremony, error) {
	return c.listPasskeyCeremonies(ctx, "/v1/sessions/"+url.PathEscape(name)+"/passkeys/ceremonies", token)
}

func (c *Client) listPasskeyCeremonies(ctx context.Context, path, token string) ([]PasskeyCeremony, error) {
	raw, err := c.getJSON(ctx, path, token)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Ceremonies []PasskeyCeremony `json:"ceremonies"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable passkey ceremony list: %w", err)
	}
	if envelope.Ceremonies == nil {
		envelope.Ceremonies = []PasskeyCeremony{}
	}
	return envelope.Ceremonies, nil
}

func (c *Client) DecidePasskeyCeremony(ctx context.Context, token, ceremonyID, decision string) error {
	_, err := c.postJSON(ctx, "/v1/passkeys/ceremonies/"+url.PathEscape(ceremonyID), token, map[string]string{"decision": decision})
	return err
}

func (c *Client) DeclarePasskeyIntent(ctx context.Context, token, name, rpID string) error {
	_, err := c.postJSON(ctx, "/v1/sessions/"+url.PathEscape(name)+"/passkeys/intents", token, map[string]string{"rp_id": rpID})
	return err
}

func (c *Client) DecideSessionPasskeyCeremony(ctx context.Context, token, name, ceremonyID, decision string) error {
	_, err := c.postJSON(ctx, "/v1/sessions/"+url.PathEscape(name)+"/passkeys/ceremonies/"+url.PathEscape(ceremonyID), token, map[string]string{"decision": decision})
	return err
}
