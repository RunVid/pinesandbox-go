package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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

func TestCreate_SuccessAndHeaders(t *testing.T) {
	var gotAuth, gotSpec, gotIdem, gotCT string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotSpec = r.Header.Get("Computer-Spec-Version")
		gotIdem = r.Header.Get("Idempotency-Key")
		gotCT = r.Header.Get("Content-Type")
		if r.Method != "POST" || r.URL.Path != "/sandboxes" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(202)
		fmt.Fprint(w, sandboxJSON)
	}, &fakeSource{initial: "jws1"})

	info, err := c.Create(context.Background(), map[string]any{"image": map[string]string{"uri": "reg/img:1"}}, "idem-1")
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
	if gotIdem != "idem-1" {
		t.Errorf("Idempotency-Key = %q", gotIdem)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
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

func TestPauseResume_409IsFalse(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{{202, true}, {409, false}, {200, true}}
	for _, tc := range cases {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/pause") {
				t.Errorf("path = %s", r.URL.Path)
			}
			w.WriteHeader(tc.status)
		}, &fakeSource{initial: "jws1"})
		got, err := c.Pause(context.Background(), "x")
		if err != nil {
			t.Fatalf("Pause(%d): %v", tc.status, err)
		}
		if got != tc.want {
			t.Errorf("Pause(%d) = %v, want %v", tc.status, got, tc.want)
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
