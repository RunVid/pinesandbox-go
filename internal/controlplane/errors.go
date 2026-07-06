// Package controlplane is the project-scoped control plane client: create / inspect /
// destroy / pause / resume a Computer's pod at https://api.<zone>/sandboxes, authenticated
// with a project JWS (Authorization: Bearer) from a TokenSource. Pine-owned — NOT the base
// gem's lifecycle adapter (which bakes in the wrong API key + a /v1 prefix). Control-plane
// errors are {code,message} (NOT RFC-9457), so this layer maps status → typed error using
// the raw body. Domain package (uses internal/base/{transport,spec}); the root SDK
// re-exports the error types.
package controlplane

import (
	"encoding/json"
	"fmt"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/transport"
)

// cpBase carries the common fields of every control-plane error. Host / Op / RequestID are
// the resource-first troubleshooting context (WHICH host, WHICH operation — the sandbox id
// rides the path, e.g. `GET /sandboxes/<id>` — and the gateway's X-Request-Id); they render
// once via the shared ContextSuffix, matching the coordinator/token error shape.
type cpBase struct {
	Status    int
	Msg       string
	Body      string // the raw response body, for support/debugging
	Host      string
	Op        string // "<METHOD> <path>" (query-stripped)
	RequestID string
	Cause     error
}

func (b cpBase) Unwrap() error { return b.Cause }

func cpErr(b cpBase) string {
	return fmt.Sprintf("pinesandbox: control plane %d: %s", b.Status, b.Msg) +
		problem.ContextSuffix(b.Host, b.Op, b.RequestID)
}

// ControlPlaneError is the generic / unmapped control-plane failure (Ruby ApiError).
type ControlPlaneError struct{ cpBase }

func (e *ControlPlaneError) Error() string { return cpErr(e.cpBase) }

// BadRequestError: 400 — malformed create/lifecycle request.
type BadRequestError struct{ cpBase }

func (e *BadRequestError) Error() string { return cpErr(e.cpBase) }

// UnauthorizedError: 401 — the project JWS was rejected even after a forced refresh.
type UnauthorizedError struct{ cpBase }

func (e *UnauthorizedError) Error() string { return cpErr(e.cpBase) }

// ForbiddenError: 403 — the project may not perform this lifecycle action.
type ForbiddenError struct{ cpBase }

func (e *ForbiddenError) Error() string { return cpErr(e.cpBase) }

// NotFoundError: 404 — no such sandbox.
type NotFoundError struct{ cpBase }

func (e *NotFoundError) Error() string { return cpErr(e.cpBase) }

// ConflictError: 409 — e.g. pausing an already-paused sandbox.
type ConflictError struct{ cpBase }

func (e *ConflictError) Error() string { return cpErr(e.cpBase) }

// UnprocessableEntityError: 422 — semantically invalid create payload.
type UnprocessableEntityError struct{ cpBase }

func (e *UnprocessableEntityError) Error() string { return cpErr(e.cpBase) }

// ServerError: 5xx — control-plane internal failure.
type ServerError struct{ cpBase }

func (e *ServerError) Error() string { return cpErr(e.cpBase) }

// statusError maps a non-OK control-plane response to a typed error, mirroring the Ruby
// raise_for_status!. method/path/host/the X-Request-Id header stamp the resource spine (WHICH
// operation on WHICH host, plus the precision handle) so a create/get/destroy/pause/resume
// failure is self-describing in a generic handler.
func statusError(method, path, host string, resp *transport.Response) error {
	b := cpBase{
		Status:    resp.Status,
		Msg:       statusMessage(resp.Status, resp.Body),
		Body:      string(resp.Body),
		Host:      host,
		Op:        transport.Operation(method, path),
		RequestID: resp.Headers.Get("X-Request-Id"),
	}
	status := resp.Status
	switch {
	case status == 400:
		return &BadRequestError{b}
	case status == 401:
		return &UnauthorizedError{b}
	case status == 403:
		return &ForbiddenError{b}
	case status == 404:
		return &NotFoundError{b}
	case status == 409:
		return &ConflictError{b}
	case status == 422:
		return &UnprocessableEntityError{b}
	case status >= 500 && status <= 599:
		return &ServerError{b}
	default:
		return &ControlPlaneError{b}
	}
}

// statusMessage surfaces whichever message field is present: control-plane errors are
// {code,message}; a bundled bind/coord error would be RFC-9457 {title,detail}.
func statusMessage(status int, body []byte) string {
	var m struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Title   string `json:"title"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &m) == nil {
		for _, s := range []string{m.Message, m.Detail, m.Title, m.Error} {
			if s != "" {
				return s
			}
		}
	}
	return fmt.Sprintf("control plane error (HTTP %d)", status)
}
