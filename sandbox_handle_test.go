package pinesandbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"go.pinesandbox.io/computer/internal/controlplane"
)

type fakeCP struct {
	wait     func(context.Context, string, *controlplane.SandboxInfo) (*controlplane.SandboxInfo, error)
	stop     func(context.Context, string, string) error
	get      func(ctx context.Context, id string) (*controlplane.SandboxInfo, error)
	destroy  func(ctx context.Context, id string) error
	destroys int
	pause    func(context.Context, string) error
	resume   func(context.Context, string) error
}

func (f *fakeCP) SetIdleTimeout(ctx context.Context, id string, seconds int, etag string) (*controlplane.SandboxInfo, error) {
	return nil, fmt.Errorf("unexpected idle-timeout update")
}

func (f *fakeCP) Get(ctx context.Context, id string) (*controlplane.SandboxInfo, error) {
	return f.get(ctx, id)
}

func (f *fakeCP) WaitForChange(ctx context.Context, id string, previous *controlplane.SandboxInfo) (*controlplane.SandboxInfo, error) {
	if f.wait != nil {
		return f.wait(ctx, id, previous)
	}
	info, err := f.get(ctx, id)
	return info, err
}

func TestLifecycleUsesConditionalWaitWithoutPeriodicSleep(t *testing.T) {
	reads, waits := 0, 0
	var now *time.Time
	cp := &fakeCP{get: func(context.Context, string) (*controlplane.SandboxInfo, error) {
		reads++
		return &controlplane.SandboxInfo{Status: "resuming", ETag: `"uid/1"`}, nil
	}, wait: func(_ context.Context, _ string, previous *controlplane.SandboxInfo) (*controlplane.SandboxInfo, error) {
		waits++
		if previous.ETag != `"uid/1"` {
			t.Fatal("lost status validator")
		}
		if waits == 1 {
			*now = now.Add(25 * time.Second)
			return previous, nil
		}
		return &controlplane.SandboxInfo{Status: "running", ETag: `"uid/2"`}, nil
	}}
	h, clock := newTestHandle(cp, "")
	now = clock
	h.sleeper = func(context.Context, time.Duration) error { t.Fatal("event-driven wait slept"); return nil }
	if err := h.WaitUntilRunning(context.Background(), time.Minute, time.Second); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || waits != 2 {
		t.Fatalf("reads=%d waits=%d", reads, waits)
	}
}

func TestConditionalWaitPacesOnlyQuickUnchangedReads(t *testing.T) {
	for _, elapsed := range []time.Duration{0, 500 * time.Millisecond, 25 * time.Second} {
		t.Run(elapsed.String(), func(t *testing.T) {
			previous := &controlplane.SandboxInfo{Status: "resuming", ETag: `"uid/1"`}
			cp := &fakeCP{}
			h, now := newTestHandle(cp, "resuming")
			cp.wait = func(context.Context, string, *controlplane.SandboxInfo) (*controlplane.SandboxInfo, error) {
				*now = now.Add(elapsed)
				return previous, nil
			}
			current, delay, err := h.observe(context.Background(), previous, 2*time.Second)
			if err != nil || current != previous || delay != max(0, 2*time.Second-elapsed) {
				t.Fatalf("current=%+v delay=%v err=%v", current, delay, err)
			}
		})
	}
}

func TestConditionalStopWaitPreservesTimeoutAndCancellation(t *testing.T) {
	for _, callerCancels := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cp := &fakeCP{get: func(context.Context, string) (*controlplane.SandboxInfo, error) {
			return &controlplane.SandboxInfo{Status: "stopping", ETag: `"uid/1"`}, nil
		}}
		h, now := newTestHandle(cp, "stopping")
		cp.wait = func(context.Context, string, *controlplane.SandboxInfo) (*controlplane.SandboxInfo, error) {
			if callerCancels {
				cancel()
			} else {
				*now = now.Add(390 * time.Second)
			}
			return nil, &TimeoutError{Msg: "wait ended"}
		}
		stopped, err := h.Stop(ctx)
		if stopped || (callerCancels && !errors.Is(err, context.Canceled)) || (!callerCancels && err != nil) {
			t.Fatalf("callerCancels=%v: stopped=%v err=%v", callerCancels, stopped, err)
		}
		cancel()
	}
}

