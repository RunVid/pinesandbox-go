package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

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

// Create provisions a pod. body is the sandbox-lifecycle create payload (assembled by the
// caller); idempotencyKey is sent as Idempotency-Key when non-empty. Returns the parsed
// SandboxInfo (202 async). No blind retry — a create may have applied; only the safe
// 401 → token-refresh is retried.
func (c *Client) Create(ctx context.Context, body any, idempotencyKey string) (*SandboxInfo, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal create payload: %w", err)
	}
	extra := map[string]string{}
	if idempotencyKey != "" {
		extra["Idempotency-Key"] = idempotencyKey
	}
	// A keyed create is safe to retry on a transient fault (the server dedupes the
	// replay); a keyless one is NOT (a reset may have applied → double-provision).
	resp, err := c.do(ctx, "POST", "/sandboxes", b, extra, idempotencyKey != "")
	if err != nil {
		return nil, err
	}
	if !okStatus(resp.Status, 200, 201, 202) {
		return nil, statusError(resp.Status, resp.Body)
	}
	return ParseSandboxInfo(resp.Body)
}

// Get fetches a sandbox by id.
func (c *Client) Get(ctx context.Context, sandboxID string) (*SandboxInfo, error) {
	resp, err := c.do(ctx, "GET", "/sandboxes/"+url.PathEscape(sandboxID), nil, nil, false)
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 {
		return nil, statusError(resp.Status, resp.Body)
	}
	return ParseSandboxInfo(resp.Body)
}

// Destroy deletes a sandbox. Idempotent: a 404 (already gone) is success.
func (c *Client) Destroy(ctx context.Context, sandboxID string) error {
	resp, err := c.do(ctx, "DELETE", "/sandboxes/"+url.PathEscape(sandboxID), nil, nil, false)
	if err != nil {
		return err
	}
	if !okStatus(resp.Status, 200, 202, 204, 404) {
		return statusError(resp.Status, resp.Body)
	}
	return nil
}

// Pause freezes the pod in place (state retained on the same pod, NOT broker capture — so
// resume is fast + same-pod). Returns false if the sandbox was already paused / not in a
// pausable state (409); any other non-OK status is an error.
func (c *Client) Pause(ctx context.Context, sandboxID string) (bool, error) {
	return c.transition(ctx, sandboxID, "pause")
}

// Resume unfreezes a paused pod. Returns false on a 409 (not paused).
func (c *Client) Resume(ctx context.Context, sandboxID string) (bool, error) {
	return c.transition(ctx, sandboxID, "resume")
}

func (c *Client) transition(ctx context.Context, sandboxID, verb string) (bool, error) {
	resp, err := c.do(ctx, "POST", "/sandboxes/"+url.PathEscape(sandboxID)+"/"+verb, nil, nil, false)
	if err != nil {
		return false, err
	}
	if resp.Status == 409 {
		return false, nil
	}
	if !okStatus(resp.Status, 200, 202, 204) {
		return false, statusError(resp.Status, resp.Body)
	}
	return true, nil
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
