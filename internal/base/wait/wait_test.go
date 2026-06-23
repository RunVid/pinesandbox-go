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