func (f *fakeCP) Destroy(ctx context.Context, id string) error {
	f.destroys++
	if f.destroy != nil {
		return f.destroy(ctx, id)
	}
	return nil
}

func (f *fakeCP) Pause(ctx context.Context, id string) error         { return f.pause(ctx, id) }
func (f *fakeCP) Release(ctx context.Context, id, etag string) error { return f.destroy(ctx, id) }
func (f *fakeCP) Resume(ctx context.Context, id string) error        { return f.resume(ctx, id) }
func (f *fakeCP) Stop(ctx context.Context, id, operationID string) error {
	return f.stop(ctx, id, operationID)
}

// statusSequence returns a Get func yielding the given statuses in order (last repeats).
func statusSequence(states ...string) (func(context.Context, string) (*controlplane.SandboxInfo, error), *int) {
	i := 0
	calls := 0
	return func(context.Context, string) (*controlplane.SandboxInfo, error) {
		calls++
		s := states[i]
		if i < len(states)-1 {
			i++
		}
		return &controlplane.SandboxInfo{Status: s}, nil
	}, &calls
}

func newTestHandle(cp sandboxControl, status string) (*SandboxHandle, *time.Time) {
	h := newSandboxHandle(cp, "sb-1", status)
	now := time.Unix(1700000000, 0)
	h.clock = func() time.Time { return now }
	h.sleeper = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }
	return h, &now
}

func TestWaitUntilRunning_SkipsPollWhenAlreadyRunning(t *testing.T) {
	get, calls := statusSequence("pending") // would never reach running
	h, _ := newTestHandle(&fakeCP{get: get}, statusRunning)
	if err := h.WaitUntilRunning(context.Background(), 10*time.Second, time.Second); err != nil {
		t.Fatalf("WaitUntilRunning: %v", err)
	}
	if *calls != 0 {
		t.Errorf("Get called %d times, want 0 (create-time Running skips the poll)", *calls)
	}
}

func TestWaitUntilRunning_PollsToRunning(t *testing.T) {
	get, calls := statusSequence("pending", "pending", "running")
	h, _ := newTestHandle(&fakeCP{get: get}, "pending")
	if err := h.WaitUntilRunning(context.Background(), 60*time.Second, time.Second); err != nil {
		t.Fatalf("WaitUntilRunning: %v", err)
	}
	if *calls != 3 {
		t.Errorf("Get called %d times, want 3", *calls)
	}
}

func TestWaitUntilRunning_TerminalState(t *testing.T) {
	get, _ := statusSequence("pending", "failed")
	h, _ := newTestHandle(&fakeCP{get: get}, "pending")
	var sf *SandboxFailedError
	if err := h.WaitUntilRunning(context.Background(), 60*time.Second, time.Second); !errors.As(err, &sf) {
		t.Fatalf("err = %T (%v), want *SandboxFailedError", err, err)
	} else if sf.State != "failed" {
		t.Errorf("State = %q", sf.State)
	}
}

func TestWaitUntilRunning_Timeout(t *testing.T) {
	get, _ := statusSequence("pending") // never ready
	h, _ := newTestHandle(&fakeCP{get: get}, "pending")
	var rt *ReadyTimeoutError
	if err := h.WaitUntilRunning(context.Background(), 5*time.Second, time.Second); !errors.As(err, &rt) {
		t.Fatalf("err = %T (%v), want *ReadyTimeoutError", err, err)
	}
}

