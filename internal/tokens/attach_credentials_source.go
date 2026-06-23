package tokens

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/transport"
)

// AttachCredentialsSource is the pk_-backed attach-credential provider (the default
// portal-as-issuer path). It exchanges the project client key at the portal for per-attach
// credentials; it caches nothing — each call is a fresh mint (the single-use bind_token is
// 5 minutes, scoped to one pod + boot). The pk_ never appears in String(). Mirrors the Ruby
// AttachCredentialsSource. Reuses internal/base/transport (Authorization: Bearer pk_).
type AttachCredentialsSource struct {
	client *transport.Client
	apiKey string
}

const registerPath = "/v1/computers"

// NewAttachCredentialsSource builds a source posting to client (the portal/control host).
func NewAttachCredentialsSource(client *transport.Client, apiKey string) (*AttachCredentialsSource, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("pinesandbox: api_key required (a pk_ client key)")
	}
	if client == nil {
		return nil, fmt.Errorf("pinesandbox: attach-credentials transport client required")
	}
	return &AttachCredentialsSource{client: client, apiKey: apiKey}, nil
}

// AttachCredentials is the per-attach mint result.
type AttachCredentials struct {
	BindToken   string
	BrokerGrant string
}

// GrantRefresh is the §6.4 mid-life grant-refresh result.
type GrantRefresh struct {
	BrokerGrant  string
	RefreshToken string
}

// CredentialsRequest are the coordinates for a per-attach mint.
type CredentialsRequest struct {
	ComputerID  string
	PodUID      string
	CoordBootID string
	SandboxID   string
	Profile     string // optional
	TTLSeconds  *int   // optional sizing hint
}

// GrantRefreshRequest are the coordinates for a grant refresh.
type GrantRefreshRequest struct {
	ComputerID  string
	PodUID      string
	CoordBootID string
	TTLSeconds  *int // optional
}

// RegisterComputer registers computer_id into the portal ownership registry (idempotent
// for the same project; 409/422 for a cross-project duplicate).
func (s *AttachCredentialsSource) RegisterComputer(ctx context.Context, computerID string) error {
	if computerID == "" {
		return fmt.Errorf("pinesandbox: computer_id required")
	}
	_, err := s.post(ctx, registerPath, map[string]string{"computer_id": computerID})
	if err == nil {
		return nil
	}
	ae, ok := asAPIError(err)
	if !ok {
		return err // transport fault — already typed
	}
	switch {
	case ae.Status == 409 || ae.Status == 422:
		return &ComputerRegistrationError{tokenBase{fmt.Sprintf("computer registration refused (%d)", ae.Status), ae.Status, ae.RequestID, ae}}
	default:
		return s.generic(ae, "computer registration")
	}
}

