package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/spec"
	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/bind"
)

// Client is the coordinator data-plane client. Construct it with NewClient.
type Client struct {
	raw  *transport.Client
	spec spec.Negotiator
	// Typed-event-iterator reconnect knobs (see stream.go). Defaults set in NewClient;
	// tests in this package override them so a reconnect doesn't actually sleep.
	streamBackoff func(attempt int) time.Duration
	streamBudget  int
}

// NewClient builds a coordinator client over raw (pointed at the Computer's data host),
// negotiating the given spec-version major.
func NewClient(raw *transport.Client, specMajor int) *Client {
	return &Client{
		raw: raw,
		spec: spec.Negotiator{
			RequestHeader:  "Computer-Spec-Version",
			ResponseHeader: "X-Computer-Spec-Version",
			SupportedMajor: specMajor,
		},
		streamBackoff: defaultStreamBackoff,
		streamBudget:  defaultReconnectBudget,
	}
}

// Host returns the data host this coordinator client targets (the public gateway host in
// production), scheme stripped.
func (c *Client) Host() string { return c.raw.Host() }

// BaseURL returns the data host origin (scheme://host) — the full URI the browser-safe
// DelegatedConnection carries as computer_host (computer-api.yaml).
func (c *Client) BaseURL() string { return c.raw.BaseURL() }

// ---- bind (public, token-less) ----

// BindPubkey fetches the coordinator's ephemeral HPKE public key + pod identity.
func (c *Client) BindPubkey(ctx context.Context) (*BindPubkey, error) {
	resp, err := c.do(ctx, "GET", "/v1/coord/bind-pubkey", "", nil)
	if err != nil {
		return nil, err
	}
	return parseBindPubkey(resp.Body)
}

// BindExtras carry the required v3 key assertion and the optional sealed
// restore secrets used only on a two-round restore commit.
type BindExtras struct {
	KeyAssertion   string
	RestoreSecrets string
}

// Bind submits the sealed bind envelope (ciphertext = enc||aead_ct, base64url by the
// caller) echoing the pod identity, and returns the pod's ct_ + epoch — or a
// RestoreChallenge when the Computer's state is asymmetric and the caller must unwrap
// the per-component secrets first.
func (c *Client) Bind(ctx context.Context, bindToken, podUID, coordBootID, ciphertext string, extras BindExtras) (*BindResult, error) {
	fields := map[string]string{
		"pod_uid":       podUID,
		"coord_boot_id": coordBootID,
		"ciphertext":    ciphertext,
		"bind_token":    bindToken,
	}
	if extras.KeyAssertion != "" {
		fields["key_assertion"] = extras.KeyAssertion
	}
	if extras.RestoreSecrets != "" {
		fields["restore_secrets"] = extras.RestoreSecrets
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal bind body: %w", err)
	}
	resp, err := c.do(ctx, "POST", "/v1/coord/bind", "", body)
	if err != nil {
		return nil, err
	}
	return parseBindResult(resp.Body)
}

// ---- session lifecycle ----

// CreateSessionOptions are the optional knobs for CreateSession.
type CreateSessionOptions struct {
	Name    string
	Label   string
	Browser bool
	Blind   bool
}

type createSessionBody struct {
	Name    string `json:"name,omitempty"`
	Browser bool   `json:"browser"`
	Label   string `json:"label,omitempty"`
	Blind   bool   `json:"blind,omitempty"`
}

// CreateSession opens a session (POST sessions, no v1 prefix). An empty Name lets the
// coordinator mint a friendly one. token is typically the ct_.
func (c *Client) CreateSession(ctx context.Context, token string, opts CreateSessionOptions) (*Session, error) {
	body, err := json.Marshal(createSessionBody{Name: opts.Name, Browser: opts.Browser, Label: opts.Label, Blind: opts.Blind})
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal create-session body: %w", err)
	}
	resp, err := c.do(ctx, "POST", "/sessions", token, body)
	if err != nil {
		return nil, err
	}
	return c.sessionFromEnvelope(resp.Body)
}

