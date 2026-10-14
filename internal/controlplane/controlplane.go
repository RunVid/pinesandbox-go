package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"go.pinesandbox.io/computer/internal/base/spec"
	"go.pinesandbox.io/computer/internal/base/transport"
)

// TokenSource yields the project JWS for the Authorization bearer. forceRefresh re-mints
// (used after a 401). Satisfied by *tokens.ControlTokenSource; an interface so this layer
// stays decoupled from the concrete source (and is trivially faked in tests).
type TokenSource interface {
	Token(ctx context.Context, forceRefresh bool) (string, error)
}

// Client is the control-plane client. Construct it with NewClient.
type Client struct {
	raw  *transport.Client
	src  TokenSource
	spec spec.Negotiator
}

// NewClient builds a control-plane client over raw (pointed at the control host),
// authenticating with src and negotiating the given spec-version major.
func NewClient(raw *transport.Client, src TokenSource, specMajor int) *Client {
	return &Client{
		raw: raw,
		src: src,
		spec: spec.Negotiator{
			RequestHeader:  "Computer-Spec-Version",
			ResponseHeader: "X-Computer-Spec-Version",
			SupportedMajor: specMajor,
		},
	}
}

// CreateComputer provisions a Computer sandbox. The body intentionally contains
// only caller-owned settings; the lifecycle server expands the project's signed
// runtime policy into the operator-managed platform configuration.
func (c *Client) CreateComputer(ctx context.Context, body map[string]any) (*SandboxInfo, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal create payload: %w", err)
	}
	const path = "/computer-sandboxes"
	// A create ID permits identical retries without extending runtime expiry.
	createID, _ := body["create_id"].(string)
	resp, err := c.do(ctx, "POST", path, b, nil, createID != "")
	if err != nil {
		return nil, err
	}
	if !okStatus(resp.Status, 200, 201, 202) {
		return nil, statusError("POST", path, c.raw.Host(), resp)
	}
	return ParseSandboxInfo(resp.Body)
}

// Stop admits a durable save-before-release operation. Acceptance is not completion.
func (c *Client) Stop(ctx context.Context, sandboxID, operationID string) error {
	path := "/sandboxes/" + url.PathEscape(sandboxID) + "/stop"
	body, err := json.Marshal(map[string]string{"id": operationID})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, "POST", path, body, nil, true)
	if err != nil {
		return err
	}
	if !okStatus(resp.Status, 202, 404) {
		return statusError("POST", path, c.raw.Host(), resp)
	}
	return nil
}

// Get fetches a sandbox by id.
func (c *Client) Get(ctx context.Context, sandboxID string) (*SandboxInfo, error) {
	path := "/sandboxes/" + url.PathEscape(sandboxID)
	resp, err := c.do(ctx, "GET", path, nil, nil, false)
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 {
		return nil, statusError("GET", path, c.raw.Host(), resp)
	}
	info, err := ParseSandboxInfo(resp.Body)
	if err == nil {
		info.ETag = resp.Headers.Get("ETag")
	}
	return info, err
}

// WaitForChange observes the last version. The server owns the hold time;
// the caller's deadline bounds the request independently.
func (c *Client) WaitForChange(ctx context.Context, sandboxID string, previous *SandboxInfo) (*SandboxInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	path := "/sandboxes/" + url.PathEscape(sandboxID)
	resp, err := c.do(ctx, "GET", path, nil, map[string]string{"If-None-Match": previous.ETag}, true)
	if err != nil {
		return nil, err
	}
	if resp.Status == 304 {
		return previous, nil
	}
	if resp.Status != 200 {
		return nil, statusError("GET", path, c.raw.Host(), resp)
	}
	info, err := ParseSandboxInfo(resp.Body)
	if err == nil {
		info.ETag = resp.Headers.Get("ETag")
	}
	return info, err
}

// Destroy deletes a sandbox. Idempotent: a 404 (already gone) is success.
func (c *Client) Destroy(ctx context.Context, sandboxID string) error {
	path := "/sandboxes/" + url.PathEscape(sandboxID)
	resp, err := c.do(ctx, "DELETE", path, nil, nil, false)
	if err != nil {
		return err
	}
	if !okStatus(resp.Status, 200, 202, 204, 404) {
		return statusError("DELETE", path, c.raw.Host(), resp)
	}
	return nil
}

// Release deletes exactly the observed incarnation and lifecycle version.
func (c *Client) Release(ctx context.Context, sandboxID, etag string) error {
	path := "/sandboxes/" + url.PathEscape(sandboxID)
	resp, err := c.do(ctx, "DELETE", path, nil, map[string]string{"If-Match": etag}, false)
	if err != nil {
		return err
	}
	if !okStatus(resp.Status, 204, 404) {
		return statusError("DELETE", path, c.raw.Host(), resp)
	}
	return nil
}

// Pause requests retained memory capture. Acceptance is not completion.
func (c *Client) Pause(ctx context.Context, sandboxID string) error {
	return c.transition(ctx, sandboxID, "pause")
}

// Resume requests restoration of retained memory. Acceptance is not completion.
func (c *Client) Resume(ctx context.Context, sandboxID string) error {
	return c.transition(ctx, sandboxID, "resume")
}

func (c *Client) transition(ctx context.Context, sandboxID, verb string) error {
	path := "/sandboxes/" + url.PathEscape(sandboxID) + "/" + verb
	resp, err := c.do(ctx, "POST", path, nil, nil, false)
	if err != nil {
		return err
	}
	if !okStatus(resp.Status, 200, 202, 204) {
		return statusError("POST", path, c.raw.Host(), resp)
	}
	return nil
}

// do sends the request with the project JWS; on a 401 it forces ONE token refresh and
// retries once (a 401 means the request was rejected, not applied, so the retry is safe
// even for create). The spec-version of every response is validated in send.
func (c *Client) do(ctx context.Context, method, path string, body []byte, extra map[string]string, retryOnTransient bool) (*transport.Response, error) {
	resp, err := c.send(ctx, method, path, body, extra, false, retryOnTransient)
	if err != nil {
		return nil, err
	}
	if resp.Status != 401 {
		return resp, nil
	}
	return c.send(ctx, method, path, body, extra, true, retryOnTransient)
}

func (c *Client) send(ctx context.Context, method, path string, body []byte, extra map[string]string, forceRefresh, retryOnTransient bool) (*transport.Response, error) {
	tok, err := c.src.Token(ctx, forceRefresh)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"Authorization":      "Bearer " + tok,
		c.spec.RequestHeader: c.spec.RequestValue(),
	}
	for k, v := range extra {
		headers[k] = v
	}
	r := transport.Request{Accept: "application/json", Headers: headers, RetryOnTransient: retryOnTransient}
	if body != nil {
		r.Body = body
		r.ContentType = "application/json"
	}
	resp, err := c.raw.DoRaw(ctx, method, path, r)
	if err != nil {
		return nil, err
	}
	// The spec-version contract rides every response (mirrors the Ruby middleware).
	if err := c.spec.Check(resp.Headers.Get(c.spec.ResponseHeader)); err != nil {
		return nil, err
	}
	return resp, nil
}

func okStatus(got int, allowed ...int) bool {
	for _, a := range allowed {
		if got == a {
			return true
		}
	}
	return false
}
