package tokens

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/base/wait"
)

// Path is the portal mint endpoint. The pk_ is presented as Authorization: Bearer; the
// portal returns a short-TTL EdDSA project JWS the control plane accepts.
const Path = "/v1/control-token"

const (
	defaultSkew        = 60 * time.Second // refresh this long before exp (mirrors the portal's back-dated iat/nbf)
	defaultMaxAttempts = 4
	maxBackoff         = 10 * time.Second
)

// ControlTokenSource mints and caches a project JWS from a pk_ client key. It is
// goroutine-safe with single-flight refresh: concurrent callers serialize on the mint and
// reuse its result. 429/5xx are retried with bounded jittered backoff; 401/403 are
// terminal. The pk_ and the minted JWS never appear in the String() form.
type ControlTokenSource struct {
	client *transport.Client
	apiKey string // pk_

	clock       func() time.Time
	sleeper     func(context.Context, time.Duration) error
	rng         func() float64 // jitter in [0,1); injectable for deterministic tests
	skew        time.Duration
	maxAttempts int

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// Option configures a ControlTokenSource.
type Option func(*ControlTokenSource)

// WithClock injects the clock (default time.Now) — for freshness tests.
func WithClock(f func() time.Time) Option { return func(s *ControlTokenSource) { s.clock = f } }

// WithSleeper injects the backoff sleeper (default wait.Sleep) — tests capture sleeps. It
// returns ctx.Err() on a cancelled context, which aborts the mint loop.
func WithSleeper(f func(context.Context, time.Duration) error) Option {
	return func(s *ControlTokenSource) { s.sleeper = f }
}

// WithJitter injects the jitter source (default rand.Float64) — tests pin it to 0.
func WithJitter(f func() float64) Option { return func(s *ControlTokenSource) { s.rng = f } }

// WithSkew sets how long before expiry the token is considered stale (default 60s).
func WithSkew(d time.Duration) Option { return func(s *ControlTokenSource) { s.skew = d } }

// WithMaxAttempts sets the mint retry budget for 429/5xx (default 4, min 1).
func WithMaxAttempts(n int) Option {
	return func(s *ControlTokenSource) {
		if n < 1 {
			n = 1
		}
		s.maxAttempts = n
	}
}

// NewControlTokenSource builds a source posting to client (pointed at the portal/control
// host). apiKey must be a non-empty pk_ client key.
func NewControlTokenSource(client *transport.Client, apiKey string, opts ...Option) (*ControlTokenSource, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("pinesandbox: api_key required (a pk_ client key)")
	}
	if client == nil {
		return nil, fmt.Errorf("pinesandbox: control-token transport client required")
	}
	s := &ControlTokenSource{
		client:      client,
		apiKey:      apiKey,
		clock:       time.Now,
		sleeper:     wait.Sleep,
		rng:         rand.Float64,
		skew:        defaultSkew,
		maxAttempts: defaultMaxAttempts,
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Token returns the current project JWS, minting/refreshing as needed. forceRefresh
// bypasses the cache (used after a control-plane 401). Single-flight: the mutex is held
// across the freshness check AND the mint, so concurrent callers serialize — the first
// mints, the rest re-check under the lock and observe the fresh token without re-minting.
func (s *ControlTokenSource) Token(ctx context.Context, forceRefresh bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !forceRefresh && s.token != "" && s.fresh(s.clock()) {
		return s.token, nil
	}
	return s.mint(ctx)
}

func (s *ControlTokenSource) fresh(now time.Time) bool {
	return !s.expiresAt.IsZero() && now.Before(s.expiresAt.Add(-s.skew))
}

// mint runs the bounded retry loop. Caller holds s.mu.
func (s *ControlTokenSource) mint(ctx context.Context) (string, error) {
	for i := 0; i < s.maxAttempts; i++ {
		last := i+1 >= s.maxAttempts
		resp, err := s.postMint(ctx)
		if err == nil {
			return s.applySuccess(resp)
		}

		var ae *problem.APIError
		if !errors.As(err, &ae) {
			return "", err // transport fault (timeout/connection) — terminal, already typed
		}
		switch {
		case ae.Status == 401:
			return "", &InvalidClientKey{tokenBase{"unknown or revoked project client key", 401, ae.RequestID, ae}}
		case ae.Status == 403:
			return "", &InsufficientScope{tokenBase{"this key may not mint control-plane tokens (needs pk_session/pk_admin)", 403, ae.RequestID, ae}}
		case ae.Status == 429:
			if last {
				return "", &RateLimited{tokenBase: tokenBase{"portal rate-limited the control-token mint", 429, ae.RequestID, ae}}
			}
			if serr := s.sleeper(ctx, s.backoff(i+1)); serr != nil {
				return "", serr
			}
		case ae.Status >= 500 && ae.Status <= 599:
			if last {
				return "", &ControlTokenError{tokenBase{"portal error minting control token", ae.Status, ae.RequestID, ae}}
			}
			if serr := s.sleeper(ctx, s.backoff(i+1)); serr != nil {
				return "", serr
			}
		default:
			return "", &ControlTokenError{tokenBase{"unexpected portal response minting control token", ae.Status, ae.RequestID, ae}}
		}
	}
	// Unreachable: the loop returns on the last attempt for every retryable branch.
	return "", &ControlTokenError{tokenBase{Msg: "control-token mint exhausted retries"}}
}

func (s *ControlTokenSource) postMint(ctx context.Context) (*transport.Response, error) {
	return s.client.Do(ctx, "POST", Path, transport.Request{
		Accept:  "application/json",
		Headers: map[string]string{"Authorization": "Bearer " + s.apiKey},
	})
}

// mintResponse is the portal's success body. expires_in (relative seconds) is preferred;
// expires_at (RFC 3339) is the fallback. A response with neither is unusable.
type mintResponse struct {
	Token     string  `json:"token"`
	ExpiresIn *int    `json:"expires_in"`
	ExpiresAt *string `json:"expires_at"`
}

func (s *ControlTokenSource) applySuccess(resp *transport.Response) (string, error) {
	var body mintResponse
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return "", &ControlTokenError{tokenBase{Msg: "mint response was not valid JSON", Status: 200, Cause: err}}
	}
	if body.Token == "" {
		return "", &ControlTokenError{tokenBase{Msg: "mint response missing token", Status: 200}}
	}
	exp, err := s.computeExpiry(body)
	if err != nil {
		return "", err
	}
	s.token = body.Token
	s.expiresAt = exp
	return s.token, nil
}

func (s *ControlTokenSource) computeExpiry(body mintResponse) (time.Time, error) {
	switch {
	case body.ExpiresIn != nil:
		return s.clock().Add(time.Duration(*body.ExpiresIn) * time.Second), nil
	case body.ExpiresAt != nil:
		t, err := time.Parse(time.RFC3339, *body.ExpiresAt)
		if err != nil {
			return time.Time{}, &ControlTokenError{tokenBase{Msg: "mint response has an unparseable expires_at", Status: 200, Cause: err}}
		}
		return t, nil
	default:
		return time.Time{}, &ControlTokenError{tokenBase{Msg: "mint response missing expiry (expires_in / expires_at)", Status: 200}}
	}
}

// backoff is bounded jittered exponential: 0.2·2^(attempt-1) seconds + up to 100ms jitter,
// capped at maxBackoff. (Retry-After, capped at the same ceiling, is not separately honored.)
func (s *ControlTokenSource) backoff(attempt int) time.Duration {
	base := 0.2 * math.Pow(2, float64(attempt-1))
	d := time.Duration((base + s.rng()*0.1) * float64(time.Second))
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// String is redacted: it never reveals the pk_ or the cached JWS.
func (s *ControlTokenSource) String() string {
	s.mu.Lock()
	cached := s.token != ""
	s.mu.Unlock()
	return fmt.Sprintf("ControlTokenSource{cached=%t, pk_+token redacted}", cached)
}