// GetSession fetches a session by name.
func (c *Client) GetSession(ctx context.Context, token, name string) (*Session, error) {
	resp, err := c.do(ctx, "GET", "/sessions/"+url.PathEscape(name), token, nil)
	if err != nil {
		return nil, err
	}
	return c.sessionFromEnvelope(resp.Body)
}

// ListSessions returns all sessions (body.sessions).
func (c *Client) ListSessions(ctx context.Context, token string) ([]*Session, error) {
	resp, err := c.do(ctx, "GET", "/sessions", token, nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Sessions []*sessionWire `json:"sessions"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable sessions list: %w", err)
	}
	out := make([]*Session, 0, len(env.Sessions))
	for _, w := range env.Sessions {
		out = append(out, w.toSession())
	}
	return out, nil
}

// DestroySession deletes a session. clean:true also GCs the on-disk run dir (default
// leaves it so a same-name re-create re-attaches to prior workdir contents).
func (c *Client) DestroySession(ctx context.Context, token, name string, clean bool) error {
	path := "/sessions/" + url.PathEscape(name)
	if clean {
		path += "?clean=true"
	}
	_, err := c.do(ctx, "DELETE", path, token, nil)
	return err
}

// RecreateTerminal rebuilds the bash terminal after an execd restart left it lost (coord
// returns 409 terminal_lost on /exec until this runs). The browser plane is preserved.
func (c *Client) RecreateTerminal(ctx context.Context, token, name string) error {
	_, err := c.do(ctx, "POST", "/sessions/"+url.PathEscape(name)+"/terminal/recreate", token, emptyJSON)
	return err
}

// Focus raises the session's window.
func (c *Client) Focus(ctx context.Context, token, name string) error {
	_, err := c.do(ctx, "POST", "/sessions/"+url.PathEscape(name)+"/focus", token, emptyJSON)
	return err
}

// Epoch returns the session's current epoch document (raw — the caller interprets it).
func (c *Client) Epoch(ctx context.Context, token, name string) (json.RawMessage, error) {
	resp, err := c.do(ctx, "GET", "/sessions/"+url.PathEscape(name)+"/epoch", token, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(resp.Body), nil
}

// ---- plumbing ----

var emptyJSON = []byte("{}")

func (c *Client) sessionFromEnvelope(body []byte) (*Session, error) {
	var env struct {
		Session *sessionWire `json:"session"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable session: %w", err)
	}
	if env.Session == nil {
		return nil, fmt.Errorf("pinesandbox: response missing session")
	}
	return env.Session.toSession(), nil
}

// coordReq is one coordinator request. accept defaults to application/json; headers carries
// caller extras (If-Match, Idempotency-Key) merged over the spec-version header by send.
type coordReq struct {
	method, path, token string
	body                []byte
	contentType, accept string
	headers             map[string]string
}

// do is the JSON common case: a request whose body (if any) is JSON, no extra headers.
func (c *Client) do(ctx context.Context, method, path, token string, body []byte) (*transport.Response, error) {
	r := coordReq{method: method, path: path, token: token, body: body}
	if body != nil {
		r.contentType = "application/json"
	}
	return c.send(ctx, r)
}

// send is the single coordinator request-and-validate path: X-Pine-Auth bearer (omitted when
// token==""), the spec-version header (+ caller extras), a caller-chosen Content-Type/Accept,
// and — on every response — the spec-version Check + the non-2xx → typed-error mapping.
func (c *Client) send(ctx context.Context, r coordReq) (*transport.Response, error) {
	accept := r.accept
	if accept == "" {
		accept = "application/json"
	}
	resp, err := c.raw.DoRaw(ctx, r.method, r.path, transport.Request{
		Token:       r.token,
		Accept:      accept,
		ContentType: r.contentType,
		Body:        r.body,
		Headers:     c.baseHeaders(r.headers),
	})
	if err != nil {
		return nil, err
	}
	if err := c.spec.Check(resp.Headers.Get(c.spec.ResponseHeader)); err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, c.respError(resp.Status, resp.Body, resp.Headers.Get("X-Request-Id"), r.token, transport.Operation(r.method, r.path))
	}
	return resp, nil
}

