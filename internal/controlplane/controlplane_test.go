package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.pinesandbox.io/computer/internal/base/spec"
	"go.pinesandbox.io/computer/internal/base/transport"
)

// fakeSource returns `initial` on a normal call and `refreshed` on a forced refresh,
// recording the force flag of each call.
type fakeSource struct {
	mu         sync.Mutex
	initial    string
	refreshed  string
	forceCalls []bool
}

func (f *fakeSource) Token(_ context.Context, force bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forceCalls = append(f.forceCalls, force)
	if force {
		return f.refreshed, nil
	}
	return f.initial, nil
}

func (f *fakeSource) calls() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.forceCalls...)
}

func newTestClient(t *testing.T, handler http.HandlerFunc, src TokenSource) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	raw := transport.New("http", strings.TrimPrefix(srv.URL, "http://"))
	return NewClient(raw, src, 1)
}

const sandboxJSON = `{"id":"pine-abc","image":{"uri":"reg/img:1"},"status":{"state":"Running"},` +
	`"metadata":{"k":"v"},"entrypoint":["/bin/sh"],"expiresAt":"2026-01-01T01:00:00Z","createdAt":"2026-01-01T00:00:00Z"}`

func TestWaitForChangePreservesConditionalResponse(t *testing.T) {
	for _, status := range []int{200, 304, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			previous := &SandboxInfo{Status: "pausing", ETag: `"uid/1"`}
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") != previous.ETag || r.Header.Get("Prefer") != "" {
					t.Errorf("missing conditional wait headers: %v", r.Header)
				}
				w.Header().Set("X-Computer-Spec-Version", "1")
				w.Header().Set("ETag", `"uid/2"`)
				w.WriteHeader(status)
				if status == 200 {
					_, _ = io.WriteString(w, sandboxJSON)
				}
			}, &fakeSource{initial: "test"})
			info, err := c.WaitForChange(context.Background(), "pine-abc", previous)
			switch status {
			case 200:
				if err != nil || info.Status != "running" || info.ETag != `"uid/2"` {
					t.Fatalf("info=%+v err=%v", info, err)
				}
			case 304:
				if err != nil || info != previous {
					t.Fatalf("304 lost previous observation: %v", err)
				}
			case 404:
				var missing *NotFoundError
				if !errors.As(err, &missing) {
					t.Fatalf("missing runtime: %v", err)
				}
			}
		})
	}
}

func TestCreate_SuccessAndHeaders(t *testing.T) {
	var gotAuth, gotSpec, gotCT string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotSpec = r.Header.Get("Computer-Spec-Version")
		gotCT = r.Header.Get("Content-Type")
		if r.Method != "POST" || r.URL.Path != "/computer-sandboxes" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(202)
		fmt.Fprint(w, sandboxJSON)
	}, &fakeSource{initial: "jws1"})

	info, err := c.CreateComputer(context.Background(), map[string]any{"timeout": 3600})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.ID != "pine-abc" || info.Status != "running" || info.Image != "reg/img:1" {
		t.Errorf("info = %+v", info)
	}
	if gotAuth != "Bearer jws1" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotSpec != "1" {
		t.Errorf("Computer-Spec-Version = %q, want 1", gotSpec)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
}

func TestCreateComputer_UsesPolicyOwnedRoute(t *testing.T) {
	var body string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/computer-sandboxes" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(202)
		fmt.Fprint(w, sandboxJSON)
	}, &fakeSource{initial: "jws1"})

	_, err := c.CreateComputer(context.Background(), map[string]any{
		"timeout":  300,
		"metadata": map[string]string{"owner": "integrator"},
	})
	if err != nil {
		t.Fatalf("CreateComputer: %v", err)
	}
	if strings.Contains(body, "image") || strings.Contains(body, "poolRef") || strings.Contains(body, "entrypoint") {
		t.Fatalf("runtime selection leaked into Computer create body: %s", body)
	}
}

func TestGet_ParsesSandboxInfo(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/sandboxes/pine-abc" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, sandboxJSON)
	}, &fakeSource{initial: "jws1"})

	info, err := c.Get(context.Background(), "pine-abc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.Metadata["k"] != "v" || len(info.Entrypoint) != 1 {
		t.Errorf("info = %+v", info)
	}
	if info.ExpiresAt == nil || info.CreatedAt == nil {
		t.Errorf("timestamps not parsed: %+v", info)
	}
}

