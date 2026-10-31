package wait

import (
	"math"
	"time"
)

// The shared stream-reconnect cadence (computer-api.yaml x-pine-retry.client.stream_reconnect),
// for a dropped event feed and a dropped artifact upload or download: 500ms·2^(attempt-1)
// plus up to 200ms of jitter, capped at 5s, and at most ReconnectBudget attempts in a row
// that move nothing forward.
const (
	// ReconnectBudget is how many reconnects in a row may move nothing forward before
	// the caller gives up.
	ReconnectBudget = 5
	reconnectBase   = 500 * time.Millisecond
	reconnectJitter = 200 * time.Millisecond
	reconnectCap    = 5 * time.Second
)

// ReconnectBackoff is the wait before reconnect attempt n (1-based), its jitter drawn from
// clock.
func ReconnectBackoff(clock Clock, attempt int) time.Duration {
	d := time.Duration(float64(reconnectBase)*math.Pow(2, float64(attempt-1))) +
		time.Duration(clock.Jitter()*float64(reconnectJitter))
	return min(d, reconnectCap)
}
