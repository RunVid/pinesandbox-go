package wait

import (
	"context"
	"math/rand"
	"time"
)

// Clock is the SDK's view of time. Every retry and poll loop reads the time, sleeps and
// draws backoff jitter through one Clock, so a test can drive a whole call on a virtual
// clock: sleeps return at once and are recorded, and jitter is fixed.
type Clock interface {
	// Now is the current time.
	Now() time.Time
	// Sleep blocks for d, or returns ctx.Err() as soon as ctx is done (see Sleep).
	Sleep(ctx context.Context, d time.Duration) error
	// Jitter is a uniform random number in [0, 1).
	Jitter() float64
}

// System is the real clock: time.Now, a context-cancellable sleep and math/rand jitter.
var System Clock = systemClock{}

type systemClock struct{}

func (systemClock) Now() time.Time                                   { return time.Now() }
func (systemClock) Sleep(ctx context.Context, d time.Duration) error { return Sleep(ctx, d) }
func (systemClock) Jitter() float64                                  { return rand.Float64() }

// Or returns c, or System when c is nil, so a zero-value holder of a Clock is usable.
func Or(c Clock) Clock {
	if c == nil {
		return System
	}
	return c
}
