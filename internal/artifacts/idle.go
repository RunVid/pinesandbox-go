package artifacts

import (
	"io"
	"sync/atomic"
	"time"
)

// transferIdleTimeout ends a transfer that moves no bytes for this long, so a stalled
// connection is resumed, as in the other SDKs, instead of hanging until the operating
// system gives up on it. A download's body is watched by transport.WatchIdle, an upload's
// by sendWatchdog. Waiting for a response to start is the transport's header timeout's.
var transferIdleTimeout = 60 * time.Second

// sendWatchdog calls stop when the transport takes no bytes of an upload's body for
// transferIdleTimeout.
type sendWatchdog struct {
	timer *time.Timer
	fired atomic.Bool
}

func newSendWatchdog(stop func()) *sendWatchdog {
	w := &sendWatchdog{}
	w.timer = time.AfterFunc(transferIdleTimeout, func() {
		w.fired.Store(true)
		stop()
	})
	w.timer.Stop()
	return w
}

// watch returns body read through the watchdog. It is armed between the transport's
// reads, while it sends what it read, and disarmed once the body is all read.
func (w *sendWatchdog) watch(body io.Reader) io.Reader {
	return &watchedSource{r: body, w: w}
}

func (w *sendWatchdog) stop() { w.timer.Stop() }

type watchedSource struct {
	r io.Reader
	w *sendWatchdog
}

func (s *watchedSource) Read(p []byte) (int, error) {
	s.w.timer.Stop()
	n, err := s.r.Read(p)
	if err == nil {
		s.w.timer.Reset(transferIdleTimeout)
	}
	return n, err
}
