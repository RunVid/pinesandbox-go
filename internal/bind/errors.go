// Package bind classifies a bind/attach attempt outcome into the readiness / race /
// terminal classes of bind-decision-table.json, and defines the typed bind errors a
// terminal outcome surfaces. Domain package (under internal/, not internal/base): the
// root SDK re-exports these error types. Pure classification — no I/O, no crypto.
package bind

import "fmt"

// base carries the common fields of every bind error: the HTTP status (0 for a
// transport/deadline failure) and the underlying cause (for errors.Unwrap).
type base struct {
	Status int
	Msg    string
	Cause  error
}

func (b base) Unwrap() error { return b.Cause }

func errMsg(label string, status int, m string) string {
	switch {
	case status != 0 && m != "":
		return fmt.Sprintf("pinesandbox: %s (%d): %s", label, status, m)
	case status != 0:
		return fmt.Sprintf("pinesandbox: %s (%d)", label, status)
	case m != "":
		return fmt.Sprintf("pinesandbox: %s: %s", label, m)
	default:
		return "pinesandbox: " + label
	}
}

// The typed bind errors — distinct, errors.As-able, mirroring the Ruby pinesandbox
// classes and the bind-decision-table error_class names.

// BindError is a generic bind failure (the 409 / catch-all fallback).
type BindError struct{ base }

func (e *BindError) Error() string { return errMsg("bind failed", e.Status, e.Msg) }

// BindAuthError: bind_token verification failed (401/403, /errors/bind-rejected).
type BindAuthError struct{ base }

func (e *BindAuthError) Error() string { return errMsg("bind rejected", e.Status, e.Msg) }

// ComputerAlreadyAttachedError: another pod holds this Computer's epoch lease.
type ComputerAlreadyAttachedError struct{ base }

func (e *ComputerAlreadyAttachedError) Error() string {
	return errMsg("computer already attached elsewhere", e.Status, e.Msg)
}

// PodPoisonedError: a single-use pod bound elsewhere / tainted — recycle, never replay.
type PodPoisonedError struct{ base }

func (e *PodPoisonedError) Error() string { return errMsg("pod poisoned", e.Status, e.Msg) }

// BrokerUnreachableError: the broker / restore pipeline is unavailable (503 / non-epoch
// bind-restore-failed).
type BrokerUnreachableError struct{ base }

func (e *BrokerUnreachableError) Error() string { return errMsg("broker unreachable", e.Status, e.Msg) }

// BindTimeoutError: the readiness deadline elapsed before the pod became ready (produced
// by the attach loop, not the classifier).
type BindTimeoutError struct{ base }

func (e *BindTimeoutError) Error() string { return errMsg("bind readiness timeout", e.Status, e.Msg) }

// TokenRejectedError: a 401 on a previously-bound ct_/ps_. Produced by the request layer.
// A report, not an instruction: on a live, current sandbox this is binding_auth_lost —
// reconcile; only confirmed sandbox-gone evidence justifies an attach. The SDK never
// implicitly rebinds (a fresh pod invalidates every ps_ and fences the old pod's state).
type TokenRejectedError struct{ base }

func (e *TokenRejectedError) Error() string { return errMsg("token rejected", e.Status, e.Msg) }

// Constructors let the bind orchestrator (internal/binder) produce the errors the
// classifier doesn't (the readiness-deadline timeout and restore-restart fallback)
// without reaching the unexported base.

// NewBindTimeoutError builds a BindTimeoutError (the readiness deadline elapsed).
func NewBindTimeoutError(msg string, cause error) *BindTimeoutError {
	return &BindTimeoutError{base{Msg: msg, Cause: cause}}
}

// NewBindError builds a generic BindError (e.g. restore restarts exhausted).
func NewBindError(status int, msg string, cause error) *BindError {
	return &BindError{base{Status: status, Msg: msg, Cause: cause}}
}

// NewTokenRejectedError builds a TokenRejectedError (a 401 on a previously-bound ct_/ps_).
// cause is wrapped so callers can still errors.As to the underlying *problem.APIError.
func NewTokenRejectedError(status int, msg string, cause error) *TokenRejectedError {
	return &TokenRejectedError{base{Status: status, Msg: msg, Cause: cause}}
}
