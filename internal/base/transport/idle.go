package transport

import (
	"io"
	"sync/atomic"
	"time"
)

// WatchIdle closes body when one read waits d for data; the read that the close ends,
// and every later one, returns idleErr. The timer runs only while a read waits, so a
// consumer that takes its time between reads never counts as a silent peer.
func WatchIdle(body io.ReadCloser, d time.Duration, idleErr error) io.ReadCloser {
	b := &idleBody{ReadCloser: body, d: d, idleErr: idleErr}
	b.timer = time.AfterFunc(d, func() {
		b.idle.Store(true)
		_ = body.Close()
	})
	b.timer.Stop()
	return b
}

type idleBody struct {
	io.ReadCloser
	d       time.Duration
	timer   *time.Timer
	idle    atomic.Bool
	idleErr error
}

func (b *idleBody) Read(p []byte) (int, error) {
	if b.idle.Load() {
		return 0, b.idleErr
	}
	b.timer.Reset(b.d)
	n, err := b.ReadCloser.Read(p)
	b.timer.Stop()
	if b.idle.Load() {
		return n, b.idleErr
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	return b.ReadCloser.Close()
}
