package transport

import "fmt"

// TimeoutError is a normalized request timeout: a net timeout, a context deadline, or a
// 408/504 from the gateway. Integrators never see a raw net/url/http error.
type TimeoutError struct {
	Op  string // "METHOD /path"
	Msg string
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("pinesandbox: %s timed out: %s", e.Op, e.Msg)
}

// Timeout reports true so callers can also test it via the net.Error-style interface.
func (e *TimeoutError) Timeout() bool { return true }

// ConnectionError is a normalized connection failure (dial refused, DNS failure, TLS
// handshake, unexpected EOF).
type ConnectionError struct {
	Op  string
	Msg string
}

func (e *ConnectionError) Error() string {
	return fmt.Sprintf("pinesandbox: %s connection failed: %s", e.Op, e.Msg)
}
