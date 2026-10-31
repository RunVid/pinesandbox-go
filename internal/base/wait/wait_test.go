package wait

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSleep_CompletesAfterDuration(t *testing.T) {
	start := time.Now()
	if err := Sleep(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Errorf("returned after %v, want >= ~20ms", elapsed)
	}
}

func TestSleep_ReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	start := time.Now()
	err := Sleep(ctx, 10*time.Second) // would block 10s without cancellation
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("did not return promptly on cancel (took %v)", elapsed)
	}
}

// jitterClock is a Clock whose jitter is fixed; it never sleeps.
type jitterClock float64

func (jitterClock) Now() time.Time                             { return time.Unix(1_760_000_000, 0) }
func (jitterClock) Sleep(context.Context, time.Duration) error { return nil }
func (c jitterClock) Jitter() float64                          { return float64(c) }

// TestReconnectBackoff_FollowsTheSharedCadence: 500ms·2^(attempt-1) plus the clock's
// jitter (up to 200ms), capped at 5s.
func TestReconnectBackoff_FollowsTheSharedCadence(t *testing.T) {
	for _, tc := range []struct {
		jitter  float64
		attempt int
		want    time.Duration
	}{
		{0, 1, 500 * time.Millisecond},
		{0, 2, time.Second},
		{0, 4, 4 * time.Second},
		{0, 5, 5 * time.Second},
		{0.5, 1, 600 * time.Millisecond},
		{0.5, 4, 4100 * time.Millisecond},
		{0.99, 9, 5 * time.Second},
	} {
		if got := ReconnectBackoff(jitterClock(tc.jitter), tc.attempt); got != tc.want {
			t.Errorf("ReconnectBackoff(jitter %v, attempt %d) = %v, want %v", tc.jitter, tc.attempt, got, tc.want)
		}
	}
}

func TestSleep_NonPositiveHonorsCancelledContext(t *testing.T) {
	if err := Sleep(context.Background(), 0); err != nil {
		t.Errorf("Sleep(live ctx, 0) = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("Sleep(cancelled ctx, 0) = %v, want context.Canceled", err)
	}
}
