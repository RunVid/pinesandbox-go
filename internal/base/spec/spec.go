// Package spec is the generic spec-version negotiation mechanism: inject a
// supported-major request header on every call and fail fast when the echoed response
// header's major differs. It is Computer-agnostic — the domain supplies the header names
// and the supported major (Computer wires in "Computer-Spec-Version" / SpecVersion), so
// the check lives in ONE place used by BOTH the control-plane and coordinator clients,
// mirroring the Ruby SpecVersionMiddleware. An ABSENT echo is tolerated (not every hop
// echoes); only a present-and-different major is an error.
package spec

import (
	"fmt"
	"strings"
)

// Negotiator carries the version contract for a client. The zero value is unusable;
// construct it with the domain's header names and supported major.
type Negotiator struct {
	RequestHeader  string // e.g. "Computer-Spec-Version"
	ResponseHeader string // e.g. "X-Computer-Spec-Version"
	SupportedMajor int    // the major this SDK pins
}

// RequestValue is the value to set for RequestHeader (the supported major).
func (n Negotiator) RequestValue() string { return fmt.Sprintf("%d", n.SupportedMajor) }

// Check validates an echoed response-header value. An absent or blank echo is tolerated
// (returns nil); a present major that differs from SupportedMajor returns *MismatchError.
// The "major" is the leading integer of the echoed value, matching Ruby's String#to_i
// (so "1", "1.4", and "1-rc" all read as major 1; non-numeric reads as 0 → mismatch).
func (n Negotiator) Check(echoed string) error {
	trimmed := strings.TrimSpace(echoed)
	if trimmed == "" {
		return nil
	}
	if leadingInt(trimmed) == n.SupportedMajor {
		return nil
	}
	return &MismatchError{Supported: n.SupportedMajor, Served: trimmed}
}

// MismatchError is raised when the gateway serves a different spec-version major than the
// SDK pins — the contracts are incompatible and the caller must upgrade one side.
type MismatchError struct {
	Supported int
	Served    string
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("pinesandbox: Computer-API spec-version mismatch: SDK pins v%d, gateway served %q", e.Supported, e.Served)
}

// leadingInt parses the leading signed integer of s, stopping at the first non-digit —
// equivalent to Ruby's String#to_i on an already-trimmed string ("" and non-numeric → 0).
func leadingInt(s string) int {
	i, n := 0, len(s)
	neg := false
	if i < n && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	val, sawDigit := 0, false
	for ; i < n && s[i] >= '0' && s[i] <= '9'; i++ {
		val = val*10 + int(s[i]-'0')
		sawDigit = true
	}
	if !sawDigit {
		return 0
	}
	if neg {
		return -val
	}
	return val
}