func TestDestroy_Idempotent(t *testing.T) {
	for _, status := range []int{204, 404, 202} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "DELETE" {
				t.Errorf("method = %s", r.Method)
			}
			w.WriteHeader(status)
		}, &fakeSource{initial: "jws1"})
		if err := c.Destroy(context.Background(), "x"); err != nil {
			t.Errorf("Destroy got error for status %d: %v", status, err)
		}
	}
}

func TestPauseResume_PreservesConflict(t *testing.T) {
	for _, verb := range []string{"pause", "resume"} {
		for _, status := range []int{200, 202, 204, 409, 501} {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/sandboxes/x/"+verb {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(status)
			}, &fakeSource{initial: "jws1"})
			request := c.Pause
			if verb == "resume" {
				request = c.Resume
			}
			err := request(context.Background(), "x")
			var conflict *ConflictError
			if status == 409 && !errors.As(err, &conflict) {
				t.Fatalf("%s: expected typed conflict, got %v", verb, err)
			}
			if (status < 400) != (err == nil) {
				t.Fatalf("%s(%d): %v", verb, status, err)
			}
		}
	}
}

func TestDo_401RefreshesAndRetriesOnce(t *testing.T) {
	src := &fakeSource{initial: "stale", refreshed: "fresh"}
	var n int
	var mu sync.Mutex
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		switch r.Header.Get("Authorization") {
		case "Bearer stale":
			w.WriteHeader(401)
		case "Bearer fresh":
			fmt.Fprint(w, sandboxJSON)
		default:
			t.Errorf("unexpected auth %q", r.Header.Get("Authorization"))
		}
	}, src)

	info, err := c.Get(context.Background(), "pine-abc")
	if err != nil {
		t.Fatalf("Get after refresh: %v", err)
	}
	if info.ID != "pine-abc" {
		t.Errorf("info = %+v", info)
	}
	if calls := src.calls(); len(calls) != 2 || calls[0] != false || calls[1] != true {
		t.Errorf("token calls = %v, want [false true]", calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 2 {
		t.Errorf("server hit %d times, want 2", n)
	}
}

func TestDo_401Persists_Unauthorized(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"message":"bad token"}`)
	}, &fakeSource{initial: "a", refreshed: "b"})
	var e *UnauthorizedError
	if _, err := c.Get(context.Background(), "x"); !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *UnauthorizedError", err, err)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		check  func(error) bool
		msg    string
	}{
		{400, `{"message":"bad"}`, func(e error) bool { var x *BadRequestError; return errors.As(e, &x) }, "bad"},
		{404, `{"detail":"gone"}`, func(e error) bool { var x *NotFoundError; return errors.As(e, &x) }, "gone"},
		{409, `{"title":"busy"}`, func(e error) bool { var x *ConflictError; return errors.As(e, &x) }, "busy"},
		{422, `{"error":"nope"}`, func(e error) bool { var x *UnprocessableEntityError; return errors.As(e, &x) }, "nope"},
		{500, `{"message":"boom"}`, func(e error) bool { var x *ServerError; return errors.As(e, &x) }, "boom"},
		{418, `{}`, func(e error) bool { var x *ControlPlaneError; return errors.As(e, &x) }, "HTTP 418"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			// Use Get so the create-only OK set doesn't interfere; 404 on Get is an error
			// (Destroy is the only idempotent-404 path).
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}, &fakeSource{initial: "jws1"})
			_, err := c.Get(context.Background(), "x")
			if err == nil || !tc.check(err) {
				t.Fatalf("status %d → %T (%v)", tc.status, err, err)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("status %d message = %q, want it to contain %q", tc.status, err.Error(), tc.msg)
			}
		})
	}
}

// TestCreateComputer_CapacityExceeded: a 429 COMPUTER_CAPACITY_EXCEEDED is a typed,
// errors.Is-able refusal sent exactly once — create never retries it — while an unrelated
// 429 stays a generic ControlPlaneError.
func TestCreateComputer_CapacityExceeded(t *testing.T) {
	for _, tc := range []struct {
		code     string
		capacity bool
	}{{"COMPUTER_CAPACITY_EXCEEDED", true}, {"RATE_LIMITED", false}} {
		t.Run(tc.code, func(t *testing.T) {
			calls := 0
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(429)
				fmt.Fprintf(w, `{"code":%q,"message":"This project is at its limit of 2 concurrent Computers."}`, tc.code)
			}, &fakeSource{initial: "jws1"})
			_, err := c.CreateComputer(context.Background(), map[string]any{"timeout": 300})
			if got := errors.Is(err, ErrCapacityExceeded); got != tc.capacity {
				t.Fatalf("errors.Is(ErrCapacityExceeded) = %v for %T (%v)", got, err, err)
			}
			if tc.capacity {
				var ce *CapacityExceededError
				if !errors.As(err, &ce) || ce.Status != 429 || ce.Code != tc.code {
					t.Fatalf("err = %#v, want *CapacityExceededError{429, %s}", err, tc.code)
				}
				if !strings.Contains(err.Error(), "limit of 2 concurrent Computers") {
					t.Errorf("message = %q", err.Error())
				}
			} else {
				var ge *ControlPlaneError
				if !errors.As(err, &ge) || ge.Code != tc.code {
					t.Fatalf("err = %#v, want *ControlPlaneError with code %s", err, tc.code)
				}
			}
			if calls != 1 {
				t.Errorf("create sent %d times, want 1", calls)
			}
		})
	}
}

// TestCreateComputer_CreateRateLimited: a 429 COMPUTER_CREATE_RATE_LIMITED is its own typed
// refusal — the two 429 sentinels never match each other's code — that keeps the server's
// message and Retry-After and is sent exactly once, even on a create_id create.
func TestCreateComputer_CreateRateLimited(t *testing.T) {
	for _, tc := range []struct {
		code                string
		rateLimit, capacity bool
	}{
		{"COMPUTER_CREATE_RATE_LIMITED", true, false},
		{"COMPUTER_CAPACITY_EXCEEDED", false, true},
		{"RATE_LIMITED", false, false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			calls := 0
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(429)
				fmt.Fprintf(w, `{"code":%q,"message":"This project has started 5 new Computers in the last minute. Nothing was created; try again in 17 seconds."}`, tc.code)
			}, &fakeSource{initial: "jws1"})
			slept := recordSleeps(c)
			_, err := c.CreateComputer(context.Background(), map[string]any{"timeout": 300, "create_id": "0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000"})
			if got := errors.Is(err, ErrCreateRateLimited); got != tc.rateLimit {
				t.Fatalf("errors.Is(ErrCreateRateLimited) = %v for %T (%v)", got, err, err)
			}
			if got := errors.Is(err, ErrCapacityExceeded); got != tc.capacity {
				t.Fatalf("errors.Is(ErrCapacityExceeded) = %v for %T (%v)", got, err, err)
			}
			if tc.rateLimit {
				var rl *CreateRateLimitedError
				if !errors.As(err, &rl) || rl.Status != 429 || rl.Code != tc.code || rl.RetryAfter != 17*time.Second {
					t.Fatalf("err = %#v, want *CreateRateLimitedError{429, %s, RetryAfter 17s}", err, tc.code)
				}
				if !strings.Contains(err.Error(), "try again in 17 seconds") {
					t.Errorf("message = %q, want the server's message", err.Error())
				}
			}
			if calls != 1 || len(*slept) != 0 {
				t.Errorf("create sent %d times after waits %v, want once with no wait", calls, *slept)
			}
		})
	}
}

// TestStatusError_RetryAfter: the integer seconds of Retry-After ride every control-plane error
// (the 503 capacity wait too); an absent or malformed value is zero rather than a guess.
func TestStatusError_RetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		status       int
		want         time.Duration
	}{
		{"seconds", "17", 429, 17 * time.Second},
		{"capacity unavailable", "30", 503, 30 * time.Second},
		{"absent", "", 429, 0},
		{"http date", "Fri, 16 Oct 2026 07:28:00 GMT", 429, 0},
		{"fractional", "1.5", 429, 0},
		{"negative", "-5", 429, 0},
		{"zero", "0", 429, 0},
		{"overflowing", "9223372036854775807", 429, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				code := "COMPUTER_CREATE_RATE_LIMITED"
				if tc.status == 503 {
					code = "COMPUTER_CAPACITY_UNAVAILABLE"
				}
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"code":%q,"message":"try again later"}`, code)
			}, &fakeSource{initial: "jws1"})
			_, err := c.CreateComputer(context.Background(), map[string]any{"timeout": 300})
			var got time.Duration
			var rl *CreateRateLimitedError
			var se *ServerError
			switch {
			case errors.As(err, &rl):
				got = rl.RetryAfter
			case errors.As(err, &se):
				got = se.RetryAfter
			default:
				t.Fatalf("err = %T (%v), want a typed control-plane error", err, err)
			}
			if got != tc.want {
				t.Errorf("RetryAfter = %v for Retry-After %q, want %v", got, tc.header, tc.want)
			}
		})
	}
}

