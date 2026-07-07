package tokens

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.pinesandbox.io/computer/internal/base/transport"
)

func unsignedJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "."
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestSource wires a ControlTokenSource at an httptest server. Defaults: zero jitter +
// no-op sleeper (deterministic, instant retries). Tests override via opts.
func newTestSource(t *testing.T, handler http.HandlerFunc, opts ...Option) (*ControlTokenSource, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := transport.New("http", strings.TrimPrefix(srv.URL, "http://"))
	base := []Option{WithJitter(func() float64 { return 0 }), WithSleeper(func(context.Context, time.Duration) error { return nil })}
	s, err := NewControlTokenSource(client, "pk_test123", append(base, opts...)...)
	if err != nil {
		t.Fatalf("NewControlTokenSource: %v", err)
	}
	return s, srv
}

func TestNew_RejectsEmptyKey(t *testing.T) {
	if _, err := NewControlTokenSource(transport.New("http", "x"), ""); err == nil {
		t.Fatal("expected error for empty api_key")
	}
	if _, err := NewControlTokenSource(nil, "pk_x"); err == nil {
		t.Fatal("expected error for nil client")
	}
}

func TestToken_MintSuccess_ExpiresIn(t *testing.T) {
	var hits int32
	var gotAuth string
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost || r.URL.Path != Path {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, Path)
		}
		fmt.Fprint(w, `{"token":"jws-abc","expires_in":3600}`)
	})

	tok, err := s.Token(context.Background(), false)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "jws-abc" {
		t.Errorf("token = %q, want jws-abc", tok)
	}
	if gotAuth != "Bearer pk_test123" {
		t.Errorf("Authorization = %q, want Bearer pk_test123", gotAuth)
	}

	// A second call is served from cache — no new mint.
	if _, err := s.Token(context.Background(), false); err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hit %d times, want 1 (second call should be cached)", got)
	}
}

