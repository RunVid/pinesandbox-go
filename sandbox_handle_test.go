package pinesandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.pinesandbox.io/computer/internal/controlplane"
)

type fakeCP struct {
	get      func(ctx context.Context, id string) (*controlplane.SandboxInfo, error)
	destroy  func(ctx context.Context, id string) error
	destroys int
}

func (f *fakeCP) Get(ctx context.Context, id string) (*controlplane.SandboxInfo, error) {
	return f.get(ctx, id)
}

func (f *fakeCP) Destroy(ctx context.Context, id string) error {
	f.destroys++
	if f.destroy != nil {
		return f.destroy(ctx, id)
	}
	return nil
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