// baseHeaders returns the spec-version request header (the one header every coordinator
// request carries), merged with any caller extras. The single place the header name is set.
func (c *Client) baseHeaders(extra map[string]string) map[string]string {
	h := map[string]string{c.spec.RequestHeader: c.spec.RequestValue()}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// streamErrorBodyCap bounds how much of a non-2xx streaming response (a problem+json
// body, not a stream) we read before surfacing the typed error.
const streamErrorBodyCap = 64 << 10

// openStream opens a streaming request with the caller's Accept, validates the
// spec-version, and maps a non-2xx (a problem+json body, not a stream) to a typed
// error — the single open path for the SSE feeds and binary streams (artifact reads).
// On success the caller owns the frame/byte loop and MUST Close the returned Body;
// on every error path openStream closes it.
//
// retryTransient opts the OPEN into the transport's transient-retry budget (parity
// with the buffered reads, which get it via DoRaw). The SSE feeds pass false: their
// iterators own a cursor-resumed reconnect budget, and retrying under them would
// compound the two.
func (c *Client) openStream(ctx context.Context, method, path, token, accept string, retryTransient bool, body []byte, extra map[string]string) (*transport.StreamResponse, error) {
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}
	open := c.raw.Stream
	if retryTransient {
		open = c.raw.StreamWithRetry
	}
	sr, err := open(ctx, method, path, transport.Request{
		Token:       token,
		Accept:      accept,
		ContentType: contentType,
		Body:        body,
		Headers:     c.baseHeaders(extra),
	})
	if err != nil {
		return nil, err
	}
	if err := c.spec.Check(sr.Headers.Get(c.spec.ResponseHeader)); err != nil {
		sr.Body.Close()
		return nil, err
	}
	if sr.Status < 200 || sr.Status >= 300 {
		b, _ := io.ReadAll(io.LimitReader(sr.Body, streamErrorBodyCap))
		sr.Body.Close()
		return nil, c.respError(sr.Status, b, sr.Headers.Get("X-Request-Id"), token, transport.Operation(method, path))
	}
	return sr, nil
}

// openSSE is openStream for the text/event-stream feeds (agent / author / control
// events and exec). Single-shot open — the feed iterators own the reconnect budget.
func (c *Client) openSSE(ctx context.Context, method, path, token string, body []byte, extra map[string]string) (*transport.StreamResponse, error) {
	return c.openStream(ctx, method, path, token, "text/event-stream", false, body, extra)
}

// respError maps a non-2xx coordinator response to a typed error: a 401 on a token'd call is
// a *bind.TokenRejectedError — a REPORT that the coordinator did not recognize the bound
// ct_/ps_, not an instruction to re-attach. On a live current sandbox that is
// binding_auth_lost; only confirmed sandbox-gone evidence justifies an attach, and the SDK
// never implicitly rebinds (a fresh pod invalidates every ps_ and fences the old pod). The
// underlying *problem.APIError is wrapped, so errors.As still reaches it. Everything else is
// the RFC-9457 *problem.APIError. Token-less routes (bind/health/metrics) never hit the 401
// branch. op is "<METHOD> <path>"; it + the data host stamp WHICH operation on WHICH Computer
// failed (the primary spine) onto the typed error, so a generic handler is self-describing.
func (c *Client) respError(status int, body []byte, requestID, token, op string) error {
	ae := problem.Parse(status, body, requestID)
	ae.Host = c.Host()
	ae.Op = op
	if status == 401 && token != "" {
		return bind.NewTokenRejectedError(401, "the bound token was rejected by the coordinator (binding_auth_lost on a live sandbox; re-attach ONLY on confirmed sandbox-gone evidence)", ae)
	}
	return ae
}
