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

// AttachCredentials is the per-attach mint result. KeyAssertion is required
// for v3: it is the portal-signed proof of which integrator key captures for
// this Computer, forwarded VERBATIM to the coord bind.
type AttachCredentials struct {
	BindToken       string
	BrokerGrant     string
	KeyAssertion    string
	BindingRevision int64
}

// CredentialsRequest are the coordinates for a per-attach mint.
type CredentialsRequest struct {
	ComputerID  string
	PodUID      string
	CoordBootID string
	SandboxID   string
	// PKComputer (base64url raw X25519 public key) + KeyGeneration are
	// required by v3: the portal advances its
	// per-Computer key floor and returns the signed key_assertion.
	PKComputer              string
	KeyGeneration           int
	ExpectedBindingRevision int64
	IdempotencyKey          string
	// Ephemeral mints an access-lease-only attach: no capture identity, so
	// pk_computer/key_generation are omitted and the portal returns no
	// key_assertion.
	Ephemeral bool
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
	if !req.Ephemeral && (req.PKComputer == "" || req.KeyGeneration <= 0) {
		return nil, fmt.Errorf("pinesandbox: pk_computer and positive key_generation required for v3 attach")
	}
	if req.ExpectedBindingRevision < 0 || req.IdempotencyKey == "" {
		return nil, fmt.Errorf("pinesandbox: expected binding revision and idempotency key required")
	}
	body := map[string]any{
		"pod_uid": req.PodUID, "coord_boot_id": req.CoordBootID,
		"sandbox_id":                req.SandboxID,
		"expected_binding_revision": req.ExpectedBindingRevision,
	}
	// An ephemeral (access-lease-only) attach submits no capture identity, so
	// pk_computer/key_generation are omitted entirely. It signals the mode so the
	// portal lazily creates a new Computer row as ephemeral (§14).
	if req.Ephemeral {
		body["persistence_mode"] = "ephemeral"
	} else {
		body["pk_computer"] = req.PKComputer
		body["key_generation"] = req.KeyGeneration
	}
	path := registerPath + "/" + req.ComputerID + "/attach-credentials"
	resp, err := s.postWithHeaders(ctx, path, body, map[string]string{
		"Idempotency-Key": req.IdempotencyKey,
	})
	if err != nil {
		if ae, ok := asAPIError(err); ok {
			if ae.Status == 404 {
				return nil, &UnknownComputerError{tokenBaseFrom(fmt.Sprintf("unknown, deleted, or cross-project computer_id %s", req.ComputerID), ae)}
			}
			if ae.Status == 412 && ae.ProblemType == "urn:pinesandbox:problem:binding-revision-conflict" {
				return nil, &BindingRevisionConflictError{
					tokenBase:        tokenBaseFrom("reload and adopt the winning binding", ae),
					CurrentRevision:  ae.CurrentBindingRevision,
					CurrentSandboxID: ae.CurrentSandboxID,
				}
			}
			return nil, s.generic(ae, "attach-credentials mint")
		}
		return nil, err
	}
	var out struct {
		BindToken       string `json:"bind_token"`
		BrokerGrant     string `json:"broker_grant"`
		KeyAssertion    string `json:"key_assertion"`
		BindingRevision int64  `json:"binding_revision"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, &AttachCredentialsError{s.malformedBase("attach-credentials response was not valid JSON", path, err)}
	}
	if out.BindingRevision <= req.ExpectedBindingRevision {
		return nil, &AttachCredentialsError{s.malformedBase("attach-credentials response missing an advancing binding revision", path, nil)}
	}
	// Once Portal returns an advancing revision, the authorization is committed.
	// Preserve the whole receipt—even if another required field is malformed—so
	// binder can record that revision before it rejects the incomplete v3 result.
	return &AttachCredentials{
		BindToken: out.BindToken, BrokerGrant: out.BrokerGrant,
		KeyAssertion: out.KeyAssertion, BindingRevision: out.BindingRevision,
	}, nil
}

// String is redacted: it never reveals the pk_.
func (s *AttachCredentialsSource) String() string {
	return "AttachCredentialsSource{pk_ redacted}"
}

func (s *AttachCredentialsSource) post(ctx context.Context, path string, body any) (*transport.Response, error) {
	return s.postWithHeaders(ctx, path, body, nil)
}

func (s *AttachCredentialsSource) postWithHeaders(ctx context.Context, path string, body any, extra map[string]string) (*transport.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal request: %w", err)
	}
	headers := map[string]string{"Authorization": "Bearer " + s.apiKey}
	for key, value := range extra {
		headers[key] = value
	}
	return s.client.Do(ctx, "POST", path, transport.Request{
		Accept:      "application/json",
		ContentType: "application/json",
		Body:        b,
		Headers:     headers,
		// Register is idempotent; attach carries a stable Idempotency-Key and
		// Portal replays the exact receipt. Both are safe across a lost response.
		RetryOnTransient: true,
	})
}

// generic maps a portal failure to a typed error (mirrors the Ruby raise_generic): 403 →
// ProjectAccessDenied (the project/key/computer may not mint now); everything else — a bad
// key (401), rate limit (429), or server error — → AttachCredentialsError with a status-keyed
// message. A bad pk_ stays in the AttachCredentialsError family so `errors.As` on the attach
// call keeps catching it; callers distinguish it by ae.Status == 401.
func (s *AttachCredentialsSource) generic(ae *problem.APIError, op string) error {
	var msg string
	switch ae.Status {
	case 401:
		msg = "invalid or unknown project client key"
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