func TestTerminate_ConfirmsGone(t *testing.T) {
	destroyed := false
	cp := &fakeCP{
		get: func(context.Context, string) (*controlplane.SandboxInfo, error) {
			if destroyed {
				return nil, &NotFoundError{}
			}
			return &controlplane.SandboxInfo{Status: "stopping"}, nil
		},
		destroy: func(context.Context, string) error { destroyed = true; return nil },
	}
	h, _ := newTestHandle(cp, "running")
	gone, err := h.Terminate(context.Background(), 30*time.Second, time.Second)
	if err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if !gone {
		t.Error("Terminate returned false, want true (404 confirms gone)")
	}
	if cp.destroys != 1 {
		t.Errorf("destroys = %d, want 1", cp.destroys)
	}
}

func TestTerminate_NotConfirmedWithinBudget(t *testing.T) {
	cp := &fakeCP{
		get: func(context.Context, string) (*controlplane.SandboxInfo, error) {
			return &controlplane.SandboxInfo{Status: "stopping"}, nil
		},
		destroy: func(context.Context, string) error { return nil },
	}
	h, _ := newTestHandle(cp, "running")
	gone, err := h.Terminate(context.Background(), 5*time.Second, time.Second)
	if err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if gone {
		t.Error("Terminate returned true, want false (never observed 404)")
	}
}

func TestTerminate_BoundsBlockedDeleteAndStatusRequests(t *testing.T) {
	for _, phase := range []string{"delete", "status"} {
		t.Run(phase, func(t *testing.T) {
			parent, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			block := func(ctx context.Context) error {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 100*time.Millisecond {
					t.Fatal("request did not inherit the deletion budget")
				}
				<-ctx.Done()
				return ctx.Err()
			}
			cp := &fakeCP{
				destroy: func(ctx context.Context, _ string) error {
					if phase == "delete" {
						return block(ctx)
					}
					return nil
				},
				get: func(ctx context.Context, _ string) (*controlplane.SandboxInfo, error) {
					return nil, block(ctx)
				},
			}
			h := newSandboxHandle(cp, "sb-1", "running")
			gone, err := h.Terminate(parent, 20*time.Millisecond, time.Second)
			if gone || err != nil {
				t.Fatalf("expired confirmation = %v, %v; want false, nil", gone, err)
			}
			if parent.Err() != nil {
				t.Fatal("wait consumed the caller's longer deadline")
			}
		})
	}
}

func TestTerminate_DeleteAndSleepConsumeTheSameBudget(t *testing.T) {
	cp := &fakeCP{get: func(context.Context, string) (*controlplane.SandboxInfo, error) {
		return &controlplane.SandboxInfo{Status: "stopping"}, nil
	}}
	h, now := newTestHandle(cp, "running")
	start := *now
	cp.destroy = func(context.Context, string) error { *now = now.Add(2 * time.Second); return nil }
	gone, err := h.Terminate(context.Background(), 5*time.Second, 2*time.Second)
	if gone || err != nil || now.Sub(start) != 5*time.Second {
		t.Fatalf("terminate = %v, %v, elapsed %s", gone, err, now.Sub(start))
	}
}

