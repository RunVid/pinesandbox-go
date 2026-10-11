package pinesandbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.pinesandbox.io/computer/internal/controlplane"
)

func TestComputerMemoryTransitionsPreserveBinding(t *testing.T) {
	for _, target := range []string{"paused", "running"} {
		t.Run(target, func(t *testing.T) {
			get, reads := statusSequence("resuming", target)
			requests := 0
			request := func(ctx context.Context, id string) error {
				requests++
				deadline, bounded := ctx.Deadline()
				if id != "sb-1" || !bounded || time.Until(deadline) > defaultMemoryWait {
					t.Fatalf("unbounded or misrouted transition: id=%q deadline=%v", id, deadline)
				}
				return nil
			}
			h, _ := newTestHandle(&fakeCP{get: get, pause: request, resume: request}, statusRunning)
			comp := newComputer("c1", make([]byte, 32))
			comp.sandbox, comp.computerToken, comp.bindingRevision = h, "ct_original", 4
			operation := comp.Pause
			if target == statusRunning {
				operation = comp.Resume
			}
			if err := operation(context.Background()); err != nil {
				t.Fatal(err)
			}
			if requests != 1 || *reads != 2 {
				t.Fatalf("requests=%d reads=%d: acceptance/cached Running must not finish the operation", requests, *reads)
			}
			if comp.sandbox != h || comp.ComputerToken() != "ct_original" || comp.BindingRevision() != 4 {
				t.Fatal("memory transition replaced the binding")
			}
		})
	}
}

func TestReconstructedComputerStatusPreservesLifecycleAndErrors(t *testing.T) {
	var state atomic.Value
	state.Store("Paused")
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/sandboxes/sb-1" {
			t.Errorf("unexpected lifecycle mutation: %s %s", r.Method, r.URL.Path)
		}
		code := int(status.Load())
		w.WriteHeader(code)
		if code == http.StatusOK {
			fmt.Fprintf(w, `{"id":"sb-1","status":{"state":%q}}`, state.Load())
		}
	}))
	defer srv.Close()
	client := &Client{conn: buildTestConnection(t, srv.URL, "http://unused")}
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	comp, err := client.AdoptExisting(context.Background(), creds.ID, creds.Key, "sb-1", "ct_saved")
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []string{"Paused", "Resuming", "Running", "Unavailable", "Stopping"} {
		state.Store(next)
		got, err := comp.Status(context.Background())
		if err != nil || got != strings.ToLower(next) {
			t.Fatalf("status: got %q error %v, want %q", got, err, next)
		}
	}
	status.Store(http.StatusNotFound)
	_, err = comp.Status(context.Background())
	var missing *NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("absence must remain a typed error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := comp.Status(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation must not become absence: %v", err)
	}
	if comp.ComputerToken() != "ct_saved" {
		t.Fatal("observation discarded the persisted binding")
	}
}

func TestMemoryTransitionFailureRetainsBinding(t *testing.T) {
	for _, state := range []string{"unavailable", "failed", "terminated"} {
		for _, target := range []string{"paused", "running"} {
			t.Run(target+"/"+state, func(t *testing.T) {
				get, _ := statusSequence(state)
				request := func(context.Context, string) error { return nil }
				cp := &fakeCP{get: get, pause: request, resume: request}
				h, _ := newTestHandle(cp, statusRunning)
				comp := newComputer("c1", make([]byte, 32))
				comp.sandbox, comp.computerToken = h, "ct_original"
				var failed *SandboxFailedError
				err := comp.transitionMemory(context.Background(), target)
				if !errors.As(err, &failed) || failed.State != state {
					t.Fatalf("error=%v", err)
				}
				if comp.sandbox != h || comp.ComputerToken() != "ct_original" || cp.destroys != 0 {
					t.Fatal("failed transition discarded its binding or deleted its runtime")
				}
			})
		}
	}
}

func TestMemoryTransitionConflictRequiresObservedTarget(t *testing.T) {
	for _, state := range []string{"paused", "running", "resuming"} {
		get, reads := statusSequence(state)
		conflict := &ConflictError{}
		conflict.Status = 409
		cp := &fakeCP{get: get, resume: func(context.Context, string) error { return conflict }}
		h, _ := newTestHandle(cp, statusRunning)
		err := h.transitionMemory(context.Background(), statusRunning)
		if (state == statusRunning) != (err == nil) || *reads != 1 {
			t.Fatalf("state=%s error=%v reads=%d", state, err, *reads)
		}
		if err != nil && !errors.Is(err, conflict) {
			t.Fatalf("conflict lost: %v", err)
		}
	}
}

func TestMemoryTransitionOneBudgetIncludesAcceptanceAndPolling(t *testing.T) {
	get, _ := statusSequence("resuming")
	cp := &fakeCP{get: get}
	h, now := newTestHandle(cp, statusRunning)
	started := *now
	cp.resume = func(context.Context, string) error { *now = now.Add(5 * time.Minute); return nil }
	var timeout *ReadyTimeoutError
	if err := h.transitionMemory(context.Background(), statusRunning); !errors.As(err, &timeout) {
		t.Fatalf("error=%v", err)
	}
	if elapsed := now.Sub(started); elapsed != defaultMemoryWait {
		t.Fatalf("operation took %v; acceptance must share the %v budget", elapsed, defaultMemoryWait)
	}
}

func TestMemoryTransitionCancellationBoundsRequestAndPoll(t *testing.T) {
	for _, blocked := range []string{"request", "poll", "sleep"} {
		t.Run(blocked, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			cp := &fakeCP{
				resume: func(ctx context.Context, _ string) error {
					if blocked == "request" {
						<-ctx.Done()
						return ctx.Err()
					}
					return nil
				},
				get: func(ctx context.Context, _ string) (*controlplane.SandboxInfo, error) {
					if blocked == "poll" {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return &controlplane.SandboxInfo{Status: "resuming"}, nil
				},
			}
			h := newSandboxHandle(cp, "sb-1", statusRunning)
			if err := h.transitionMemory(ctx, statusRunning); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("caller deadline was not preserved: %v", err)
			}
		})
	}
}