const testCreateID = "0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000"

// busyServer answers the first `busy` creates 503 COMPUTER_CREATE_BUSY with retryAfter (no
// header when ""), then 202, counting the creates and checking each repeats the same body.
func busyServer(t *testing.T, busy int, retryAfter string) (http.HandlerFunc, *int) {
	t.Helper()
	calls := 0
	var first string
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if calls++; calls == 1 {
			first = string(body)
		} else if string(body) != first {
			t.Errorf("repeat %d body = %s, want the first create's %s", calls, body, first)
		}
		if calls > busy {
			w.WriteHeader(202)
			fmt.Fprint(w, sandboxJSON)
			return
		}
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(503)
		fmt.Fprint(w, `{"code":"COMPUTER_CREATE_BUSY","message":"Nothing was created; retry after the Retry-After delay."}`)
	}, &calls
}

// recordSleeps replaces the busy-create wait with one that records each wait and returns.
func recordSleeps(c *Client) *[]time.Duration {
	var slept []time.Duration
	c.sleeper = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	return &slept
}

func wantBusyError(t *testing.T, err error) {
	t.Helper()
	var se *ServerError
	if !errors.As(err, &se) || se.Status != 503 || se.Code != "COMPUTER_CREATE_BUSY" {
		t.Fatalf("err = %#v, want the busy *ServerError", err)
	}
}

