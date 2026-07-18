package bind

import (
	"net/http"
	"strings"
)

// Class is the retry classification of a bind attempt outcome (bind-decision-table.json).
type Class int

const (
	// Readiness: a fresh pod isn't up yet (plain ingress 404/503, /errors/bind-in-progress,
	// a transient restore failure, or a transport fault). Retry, DEADLINE-bound, REUSING
	// the same envelope byte-for-byte.
	ClassReadiness Class = iota
	// Terminal: a typed failure — no same-pod retry. Classify also returns the typed error.
	ClassTerminal
)

func (c Class) String() string {
	switch c {
	case ClassReadiness:
		return "readiness"
	default:
		return "terminal"
	}
}

// Outcome is one bind attempt's result. TransportFault is set when there was no HTTP
// response (timeout / connection-refused / TLS). ProblemType is the RFC-9457 `type` ("" =
// a plain ingress response with no problem body). Message is the detail (the
// bind-restore-failed epoch-conflict discriminator).
type Outcome struct {
	TransportFault bool
	Status         int
	ProblemType    string
	Message        string
}

// Classify returns the retry class for a bind attempt. For ClassTerminal, err is the
// typed bind error to surface; for Readiness, err is nil (the caller retries the same
// committed authorization per the class's policy). Precedence mirrors
// bind-decision-table.json. bind-restore-no-challenge is handled by the binder because it
// restarts the restore transaction without re-minting the attach authorization.
func Classify(o Outcome) (Class, error) {
	// 1. Transport fault → readiness (the pod/ingress isn't answering yet).
	if o.TransportFault {
		return ClassReadiness, nil
	}

	// 2. No problem body: a PLAIN 404/503 is the ingress readiness signal; any other bare
	//    status falls through to the status fallback.
	if o.ProblemType == "" {
		if o.Status == http.StatusNotFound || o.Status == http.StatusServiceUnavailable {
			return ClassReadiness, nil
		}
		return ClassTerminal, o.statusFallback()
	}

	// 3–5. Problem-type-driven.
	switch {
	case o.ProblemType == "/errors/bind-in-progress":
		return ClassReadiness, nil
	case o.ProblemType == "/errors/bind-restore-failed":
		// The epoch-conflict variant is terminal (another pod won the lease). A
		// transient >=500 retries the same envelope: Portal has already committed the
		// single authorization for this sandbox, so re-minting would be invalid.
		switch {
		case indicatesEpochConflict(o.Message):
			return ClassTerminal, &ComputerAlreadyAttachedError{base{o.Status, o.Message, nil}}
		case o.Status >= 500:
			return ClassReadiness, nil
		default:
			return ClassTerminal, &BrokerUnreachableError{base{o.Status, o.Message, nil}}
		}
	}

	// 6. Terminal problem-type map.
	switch o.ProblemType {
	case "/errors/bind-rejected":
		return ClassTerminal, &BindAuthError{base{o.Status, o.Message, nil}}
	case "/errors/bind-not-configured", "/errors/bind-lease-unenforceable":
		// Both are structural pod/deployment misconfigurations a bind retry
		// cannot fix — surfaced as themselves, never as a broker outage.
		return ClassTerminal, &BindError{base{o.Status, o.Message, nil}}
	case "/errors/stale-coord-boot-id", "/errors/wrong-pod-uid", "/errors/ephem-pub-mismatch":
		// The committed receipt targets one immutable pod identity. A changed identity
		// requires a fresh sandbox; it must never trigger another Portal mint for this
		// single-use sandbox id.
		return ClassTerminal, &BindError{base{o.Status, o.Message, nil}}
	case "/errors/already-bound-different-cid", "/errors/pod-tainted":
		return ClassTerminal, &PodPoisonedError{base{o.Status, o.Message, nil}}
	}

	// 7. A problem type we don't specifically map → status fallback.
	return ClassTerminal, o.statusFallback()
}

// statusFallback maps an unmatched status to a typed terminal error.
func (o Outcome) statusFallback() error {
	b := base{o.Status, o.Message, nil}
	switch {
	case o.Status == http.StatusUnauthorized || o.Status == http.StatusForbidden:
		return &BindAuthError{b}
	case o.Status == http.StatusConflict:
		return &BindError{b}
	case o.Status == http.StatusServiceUnavailable:
		return &BrokerUnreachableError{b}
	default:
		return &BindError{b}
	}
}

func indicatesEpochConflict(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "epoch")
}