// TestTerminate_ContextCancelAborts: a cancelled ctx stops the confirm-gone poll promptly
// instead of blocking the full wait budget (the ctx-aware poll fix).
func TestTerminate_ContextCancelAborts(t *testing.T) {
	cp := &fakeCP{
		get: func(context.Context, string) (*controlplane.SandboxInfo, error) {
			return &controlplane.SandboxInfo{Status: "stopping"}, nil
		},
		destroy: func(context.Context, string) error { return nil },
	}
	h := newSandboxHandle(cp, "sb-1", "running")
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	// Real-clock handle; the sleeper cancels on the first poll and reports it (like wait.Sleep).
	h.sleeper = func(c context.Context, _ time.Duration) error {
		calls++
		cancel()
		return c.Err()
	}
	gone, err := h.Terminate(ctx, 90*time.Second, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if gone {
		t.Error("gone = true, want false on cancellation")
	}
	if calls > 1 {
		t.Errorf("polled %d times after cancel — should stop promptly", calls)
	}
}

func TestAlive(t *testing.T) {
	running := &fakeCP{get: func(context.Context, string) (*controlplane.SandboxInfo, error) {
		return &controlplane.SandboxInfo{Status: "running"}, nil
	}}
	h, _ := newTestHandle(running, "running")
	if !h.Alive(context.Background()) {
		t.Error("Alive = false for running")
	}
	dead := &fakeCP{get: func(context.Context, string) (*controlplane.SandboxInfo, error) { return nil, &NotFoundError{} }}
	h2, _ := newTestHandle(dead, "running")
	if h2.Alive(context.Background()) {
		t.Error("Alive = true for a 404'd sandbox")
	}
}

func TestConditionalWaitRetriesTransientFaultsWithinLifecycleBudget(t *testing.T) {
	attempts := 0
	cp := &fakeCP{get: func(context.Context, string) (*controlplane.SandboxInfo, error) {
		return &controlplane.SandboxInfo{Status: "resuming", ETag: `"uid/1"`}, nil
	}, wait: func(_ context.Context, _ string, previous *controlplane.SandboxInfo) (*controlplane.SandboxInfo, error) {
		attempts++
		if previous.ETag != `"uid/1"` {
			t.Fatal("transport fault changed the last observation")
		}
		switch attempts {
		case 1:
			return nil, &TimeoutError{Msg: "conditional read timed out"}
		case 2:
			return nil, &ConnectionError{Msg: "connection reset"}
		case 3, 4, 5:
			err := &ServerError{}
			err.Status = []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}[attempts-3]
			return nil, err
		default:
			return &controlplane.SandboxInfo{Status: "running", ETag: `"uid/2"`}, nil
		}
	}}
	h, now := newTestHandle(cp, "resuming")
	started := *now
	if err := h.WaitUntilRunning(context.Background(), time.Minute, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if attempts != 6 || now.Sub(started) != 10*time.Second {
		t.Fatalf("attempts=%d paced=%v", attempts, now.Sub(started))
	}
}

func TestConditionalWaitBoundsPersistentGatewayFailure(t *testing.T) {
	err := &ServerError{}
	err.Status = http.StatusBadGateway
	cp := &fakeCP{get: func(context.Context, string) (*controlplane.SandboxInfo, error) {
		return &controlplane.SandboxInfo{Status: "resuming", ETag: `"uid/1"`}, nil
	}, wait: func(context.Context, string, *controlplane.SandboxInfo) (*controlplane.SandboxInfo, error) {
		return nil, err
	}}
	h, now := newTestHandle(cp, "resuming")
	started := *now
	var timeout *ReadyTimeoutError
	if got := h.WaitUntilRunning(context.Background(), 5*time.Second, 2*time.Second); !errors.As(got, &timeout) {
		t.Fatalf("err=%v, want readiness timeout", got)
	}
	if now.Sub(started) != 5*time.Second {
		t.Fatalf("wait exceeded caller budget: %v", now.Sub(started))
	}
}

func TestConditionalWaitPreservesNonTransientErrors(t *testing.T) {
	internal := &ServerError{}
	internal.Status = http.StatusInternalServerError
	unsupported := &ServerError{}
	unsupported.Status = http.StatusNotImplemented
	for _, failure := range []error{&UnauthorizedError{}, &ForbiddenError{}, &NotFoundError{}, &ConflictError{}, &CapacityExceededError{}, internal, unsupported} {
		cp := &fakeCP{get: func(context.Context, string) (*controlplane.SandboxInfo, error) {
			return &controlplane.SandboxInfo{Status: "resuming", ETag: `"uid/1"`}, nil
		}, wait: func(context.Context, string, *controlplane.SandboxInfo) (*controlplane.SandboxInfo, error) {
			return nil, failure
		}}
		h, _ := newTestHandle(cp, "resuming")
		h.sleeper = func(context.Context, time.Duration) error { t.Fatal("non-transient failure was retried"); return nil }
		if err := h.WaitUntilRunning(context.Background(), time.Minute, time.Second); err != failure {
			t.Fatalf("err=%v, want original %v", err, failure)
		}
	}
}