// TestCreateComputer_BusyKeyedCreateRepeats: a keyed create the server answered busy waits its
// Retry-After (a second when there is none usable) and repeats the same request once.
func TestCreateComputer_BusyKeyedCreateRepeats(t *testing.T) {
	for _, tc := range []struct {
		retryAfter string
		want       time.Duration
	}{{"3", 3 * time.Second}, {"", time.Second}, {"soon", time.Second}} {
		t.Run(fmt.Sprintf("Retry-After %q", tc.retryAfter), func(t *testing.T) {
			handler, calls := busyServer(t, 1, tc.retryAfter)
			c := newTestClient(t, handler, &fakeSource{initial: "jws1"})
			slept := recordSleeps(c)
			info, err := c.CreateComputer(context.Background(), map[string]any{"timeout": 300, "create_id": testCreateID})
			if err != nil || info.ID != "pine-abc" {
				t.Fatalf("info=%+v err=%v, want the repeat's runtime", info, err)
			}
			if *calls != 2 || len(*slept) != 1 || (*slept)[0] != tc.want {
				t.Fatalf("creates=%d waits=%v, want 2 creates after one %v wait", *calls, *slept, tc.want)
			}
		})
	}
}

// TestCreateComputer_BusyNotRepeated: an unkeyed create may have no safe repeat, and no other
// refusal is busy — each returns its error after the one create, with no wait.
func TestCreateComputer_BusyNotRepeated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
		keyed  bool
	}{
		{"unkeyed busy", 503, "COMPUTER_CREATE_BUSY", false},
		{"capacity unavailable", 503, "COMPUTER_CAPACITY_UNAVAILABLE", true},
		{"create rate limited", 429, "COMPUTER_CREATE_RATE_LIMITED", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"code":%q,"message":"not now"}`, tc.code)
			}, &fakeSource{initial: "jws1"})
			slept := recordSleeps(c)
			body := map[string]any{"timeout": 300}
			if tc.keyed {
				body["create_id"] = testCreateID
			}
			_, err := c.CreateComputer(context.Background(), body)
			if err == nil || !strings.Contains(err.Error(), "not now") {
				t.Fatalf("err = %v, want the server's refusal", err)
			}
			if calls != 1 || len(*slept) != 0 {
				t.Fatalf("creates=%d waits=%v, want one create and no wait", calls, *slept)
			}
		})
	}
}

// TestCreateComputer_BusyRepeatsAreBounded: a create the server keeps answering busy repeats
// ten times, then returns the busy error as a *ServerError.
func TestCreateComputer_BusyRepeatsAreBounded(t *testing.T) {
	handler, calls := busyServer(t, 1000, "1")
	c := newTestClient(t, handler, &fakeSource{initial: "jws1"})
	slept := recordSleeps(c)
	_, err := c.CreateComputer(context.Background(), map[string]any{"timeout": 300, "create_id": testCreateID})
	wantBusyError(t, err)
	if *calls != 11 || len(*slept) != 10 {
		t.Fatalf("creates=%d waits=%d, want 11 creates after 10 waits", *calls, len(*slept))
	}
}

// TestCreateComputer_BusyWaitPastDeadlineReturns: when the busy wait would not fit before the
// caller's deadline, the create returns the busy error at once instead of waiting.
func TestCreateComputer_BusyWaitPastDeadlineReturns(t *testing.T) {
	handler, calls := busyServer(t, 1000, "2")
	c := newTestClient(t, handler, &fakeSource{initial: "jws1"})
	c.sleeper = func(context.Context, time.Duration) error {
		t.Error("waited although the wait would outlast the deadline")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.CreateComputer(ctx, map[string]any{"timeout": 300, "create_id": testCreateID})
	wantBusyError(t, err)
	if ctx.Err() != nil || *calls != 1 {
		t.Fatalf("ctx.Err()=%v creates=%d, want one create returned before the deadline", ctx.Err(), *calls)
	}
}

// TestErrorCarriesResourceContext: a control-plane failure is self-describing — the error
// string names WHICH host, WHICH operation (`GET /sandboxes/<id>`), and the request id.
func TestErrorCarriesResourceContext(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-cp")
		w.WriteHeader(404)
		fmt.Fprint(w, `{"message":"no such sandbox"}`)
	}, &fakeSource{initial: "jws1"})
	_, err := c.Get(context.Background(), "sbx-1")
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %T (%v), want *NotFoundError", err, err)
	}
	msg := err.Error()
	for _, want := range []string{"host=", "op=GET /sandboxes/sbx-1", "request_id=req-cp"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	if nf.Op != "GET /sandboxes/sbx-1" || nf.RequestID != "req-cp" || nf.Host == "" {
		t.Errorf("fields: Host=%q Op=%q RequestID=%q", nf.Host, nf.Op, nf.RequestID)
	}
}

func TestSpecVersionMismatch(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Computer-Spec-Version", "2")
		fmt.Fprint(w, sandboxJSON)
	}, &fakeSource{initial: "jws1"})
	var me *spec.MismatchError
	if _, err := c.Get(context.Background(), "x"); !errors.As(err, &me) {
		t.Fatalf("err = %T (%v), want *spec.MismatchError", err, err)
	}
}

func TestKeyedCreateReplaysLostReplyWithSameIdentity(t *testing.T) {
	const createID = "11111111-1111-4111-8111-111111111111"
	var calls int
	var bodies []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		calls++
		if calls == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close() // Admission succeeded, its response was lost.
			return
		}
		w.WriteHeader(202)
		fmt.Fprint(w, `{"id":"same-runtime","status":{"state":"Pending"}}`)
	}, &fakeSource{initial: "jws"})
	info, err := c.CreateComputer(context.Background(), map[string]any{"timeout": 300, "create_id": createID})
	if err != nil || info.ID != "same-runtime" || calls != 2 || bodies[0] != bodies[1] {
		t.Fatalf("keyed retry: info=%+v err=%v calls=%d bodies=%v", info, err, calls, bodies)
	}
}

type waitRoundTripper func(*http.Request) (*http.Response, error)

func (f waitRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConditionalWaitHonorsCallerDeadlineWithoutNegotiatingServerTiming(t *testing.T) {
	for _, short := range []bool{false, true} {
		ctx := context.Background()
		cancel := func() {}
		maxBudget := 35 * time.Second
		if short {
			ctx, cancel = context.WithTimeout(ctx, 12*time.Second)
			maxBudget = 12 * time.Second
		}
		raw := transport.New("https", "api.example.test", transport.WithHTTPClient(&http.Client{Transport: waitRoundTripper(func(r *http.Request) (*http.Response, error) {
			deadline, ok := r.Context().Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining > maxBudget || remaining < maxBudget-time.Second || r.Header.Get("Prefer") != "" {
				t.Errorf("budget=%v wait=%s", remaining, r.Header.Get("Prefer"))
			}
			return &http.Response{StatusCode: 304, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}))
		c := NewClient(raw, &fakeSource{initial: "test"}, 1)
		_, err := c.WaitForChange(ctx, "computer", &SandboxInfo{Status: "pausing", ETag: `"uid/1"`})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
}
