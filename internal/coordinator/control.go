package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// ---- tabs ----

// ListTabs returns the session's tabs.
func (c *Client) ListTabs(ctx context.Context, token, name string) ([]Tab, error) {
	raw, err := c.envelopeField(ctx, "GET", "/sessions/"+url.PathEscape(name)+"/tabs", token, nil, "tabs")
	if err != nil {
		return nil, err
	}
	return parseTabs(raw)
}

// CreateTab opens a tab at url and returns it.
func (c *Client) CreateTab(ctx context.Context, token, name, tabURL, label string) (*Tab, error) {
	body := map[string]any{"url": tabURL}
	if label != "" {
		body["label"] = label
	}
	raw, err := c.postEnvelopeField(ctx, "/sessions/"+url.PathEscape(name)+"/tabs", token, body, "tab")
	if err != nil {
		return nil, err
	}
	return parseTab(raw)
}

// PatchTabOptions are the tab fields to change (nil = leave unchanged).
type PatchTabOptions struct {
	Active *bool
	Label  *string
}

// PatchTab updates a tab and returns it.
func (c *Client) PatchTab(ctx context.Context, token, name, targetID string, opts PatchTabOptions) (*Tab, error) {
	body := map[string]any{}
	if opts.Active != nil {
		body["active"] = *opts.Active
	}
	if opts.Label != nil {
		body["label"] = *opts.Label
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal patch-tab: %w", err)
	}
	resp, err := c.do(ctx, "PATCH", "/sessions/"+url.PathEscape(name)+"/tabs/"+url.PathEscape(targetID), token, b)
	if err != nil {
		return nil, err
	}
	raw, err := extractField(resp.Body, "tab")
	if err != nil {
		return nil, err
	}
	return parseTab(raw)
}

// CloseTab closes a tab.
func (c *Client) CloseTab(ctx context.Context, token, name, targetID string) error {
	_, err := c.do(ctx, "DELETE", "/sessions/"+url.PathEscape(name)+"/tabs/"+url.PathEscape(targetID), token, nil)
	return err
}

// ---- control state (v1) ----

// ControlState is a control document + its ETag (used as If-Match on the next update).
type ControlState struct {
	State json.RawMessage
	ETag  string
}

// GetControl returns the session's control state + ETag.
func (c *Client) GetControl(ctx context.Context, token, name string) (*ControlState, error) {
	resp, err := c.do(ctx, "GET", "/v1/sessions/"+url.PathEscape(name)+"/control", token, nil)
	if err != nil {
		return nil, err
	}
	return &ControlState{State: json.RawMessage(resp.Body), ETag: resp.Headers.Get("ETag")}, nil
}

// PatchControlOptions configure UpdateControl. IfMatch is required unless Force.
type PatchControlOptions struct {
	IfMatch        string
	Force          bool
	IdempotencyKey string
}

// PatchControl applies a control patch and returns the new state + ETag.
func (c *Client) PatchControl(ctx context.Context, token, name string, body any, opts PatchControlOptions) (*ControlState, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal control patch: %w", err)
	}
	path := "/v1/sessions/" + url.PathEscape(name) + "/control"
	if opts.Force {
		path += "?force=true"
	}
	headers := map[string]string{}
	if opts.IfMatch != "" {
		headers["If-Match"] = opts.IfMatch
	}
	if opts.IdempotencyKey != "" {
		headers["Idempotency-Key"] = opts.IdempotencyKey
	}
	resp, err := c.send(ctx, coordReq{method: "PATCH", path: path, token: token, body: b, contentType: "application/json", headers: headers})
	if err != nil {
		return nil, err
	}
	return &ControlState{State: json.RawMessage(resp.Body), ETag: resp.Headers.Get("ETag")}, nil
}

// ControlNotify posts a control notification (If-Match required).
func (c *Client) ControlNotify(ctx context.Context, token, name, reason, detail, ifMatch string) error {
	body := map[string]any{"reason": reason}
	if detail != "" {
		body["detail"] = detail
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("pinesandbox: marshal control notify: %w", err)
	}
	headers := map[string]string{}
	if ifMatch != "" {
		headers["If-Match"] = ifMatch
	}
	_, err = c.send(ctx, coordReq{method: "POST", path: "/v1/sessions/" + url.PathEscape(name) + "/control/notify", token: token, body: b, contentType: "application/json", headers: headers})
	return err
}

// DesktopToken is a short-TTL browser-safe VNC desktop-stream token (dt_).
type DesktopToken struct {
	Token     string
	ExpiresAt string
}

// MintDesktopToken mints a dt_ for the session (ct_-only).
func (c *Client) MintDesktopToken(ctx context.Context, token, name string) (*DesktopToken, error) {
	resp, err := c.do(ctx, "POST", "/v1/sessions/"+url.PathEscape(name)+"/desktop-token", token, emptyJSON)
	if err != nil {
		return nil, err
	}
	var w struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(resp.Body, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable desktop token: %w", err)
	}
	return &DesktopToken{Token: w.Token, ExpiresAt: w.ExpiresAt}, nil
}

// ---- handoffs ----

// ListHandoffs lists control handoffs (raw "handoffs" array). limit<=0 / before="" omit.
func (c *Client) ListHandoffs(ctx context.Context, token, name string, limit int, before string) (json.RawMessage, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	if before != "" {
		q.Set("before", before)
	}
	path := "/v1/sessions/" + url.PathEscape(name) + "/handoffs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return c.envelopeField(ctx, "GET", path, token, nil, "handoffs")
}

// GetHandoff returns one handoff (raw).
func (c *Client) GetHandoff(ctx context.Context, token, name, handoffID string) (json.RawMessage, error) {
	resp, err := c.do(ctx, "GET", "/v1/sessions/"+url.PathEscape(name)+"/handoffs/"+url.PathEscape(handoffID), token, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(resp.Body), nil
}

// ControlEvents is a typed, resuming iterator — see stream.go.

// ---- helpers ----

func (c *Client) envelopeField(ctx context.Context, method, path, token string, body []byte, field string) (json.RawMessage, error) {
	resp, err := c.do(ctx, method, path, token, body)
	if err != nil {
		return nil, err
	}
	return extractField(resp.Body, field)
}

func (c *Client) postEnvelopeField(ctx context.Context, path, token string, body any, field string) (json.RawMessage, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal body: %w", err)
	}
	return c.envelopeField(ctx, "POST", path, token, b, field)
}

func extractField(body []byte, field string) (json.RawMessage, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable response: %w", err)
	}
	v, ok := env[field]
	if !ok {
		return nil, fmt.Errorf("pinesandbox: response missing %q field", field)
	}
	return v, nil
}
