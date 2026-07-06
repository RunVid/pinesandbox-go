package transport

import (
	"strings"

	"go.pinesandbox.io/computer/internal/base/problem"
)

// Operation renders the canonical "op" string used in every troubleshooting context:
// "<METHOD> <path>" with any query string and fragment stripped. Query values (a filename,
// a cursor, a selector, a file path) must NEVER leak into an error message or a metric-
// adjacent field — so this is the single choke point both the unary and streaming paths
// (and the control-plane / token layers) route through when they stamp Op.
func Operation(method, path string) string {
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	return method + " " + path
}

// TimeoutError is a normalized request timeout: a net timeout, a context deadline, or a
// 408/504 from the gateway. Integrators never see a raw net/url/http error. Host / Op /
// RequestID are the resource-first troubleshooting context (WHICH Computer, WHICH operation,
// and — for a 408/504, which is a real response — the request id); they are rendered ONCE
// via the shared ContextSuffix, so no caller folds context into Msg by hand.
type TimeoutError struct {
	Host      string
	Op        string // "<METHOD> <path>" (query-stripped)
	RequestID string
	Msg       string // the underlying cause / status detail
}

func (e *TimeoutError) Error() string {
	return "pinesandbox: request timed out" + problem.ContextSuffix(e.Host, e.Op, e.RequestID) + ": " + e.Msg
}

// Timeout reports true so callers can also test it via the net.Error-style interface.
func (e *TimeoutError) Timeout() bool { return true }

// ConnectionError is a normalized connection failure (dial refused, DNS failure, TLS
// handshake, unexpected EOF). Host / Op carry the resource-first context (a transport fault
// has no response, so no RequestID); rendered once via the shared ContextSuffix.
type ConnectionError struct {
	Host      string
	Op        string
	RequestID string
	Msg       string
}

func (e *ConnectionError) Error() string {
	return "pinesandbox: connection failed" + problem.ContextSuffix(e.Host, e.Op, e.RequestID) + ": " + e.Msg
}
