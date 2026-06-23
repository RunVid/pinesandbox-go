// Package transport is the HTTP client one host (a coord data host or the control-plane
// host) talks through. It carries the bearer as X-Pine-Auth (coord reads it; the gateway
// forwards it unchanged), decodes non-2xx into a typed *problem.APIError, captures the
// request-id, and normalizes transport faults into typed *TimeoutError / *ConnectionError
// so an integrator never sees a raw net/url error. Generic base primitive (internal/base)
// — Computer-agnostic, must not import a domain package (§3).
package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"time"

	"go.pinesandbox.io/computer/internal/base/problem"
)

// defaultTimeout matches the Ruby reference's unary HTTP timeout. Streaming uses a
// separate client with NO total timeout (an SSE stream is long-lived; cancel it via the
// context).
const defaultTimeout = 30 * time.Second

// streamHeaderTimeout floors how long the streaming client waits for the response HEADERS.
// The stream itself is long-lived (no total timeout), but a server that accepts the TCP
// connection and never sends a response would otherwise leak a goroutine + connection
// forever when the caller passes a deadline-less context. Headers arrive immediately on a
// healthy SSE stream, so this never affects one.
const streamHeaderTimeout = 60 * time.Second

// defaultMaxAttempts is the total send count for a retry-eligible request (1 try + 2
// retries) on a transient transport fault. Matches the Ruby SDK's transient-retry budget.
const defaultMaxAttempts = 3

// Client issues unary requests against scheme://host.
type Client struct {
	base        string
	hc          *http.Client
	streamHC    *http.Client
	maxAttempts int
	backoff     func(attempt int) time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient injects a custom *http.Client (timeouts, transport, test doubles). The
// streaming client reuses its Transport but drops the total timeout.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithRetry overrides the transient-retry budget + backoff (tests inject a zero backoff so
// they don't actually sleep). attempts < 1 disables retry.
func WithRetry(attempts int, backoff func(attempt int) time.Duration) Option {
	return func(c *Client) { c.maxAttempts = attempts; c.backoff = backoff }
}

// defaultBackoff is bounded exponential with small jitter, capped at 2s (mirrors the Ruby
// SDK's transient_backoff: 0.2·2^(n-1) + jitter, cap 2s).
func defaultBackoff(attempt int) time.Duration {
	base := 0.2 * math.Pow(2, float64(attempt-1))
	d := base + rand.Float64()*0.1
	if d > 2.0 {
		d = 2.0
	}
	return time.Duration(d * float64(time.Second))
}

// New builds a Client for scheme://host (host may include a local-dev port).
func New(scheme, host string, opts ...Option) *Client {
	c := &Client{
		base:        scheme + "://" + host,
		hc:          &http.Client{Timeout: defaultTimeout},
		maxAttempts: defaultMaxAttempts,
		backoff:     defaultBackoff,
	}
	for _, o := range opts {
		o(c)
	}
	// Streaming reuses the unary client's Transport (TLS/proxy config) but drops the total
	// timeout — an SSE stream is long-lived and cancelled via context. It adds a
	// response-header-timeout floor so a connect-but-never-respond server can't leak forever.
	c.streamHC = &http.Client{Transport: streamTransport(c.hc.Transport)}
	return c
}

// streamTransport clones the base *http.Transport (or the default) and adds a
// ResponseHeaderTimeout floor, without mutating the shared/default transport. A non-
// *http.Transport RoundTripper is used as-is (no floor — rare; custom test/proxy doubles).
func streamTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if t, ok := base.(*http.Transport); ok {
		ct := t.Clone()
		ct.ResponseHeaderTimeout = streamHeaderTimeout
		return ct
	}
	return base
}

// Host returns the host[:port] this client targets (the scheme stripped) — the public
// gateway host in production.
func (c *Client) Host() string {
	if i := strings.Index(c.base, "://"); i >= 0 {
		return c.base[i+3:]
	}
	return c.base
}

// BaseURL returns the full scheme://host[:port] origin this client targets — the public
// gateway origin in production. Used to build the browser-safe DelegatedConnection, whose
// computer_host is a full URI per computer-api.yaml (so the web SDK derives the desktop's
// ws/wss scheme from it rather than guessing).
func (c *Client) BaseURL() string { return c.base }

// Request is one unary call. Token "" omits X-Pine-Auth (bind-pubkey/health/metrics are
// token-less). Headers carries extras (Idempotency-Key, If-Match, …).
type Request struct {
	Token       string
	Accept      string // default "application/json"
	ContentType string
	Body        []byte
	Headers     map[string]string
	// RetryOnTransient opts a NON-idempotent method (POST/PATCH) into the bounded
	// transient-fault retry. Idempotent methods (GET/DELETE/HEAD) retry automatically;
	// set this only for a POST/PATCH the caller knows is safe to replay — an idempotent
	// mint/register, or a create carrying a stable Idempotency-Key the server dedupes.
	// A keyless create must NOT set it (a reset may have applied → double-provision).
	RetryOnTransient bool
}