// Credentials mints the per-attach bind_token + broker_grant for one pod + boot.
func (s *AttachCredentialsSource) Credentials(ctx context.Context, req CredentialsRequest) (*AttachCredentials, error) {
	if req.ComputerID == "" || req.PodUID == "" || req.CoordBootID == "" || req.SandboxID == "" {
		return nil, fmt.Errorf("pinesandbox: computer_id, pod_uid, coord_boot_id, sandbox_id all required")
	}
	body := map[string]any{"pod_uid": req.PodUID, "coord_boot_id": req.CoordBootID, "sandbox_id": req.SandboxID}
	if req.Profile != "" {
		body["profile"] = req.Profile
	}
	if req.TTLSeconds != nil {
		body["ttl_seconds"] = *req.TTLSeconds
	}
	resp, err := s.post(ctx, registerPath+"/"+req.ComputerID+"/attach-credentials", body)
	if err != nil {
		if ae, ok := asAPIError(err); ok {
			if ae.Status == 404 {
				return nil, &UnknownComputerError{tokenBase{fmt.Sprintf("unknown, deleted, or cross-project computer_id %s — register it first", req.ComputerID), 404, ae.RequestID, ae}}
			}
			return nil, s.generic(ae, "attach-credentials mint")
		}
		return nil, err
	}
	var out struct {
		BindToken   string `json:"bind_token"`
		BrokerGrant string `json:"broker_grant"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, &AttachCredentialsError{tokenBase{Msg: "attach-credentials response was not valid JSON", Status: 200, Cause: err}}
	}
	if out.BindToken == "" || out.BrokerGrant == "" {
		return nil, &AttachCredentialsError{tokenBase{Msg: "attach-credentials response missing bind_token/broker_grant", Status: 200}}
	}
	return &AttachCredentials{BindToken: out.BindToken, BrokerGrant: out.BrokerGrant}, nil
}

// GrantRefresh mints a fresh {broker_grant, refresh_token} for the §6.4 mid-life refresh.
func (s *AttachCredentialsSource) GrantRefresh(ctx context.Context, req GrantRefreshRequest) (*GrantRefresh, error) {
	if req.ComputerID == "" || req.PodUID == "" || req.CoordBootID == "" {
		return nil, fmt.Errorf("pinesandbox: computer_id, pod_uid, coord_boot_id all required")
	}
	body := map[string]any{"pod_uid": req.PodUID, "coord_boot_id": req.CoordBootID}
	if req.TTLSeconds != nil {
		body["ttl_seconds"] = *req.TTLSeconds
	}
	resp, err := s.post(ctx, registerPath+"/"+req.ComputerID+"/grant-refresh", body)
	if err != nil {
		if ae, ok := asAPIError(err); ok {
			if ae.Status == 404 {
				return nil, &UnknownComputerError{tokenBase{fmt.Sprintf("unknown, deleted, or cross-project computer_id %s", req.ComputerID), 404, ae.RequestID, ae}}
			}
			return nil, s.generic(ae, "grant-refresh mint")
		}
		return nil, err
	}
	var out struct {
		BrokerGrant  string `json:"broker_grant"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, &AttachCredentialsError{tokenBase{Msg: "grant-refresh response was not valid JSON", Status: 200, Cause: err}}
	}
	if out.BrokerGrant == "" || out.RefreshToken == "" {
		return nil, &AttachCredentialsError{tokenBase{Msg: "grant-refresh response missing broker_grant/refresh_token", Status: 200}}
	}
	return &GrantRefresh{BrokerGrant: out.BrokerGrant, RefreshToken: out.RefreshToken}, nil
}

// String is redacted: it never reveals the pk_.
func (s *AttachCredentialsSource) String() string {
	return "AttachCredentialsSource{pk_ redacted}"
}

func (s *AttachCredentialsSource) post(ctx context.Context, path string, body any) (*transport.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal request: %w", err)
	}
	return s.client.Do(ctx, "POST", path, transport.Request{
		Accept:      "application/json",
		ContentType: "application/json",
		Body:        b,
		Headers:     map[string]string{"Authorization": "Bearer " + s.apiKey},
		// register + credential/grant mints are idempotent → safe to retry on a
		// transient connection fault (re-minting yields a fresh, harmless result).
		RetryOnTransient: true,
	})
}

// generic maps a non-specific portal failure to AttachCredentialsError with a status-keyed
// default message (mirrors the Ruby raise_generic).
func (s *AttachCredentialsSource) generic(ae *problem.APIError, op string) error {
	var msg string
	switch ae.Status {
	case 401:
		msg = "invalid or unknown project client key"
	case 403:
		msg = "this key may not mint attach credentials (scope or computer status)"
	case 429:
		msg = "portal rate-limited the " + op
	default:
		msg = fmt.Sprintf("portal %s failed (%d)", op, ae.Status)
	}
	return &AttachCredentialsError{tokenBase{msg, ae.Status, ae.RequestID, ae}}
}

func asAPIError(err error) (*problem.APIError, bool) {
	var ae *problem.APIError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}