func TestToken_MintSuccess_ExpiresAt(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"jws-at","expires_at":"2026-01-01T01:00:00Z"}`)
	}, WithClock(clk.now))

	tok, err := s.Token(context.Background(), false)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "jws-at" {
		t.Errorf("token = %q, want jws-at", tok)
	}
}

func TestToken_MintSuccess_UsesJWTExpWithoutResponseExpiry(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tok := unsignedJWT(t, map[string]any{"exp": clk.now().Add(time.Hour).Unix()})
	var hits int32
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprintf(w, `{"token":%q}`, tok)
	}, WithClock(clk.now), WithSkew(60*time.Second))

	if got, err := s.Token(context.Background(), false); err != nil || got != tok {
		t.Fatalf("Token = %q, %v; want minted JWT", got, err)
	}
	if _, err := s.Token(context.Background(), false); err != nil {
		t.Fatalf("cached Token: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hit %d times, want 1 (JWT exp should make cache fresh)", got)
	}
}

// The token's own exp is authoritative for cache lifetime — a longer response expires_in
// must NOT keep an already-expiring JWS cached past its real exp.
func TestToken_JWTExpPreferredOverResponseExpiry(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tok := unsignedJWT(t, map[string]any{"exp": clk.now().Add(2 * time.Minute).Unix()})
	var hits int32
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprintf(w, `{"token":%q,"expires_in":3600}`, tok) // response claims 1h; token exp says 2m
	}, WithClock(clk.now), WithSkew(60*time.Second))

	if _, err := s.Token(context.Background(), false); err != nil {
		t.Fatalf("mint: %v", err)
	}
	// Inside the token's real exp (minus skew) → still cached, no re-mint.
	clk.advance(30 * time.Second)
	if _, err := s.Token(context.Background(), false); err != nil {
		t.Fatalf("cached Token: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hit %d times, want 1 (still fresh within the token exp)", got)
	}
	// Past the token exp minus skew (2m − 60s = 90s) → re-mint, despite expires_in=3600.
	clk.advance(90 * time.Second)
	if _, err := s.Token(context.Background(), false); err != nil {
		t.Fatalf("re-mint: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hit %d times, want 2 (token exp, not expires_in, drives re-mint)", got)
	}
}

func TestToken_ForceRefresh_Rebypasses(t *testing.T) {
	var hits int32
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		fmt.Fprintf(w, `{"token":"jws-%d","expires_in":3600}`, n)
	})

	first, _ := s.Token(context.Background(), false)
	second, err := s.Token(context.Background(), true) // force
	if err != nil {
		t.Fatalf("force Token: %v", err)
	}
	if first == second {
		t.Errorf("force-refresh returned the cached token %q", first)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hit %d times, want 2 (force re-mints)", got)
	}
}

func TestToken_RefreshesWhenStale(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	var hits int32
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		fmt.Fprintf(w, `{"token":"jws-%d","expires_in":3600}`, n) // 1h TTL
	}, WithClock(clk.now), WithSkew(60*time.Second))

	if _, err := s.Token(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	// Within the skew window of expiry → stale → re-mint.
	clk.advance(3600*time.Second - 30*time.Second)
	if _, err := s.Token(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hit %d times, want 2 (stale → re-mint)", got)
	}
}

func TestToken_401_InvalidClientKey(t *testing.T) {
	var hits int32
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(401)
		fmt.Fprint(w, `{"detail":"nope"}`)
	})
	_, err := s.Token(context.Background(), false)
	var e *InvalidClientKey
	if !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *InvalidClientKey", err, err)
	}
	if e.Status != 401 {
		t.Errorf("Status = %d, want 401", e.Status)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("401 was retried (%d hits) — should be terminal", got)
	}
}

// TestToken_ErrorCarriesResourceContext: the top-level token error string keeps the resource
// context (host + op + request id) from the wrapped *problem.APIError, so a generic handler
// that logs only err.Error() still sees WHICH portal call failed.
func TestToken_ErrorCarriesResourceContext(t *testing.T) {
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-tok")
		w.WriteHeader(401)
		fmt.Fprint(w, `{"detail":"nope"}`)
	})
	_, err := s.Token(context.Background(), false)
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	for _, want := range []string{"host=", "op=POST /v1/control-token", "request_id=req-tok"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestToken_403_InsufficientScope(t *testing.T) {
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	})
	var e *InsufficientScope
	if _, err := s.Token(context.Background(), false); !errors.As(err, &e) {
		t.Fatalf("err = %v, want *InsufficientScope", err)
	}
}

func TestToken_429_RetriesThenSucceeds(t *testing.T) {
	var hits int32
	var sleeps int32
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, `{"token":"jws-ok","expires_in":3600}`)
	}, WithSleeper(func(context.Context, time.Duration) error { atomic.AddInt32(&sleeps, 1); return nil }))

	tok, err := s.Token(context.Background(), false)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "jws-ok" {
		t.Errorf("token = %q, want jws-ok", tok)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("hits = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&sleeps); got != 1 {
		t.Errorf("sleeps = %d, want 1", got)
	}
}

// TestToken_ContextCancelledDuringBackoff: a cancelled context aborts the mint retry loop
// instead of sleeping out the backoff (the ctx-aware sleeper fix).
func TestToken_ContextCancelledDuringBackoff(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(429) // readiness/retryable forever
	}))
	t.Cleanup(srv.Close)
	client := transport.New("http", strings.TrimPrefix(srv.URL, "http://"))
	ctx, cancel := context.WithCancel(context.Background())
	s, err := NewControlTokenSource(client, "pk_x",
		WithJitter(func() float64 { return 0 }),
		WithSleeper(func(c context.Context, _ time.Duration) error { cancel(); return c.Err() }))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Token(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := atomic.LoadInt32(&hits); got > 2 {
		t.Errorf("mint retried %d times after cancel — should stop promptly", got)
	}
}

func TestToken_429_ExhaustsToRateLimited(t *testing.T) {
	var hits int32
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(429)
	}, WithMaxAttempts(2))

	_, err := s.Token(context.Background(), false)
	var e *RateLimited
	if !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *RateLimited", err, err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("hits = %d, want 2 (maxAttempts)", got)
	}
}

func TestToken_5xx_RetriesThenExhausts(t *testing.T) {
	var hits int32
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(503)
	}, WithMaxAttempts(3))

	_, err := s.Token(context.Background(), false)
	var e *ControlTokenError
	if !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *ControlTokenError", err, err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("hits = %d, want 3", got)
	}
}

func TestToken_MalformedSuccess(t *testing.T) {
	cases := map[string]string{
		"missing token":  `{"expires_in":3600}`,
		"missing expiry": `{"token":"jws-x"}`,
		"not json":       `<<<`,
		"bad expires_at": `{"token":"jws-x","expires_at":"not-a-date"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			})
			_, err := s.Token(context.Background(), false)
			var e *ControlTokenError
			if !errors.As(err, &e) {
				t.Fatalf("err = %T (%v), want *ControlTokenError", err, err)
			}
			// Every malformed-200 must be self-describing (R3-1): the mint op
			// is known even when the response gave us nothing to work with.
			if msg := err.Error(); !strings.Contains(msg, "op=POST /v1/control-token") || !strings.Contains(msg, "host=") {
				t.Fatalf("malformed-200 error lacks resource context: %q", msg)
			}
		})
	}
}

// TestToken_SingleFlight asserts that many concurrent callers trigger exactly ONE mint —
// the rest observe the freshly-cached token under the lock. Run under -race.
func TestToken_SingleFlight(t *testing.T) {
	var hits int32
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(10 * time.Millisecond) // widen the race window
		fmt.Fprint(w, `{"token":"jws-shared","expires_in":3600}`)
	})

	const n = 25
	var wg sync.WaitGroup
	toks := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			toks[i], errs[i] = s.Token(context.Background(), false)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if toks[i] != "jws-shared" {
			t.Errorf("goroutine %d token = %q, want jws-shared", i, toks[i])
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hit %d times under concurrency, want exactly 1 (single-flight)", got)
	}
}

// TestString_Redacts ensures neither the pk_ nor the cached JWS leaks via String().
func TestString_Redacts(t *testing.T) {
	s, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"super-secret-jws","expires_in":3600}`)
	})
	if _, err := s.Token(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	got := s.String()
	if strings.Contains(got, "pk_test123") || strings.Contains(got, "super-secret-jws") {
		t.Errorf("String() leaked a secret: %q", got)
	}
	if !strings.Contains(got, "cached=true") {
		t.Errorf("String() = %q, want it to report cached=true", got)
	}
}
