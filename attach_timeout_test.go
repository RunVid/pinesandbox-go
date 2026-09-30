package pinesandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAttach_InvalidReadinessBudgetDoesNotMutateBinding(t *testing.T) {
	comp := newComputer("0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000", make([]byte, 32))
	err := comp.Attach(context.Background(), nil, AttachOptions{
		ReadyTimeout: -time.Second, BindingRevision: 3, Ephemeral: true,
	})
	if err == nil {
		t.Fatal("Attach accepted a negative readiness budget")
	}
	if comp.BindingRevision() != 0 {
		t.Fatal("invalid attach mutated the binding revision")
	}
}

// Cold provisioning uses the readiness budget independently of Computer lifetime.
func TestAttach_ProvisionBoundedByReadinessBudget(t *testing.T) {
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/computer-sandboxes" && r.Method == http.MethodPost {
			time.Sleep(500 * time.Millisecond) // slower than the 50ms budget below
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer control.Close()

	conn := buildTestConnection(t, control.URL, control.URL)
	comp := newComputer("0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000", make([]byte, 32))
	capture, err := GenerateCaptureKeypair(1)
	if err != nil {
		t.Fatal(err)
	}

	err = comp.Attach(context.Background(), conn, AttachOptions{
		Timeout:        8 * time.Hour,
		ReadyTimeout:   50 * time.Millisecond,
		CaptureKeypair: capture,
	})

	// The provision times out within the budget — not a bind error after a 30s fallback.
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("Attach err = %T (%v), want the provision to time out (*TimeoutError) within the readiness budget", err, err)
	}
}

func TestAttach_ReadinessFailureDeletesAllocation(t *testing.T) {
	for _, cause := range []string{"deadline", "caller cancellation", "failed pod"} {
		t.Run(cause, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var deleted atomic.Int32
			control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/computer-sandboxes":
					var body struct {
						Timeout int `json:"timeout"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.Timeout != 28800 {
						t.Errorf("lifetime = %d, want 28800", body.Timeout)
					}
					w.WriteHeader(http.StatusAccepted)
					fmt.Fprint(w, `{"id":"sb-pending","status":{"state":"Pending"}}`)
				case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/sb-pending":
					if cause == "caller cancellation" {
						cancel()
					}
					state := "Pending"
					if cause == "failed pod" {
						state = "Failed"
					}
					fmt.Fprintf(w, `{"id":"sb-pending","status":{"state":%q}}`, state)
				case r.Method == http.MethodDelete && r.URL.Path == "/sandboxes/sb-pending":
					deleted.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			defer control.Close()
			conn := buildTestConnection(t, control.URL, control.URL)
			comp := newComputer("0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000", make([]byte, 32))
			err := comp.Attach(ctx, conn, AttachOptions{Timeout: 8 * time.Hour, ReadyTimeout: 100 * time.Millisecond, Ephemeral: true})
			if err == nil {
				t.Fatal("attach unexpectedly succeeded")
			}
			var rt *ReadyTimeoutError
			var failed *SandboxFailedError
			switch cause {
			case "deadline":
				if !errors.As(err, &rt) {
					t.Fatalf("err = %T (%v), want *ReadyTimeoutError when the readiness budget expires", err, err)
				}
				if rt.SandboxID != "sb-pending" || rt.LastState != "pending" {
					t.Fatalf("ReadyTimeoutError = %+v, want sandbox sb-pending last state pending", rt)
				}
			case "caller cancellation":
				if errors.As(err, &rt) {
					t.Fatalf("caller cancellation reported as readiness timeout: %v", err)
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err = %T (%v), want caller cancellation", err, err)
				}
			case "failed pod":
				if !errors.As(err, &failed) {
					t.Fatalf("err = %T (%v), want *SandboxFailedError", err, err)
				}
			}
			if deleted.Load() != 1 {
				t.Fatalf("DELETE count = %d, want 1 after %s", deleted.Load(), cause)
			}
			if comp.SandboxID() != "" {
				t.Fatal("failed attach retained an allocation")
			}
		})
	}
}
