package pinesandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"go.pinesandbox.io/computer/internal/base/transport"
)

// DiagnosticOptions requests a read-only check of an existing Computer.
// No private Computer keys or live handle are needed. Description and RequestID
// are private support context. Session requests an investigation of its recorded work.
type DiagnosticOptions struct {
	ComputerID  string `json:"computer_id"`
	Session     string `json:"session,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	Description string `json:"description,omitempty"`
	// Reuse the key with the same options after an uncertain response.
	// An omitted key is generated once per invocation, including transport retries.
	IdempotencyKey string `json:"-"`
}

// Diagnostic is a saved support reference, not a human support ticket.
// State is queued, running, completed, or failed. Assessment is nil while pending.
type Diagnostic struct {
	ID          string                `json:"id"`
	ComputerID  string                `json:"computer_id"`
	State       string                `json:"state"`
	CreatedAt   time.Time             `json:"created_at"`
	ExpiresAt   time.Time             `json:"expires_at"`
	CompletedAt *time.Time            `json:"completed_at"`
	Assessment  *DiagnosticAssessment `json:"assessment"`
}

type DiagnosticAssessment struct {
	Code     string             `json:"code"`
	Message  string             `json:"message"`
	Evidence string             `json:"evidence"`
	Actions  []DiagnosticAction `json:"actions"`
}

type DiagnosticAction struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var diagnosticIDRE = regexp.MustCompile(`^diag_[0-9a-f]{16}$`)

// CreateDiagnostic saves a request and returns immediately. It never repeats
// customer work, restarts the Computer, or opens a human support ticket.
func (c *Client) CreateDiagnostic(ctx context.Context, opts DiagnosticOptions) (*Diagnostic, error) {
	if !computerIDRE.MatchString(opts.ComputerID) {
		return nil, fmt.Errorf("pinesandbox: valid Computer ID required")
	}
	key := opts.IdempotencyKey
	if key == "" {
		var err error
		key, err = uuidV7()
		if err != nil {
			return nil, err
		}
	}
	body, err := json.Marshal(opts)
	if err != nil {
		return nil, err
	}
	return c.requestDiagnostic(ctx, http.MethodPost, "/v1/diagnostics", transport.Request{
		Body: body, ContentType: "application/json", RetryOnTransient: true,
		Headers: map[string]string{"Idempotency-Key": key},
	})
}

// GetDiagnostic reads the saved result. Reading never schedules another check.
func (c *Client) GetDiagnostic(ctx context.Context, id string) (*Diagnostic, error) {
	if !diagnosticIDRE.MatchString(id) {
		return nil, fmt.Errorf("pinesandbox: valid diagnostic reference required")
	}
	return c.requestDiagnostic(ctx, http.MethodGet, "/v1/diagnostics/"+id, transport.Request{})
}

func (c *Client) requestDiagnostic(ctx context.Context, method, path string, req transport.Request) (*Diagnostic, error) {
	if c.projectKey == "" {
		return nil, fmt.Errorf("pinesandbox: diagnostics need the project key (ClientOptions.APIKey)")
	}
	if req.Headers == nil {
		req.Headers = make(map[string]string)
	}
	req.Headers["Authorization"] = "Bearer " + c.projectKey
	response, err := c.portalTransport.Do(ctx, method, path, req)
	if err != nil {
		return nil, err
	}
	var result Diagnostic
	if err := json.Unmarshal(response.Body, &result); err != nil {
		return nil, fmt.Errorf("pinesandbox: invalid diagnostic response (request_id=%s)", response.Headers.Get("X-Request-Id"))
	}
	if !diagnosticIDRE.MatchString(result.ID) || result.State == "" {
		return nil, fmt.Errorf("pinesandbox: incomplete diagnostic response (request_id=%s)", response.Headers.Get("X-Request-Id"))
	}
	return &result, nil
}
