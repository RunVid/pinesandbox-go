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

// CredentialsRequest are the coordinates for a per-attach mint.
type CredentialsRequest struct {
	ComputerID  string
	PodUID      string
	CoordBootID string
	SandboxID   string
	Profile     string // optional
	TTLSeconds  *int   // optional sizing hint
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
		return &ComputerRegistrationError{tokenBaseFrom(fmt.Sprintf("computer registration refused (%d)", ae.Status), ae)}
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
	path := registerPath + "/" + req.ComputerID + "/attach-credentials"
	resp, err := s.post(ctx, path, body)
	if err != nil {
		if ae, ok := asAPIError(err); ok {
			if ae.Status == 404 {
				return nil, &UnknownComputerError{tokenBaseFrom(fmt.Sprintf("unknown, deleted, or cross-project computer_id %s — register it first", req.ComputerID), ae)}
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
		return nil, &AttachCredentialsError{s.malformedBase("attach-credentials response was not valid JSON", path, err)}
	}
	if out.BindToken == "" || out.BrokerGrant == "" {
		return nil, &AttachCredentialsError{s.malformedBase("attach-credentials response missing bind_token/broker_grant", path, nil)}
	}
	return &AttachCredentials{BindToken: out.BindToken, BrokerGrant: out.BrokerGrant}, nil
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
		return &InvalidClientKey{tokenBaseFrom("invalid or unknown project client key", ae)}
	case 403:
		return &ProjectAccessDenied{tokenBaseFrom("project or key may not mint attach credentials (scope, project status, or computer status)", ae)}
	case 429:
		msg = "portal rate-limited the " + op
	default:
		msg = fmt.Sprintf("portal %s failed (%d)", op, ae.Status)
	}
	return &AttachCredentialsError{tokenBaseFrom(msg, ae)}
}

// malformedBase builds the tokenBase for a 200-but-unusable mint response: no request id to
// wrap, but the known operation (path) + host still name WHICH portal call this was.
func (s *AttachCredentialsSource) malformedBase(msg, path string, cause error) tokenBase {
	return tokenBase{Msg: msg, Status: 200, Host: s.client.Host(), Op: transport.Operation("POST", path), Cause: cause}
}

func asAPIError(err error) (*problem.APIError, bool) {
	var ae *problem.APIError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}