// Response is a 2xx result; Headers exposes ETag/X-Request-Id to callers.
type Response struct {
	Status  int
	Body    []byte
	Headers http.Header
}

// DoRaw executes method+path and returns the *Response for ANY HTTP status (including
// non-2xx). Only a transport fault yields an error (*TimeoutError / *ConnectionError; a
// context cancel propagates raw). Callers that want the RFC-9457 non-2xx → *problem.APIError
// mapping use Do; callers with a different error contract (the control plane's {code,message})
// inspect the raw Response themselves.
func (c *Client) DoRaw(ctx context.Context, method, path string, r Request) (*Response, error) {
	attempts := 1
	if c.maxAttempts > 1 && (idempotentMethod(method) || r.RetryOnTransient) {
		attempts = c.maxAttempts
	}
	for attempt := 1; ; attempt++ {
		resp, err := c.doOnce(ctx, method, path, r)
		if err == nil || !isTransientFault(err) || attempt >= attempts {
			return resp, err
		}
		// Transient transport fault on a retry-eligible request: back off, then retry.
		// A ctx cancellation during the wait ends it, returning the transient fault.
		select {
		case <-time.After(c.backoff(attempt)):
		case <-ctx.Done():
			return nil, err
		}
	}
}

// doOnce is a single send: build, execute, read the full body, normalize a fault.
func (c *Client) doOnce(ctx context.Context, method, path string, r Request) (*Response, error) {
	req, err := c.newRequest(ctx, method, path, r)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, normalizeFault(method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, normalizeFault(method, path, err)
	}
	return &Response{Status: resp.StatusCode, Body: b, Headers: resp.Header}, nil
}

// idempotentMethod reports whether a method is safe to replay unconditionally.
func idempotentMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodDelete, http.MethodHead:
		return true
	default:
		return false
	}
}

// isTransientFault reports whether err is a normalized transient transport fault
// (connection reset/dial/EOF or a net timeout) — the only class worth retrying.
func isTransientFault(err error) bool {
	var ce *ConnectionError
	var te *TimeoutError
	return errors.As(err, &ce) || errors.As(err, &te)
}

// newRequest builds the *http.Request with the standard headers (Accept, X-Pine-Auth,
// Content-Type, extras). Shared by DoRaw and Stream.
func (c *Client) newRequest(ctx context.Context, method, path string, r Request) (*http.Request, error) {
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, fmt.Errorf("transport: build request: %w", err)
	}
	accept := r.Accept
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)
	if r.Token != "" {
		req.Header.Set("X-Pine-Auth", r.Token)
	}
	if r.ContentType != "" {
		req.Header.Set("Content-Type", r.ContentType)
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// StreamResponse is a long-lived response whose Body is the live stream (the caller MUST
// Close it). Used for SSE — DoRaw would buffer the whole body.
type StreamResponse struct {
	Status  int
	Body    io.ReadCloser
	Headers http.Header
}

// Stream opens method+path on the no-total-timeout streaming client and returns the live
// response. Only a transport fault errors here; the caller inspects Status and reads Body
// (then Closes it). Cancel the stream by cancelling ctx.
func (c *Client) Stream(ctx context.Context, method, path string, r Request) (*StreamResponse, error) {
	req, err := c.newRequest(ctx, method, path, r)
	if err != nil {
		return nil, err
	}
	resp, err := c.streamHC.Do(req)
	if err != nil {
		return nil, normalizeFault(method, path, err)
	}
	return &StreamResponse{Status: resp.StatusCode, Body: resp.Body, Headers: resp.Header}, nil
}

// Do executes method+path. On 2xx returns *Response. On non-2xx returns a typed
// *problem.APIError (408/504 → *TimeoutError). On a transport fault returns
// *TimeoutError / *ConnectionError (a context cancel propagates raw).
func (c *Client) Do(ctx context.Context, method, path string, r Request) (*Response, error) {
	resp, err := c.DoRaw(ctx, method, path, r)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.Status >= 200 && resp.Status < 300:
		return resp, nil
	case resp.Status == http.StatusRequestTimeout, resp.Status == http.StatusGatewayTimeout:
		return nil, &TimeoutError{Op: method + " " + path, Msg: fmt.Sprintf("status %d", resp.Status)}
	default:
		return nil, problem.Parse(resp.Status, resp.Body, resp.Headers.Get("X-Request-Id"))
	}
}

// normalizeFault maps a Go transport error to a typed SDK error. A caller-initiated
// cancel propagates unchanged (it's not a transport fault).
func normalizeFault(method, path string, err error) error {
	op := method + " " + path
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &TimeoutError{Op: op, Msg: err.Error()}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &TimeoutError{Op: op, Msg: err.Error()}
	}
	return &ConnectionError{Op: op, Msg: err.Error()}
}
