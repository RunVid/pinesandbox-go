// Package problem decodes RFC 9457 problem+json into a typed *APIError and resolves the
// retryable judgment: the 0C wire field when present, falling back to the embedded
// error-taxonomy for older servers that omit it. A generic base primitive
// (internal/base) — Computer-agnostic, must not import a domain package (§3).
//
// The taxonomy is embedded (go:embed) — compiled into the binary, NOT read from disk at
// runtime (the contract README's "no runtime file dependency"). A drift test guards the
// embedded copy against the canonical artifact.
package problem

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

//go:embed error-taxonomy.json
var taxonomyJSON []byte

// taxonomy maps a problem-type slug to its retryable judgment, parsed once from the
// embedded artifact.
var taxonomy = mustLoadTaxonomy()

func mustLoadTaxonomy() map[string]bool {
	var f struct {
		Entries []struct {
			Type      string `json:"type"`
			Retryable bool   `json:"retryable"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(taxonomyJSON, &f); err != nil {
		panic("problem: embedded error-taxonomy.json is invalid: " + err.Error())
	}
	m := make(map[string]bool, len(f.Entries))
	for _, e := range f.Entries {
		m[e.Type] = e.Retryable
	}
	return m
}

// APIError is a typed coordinator / control-plane error (RFC 9457 problem+json). Its
// fields are exported and final after decode; there is deliberately NO Retryable()
// method — Go forbids a field and method of the same name (§7). Callers reach it with
// errors.As(err, &apiErr).
type APIError struct {
	Status      int    // HTTP status
	ProblemType string // RFC-9457 `type`, e.g. "/errors/session-busy"
	// AltProblemTypes lets a SENTINEL match additional server slugs under one
	// errors.Is target — e.g. one "no active turn" sentinel covering both the
	// coordinator's idle-session and mid-request-race 409s. Always empty on a
	// wire error (it carries a single ProblemType); consulted by Is only when
	// this value is the errors.Is target.
	AltProblemTypes []string
	Title           string
	Detail          string
	Reason          string // safe, spec-defined machine reason (never an internal exception)
	// Host + Op are the PRIMARY troubleshooting spine: WHICH Computer (the data host
	// <sandbox>.computer.<zone>) and WHICH operation ("<METHOD> <path>") failed. They are
	// set by the transport / coordinator layer that knows them (problem.Parse is host-
	// agnostic by design), so an integrator catching this in a generic handler sees the
	// resource + op with no extra plumbing. RequestID is the SECONDARY precision handle.
	Host      string // data host — WHICH Computer/endpoint (empty for the sentinels)
	Op        string // "<METHOD> <path>" — WHICH operation (empty for the sentinels)
	RequestID string // body `request_id`, else the X-Request-Id header (0C)
	Retryable bool   // wire `retryable` (0C) when present, else the taxonomy fallback
	// Attach authorization conflict extensions. They identify the winning
	// integrator-side binding to reload/adopt; Portal never returns a ct_.
	CurrentBindingRevision *int64
	CurrentSandboxID       string
}

func (e *APIError) Error() string {
	var msg string
	if e.ProblemType != "" {
		msg = fmt.Sprintf("pinesandbox: %d %s: %s", e.Status, e.ProblemType, e.Detail)
	} else {
		msg = fmt.Sprintf("pinesandbox: %d: %s", e.Status, e.Detail)
	}
	// Resource-first context suffix (host → op → request_id): integrators overwhelmingly
	// log only err.Error(), so the resource + operation that failed — the primary spine —
	// and the request_id precision handle must all land in the message, not only on fields.
	return msg + ContextSuffix(e.Host, e.Op, e.RequestID)
}

// ContextSuffix renders the resource-first troubleshooting context appended to an error
// message: host (WHICH Computer) → op (WHICH operation) → request_id (the single failed
// call). host/op are the PRIMARY spine (durable, held by the integrator, pivots across
// time); request_id is the SECONDARY precision handle. Each part is included only when
// non-empty, in that order; returns "" when all are absent (so a bare error stays clean).
func ContextSuffix(host, op, requestID string) string {
	parts := make([]string, 0, 3)
	if host != "" {
		parts = append(parts, "host="+host)
	}
	if op != "" {
		parts = append(parts, "op="+op)
	}
	if requestID != "" {
		parts = append(parts, "request_id="+requestID)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// Is matches by RFC-9457 problem-type slug, so a caller can branch on a named sentinel
// (a *APIError carrying just the slug) instead of comparing raw status ints:
//
//	if errors.Is(err, pinesandbox.ErrTaskNotReady) { /* poll again */ }
//
// The full wire detail stays reachable via errors.As(err, &apiErr). Stays domain-free: the
// match is purely "same non-empty slug", so the named sentinels live in the SDK facade.
func (e *APIError) Is(target error) bool {
	t, ok := target.(*APIError)
	if !ok || t.ProblemType == "" {
		return false
	}
	if e.ProblemType == t.ProblemType {
		return true
	}
	for _, alt := range t.AltProblemTypes {
		if e.ProblemType == alt {
			return true
		}
	}
	return false
}

// wireProblem is the decoded body. Retryable is a pointer so an absent field (fall back
// to the taxonomy) is distinguishable from an explicit false.
type wireProblem struct {
	Type                   string `json:"type"`
	Title                  string `json:"title"`
	Detail                 string `json:"detail"`
	Status                 int    `json:"status"`
	RequestID              string `json:"request_id"`
	Retryable              *bool  `json:"retryable"`
	Reason                 string `json:"reason"`
	CurrentBindingRevision *int64 `json:"current_binding_revision"`
	CurrentSandboxID       string `json:"current_sandbox_id"`
}

// Parse builds an *APIError from a non-2xx response: status is the HTTP status, body is
// the (problem+json or other) body, headerRequestID is the X-Request-Id header used when
// the body omits request_id (every response carries it — 0C). Retryable is the wire
// judgment when present, else the embedded-taxonomy fallback.
func Parse(status int, body []byte, headerRequestID string) *APIError {
	e := &APIError{Status: status, RequestID: headerRequestID}
	var wp wireProblem
	if json.Unmarshal(body, &wp) == nil {
		e.ProblemType, e.Title, e.Detail, e.Reason = wp.Type, wp.Title, wp.Detail, wp.Reason
		e.CurrentBindingRevision = wp.CurrentBindingRevision
		e.CurrentSandboxID = wp.CurrentSandboxID
		if wp.RequestID != "" {
			e.RequestID = wp.RequestID
		}
		// The body's `status` is intentionally NOT used to override e.Status — the real
		// transport HTTP status is authoritative for control flow (callers branch on
		// ae.Status == 404 etc.). A body whose `status` disagrees (proxy rewrite / forwarded
		// inner error) must not move the SDK's view of what the server actually returned.
		if wp.Retryable != nil { // wire judgment wins
			e.Retryable = *wp.Retryable
			return e
		}
	}
	e.Retryable = RetryableFallback(e.ProblemType, e.Status)
	return e
}

// RetryableFallback is the error-taxonomy judgment for when the wire omits `retryable`
// (older servers). Known slugs use the embedded table; unknown slugs use the documented
// heuristic: 412 → true, 501 → false, ≥500 → true, else false.
func RetryableFallback(slug string, status int) bool {
	if r, ok := taxonomy[slug]; ok {
		return r
	}
	switch {
	case status == http.StatusPreconditionFailed: // 412
		return true
	case status == http.StatusNotImplemented: // 501
		return false
	case status >= 500:
		return true
	default:
		return false
	}
}
