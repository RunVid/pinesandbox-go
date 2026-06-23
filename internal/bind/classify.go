package bind

import (
	"net/http"
	"strings"
)

// Class is the retry classification of a bind attempt outcome (bind-decision-table.json).
type Class int

const (
	// Readiness: a fresh pod isn't up yet (plain ingress 404/503, /errors/bind-in-progress,
	// or a transport fault). Retry, DEADLINE-bound, REUSING the same envelope byte-for-byte.
	ClassReadiness Class = iota
	// Race: a pod-identity shift (stale boot id / wrong pod uid / ephem mismatch, or a
	// transient bind-restore-failed). Retry, ATTEMPT-bound, RE-MINTING the envelope.
	ClassRace
	// Terminal: a typed failure — no same-pod retry. Classify also returns the typed error.
	ClassTerminal
)

func (c Class) String() string {
	switch c {
	case ClassReadiness:
		return "readiness"
	case ClassRace:
		return "race"
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

// Race problem types (pod-identity shift → re-mint). ephem-pub-mismatch is defensive
// (coord surfaces an ephem mismatch as hpke-decrypt-failed = terminal).
var raceProblemTypes = map[string]bool{
	"/errors/stale-coord-boot-id": true,
	"/errors/wrong-pod-uid":       true,
	"/errors/ephem-pub-mismatch":  true,
}

// Classify returns the retry class for a bind attempt. For ClassTerminal, err is the
// typed bind error to surface; for Readiness/Race, err is nil (the caller retries per the
// class's policy). Precedence mirrors bind-decision-table.json.
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
	case raceProblemTypes[o.ProblemType]:
		return ClassRace, nil
	case o.ProblemType == "/errors/bind-restore-failed":
		// epoch-conflict variant is terminal (another pod won the lease); a transient
		// >=500 is a race (re-mint); otherwise broker-unreachable.
		switch {
		case indicatesEpochConflict(o.Message):
			return ClassTerminal, &ComputerAlreadyAttachedError{base{o.Status, o.Message, nil}}
		case o.Status >= 500:
			return ClassRace, nil
		default:
			return ClassTerminal, &BrokerUnreachableError{base{o.Status, o.Message, nil}}
		}
	}

	// 6. Terminal problem-type map.
	switch o.ProblemType {
	case "/errors/bind-rejected":
		return ClassTerminal, &BindAuthError{base{o.Status, o.Message, nil}}
	case "/errors/bind-not-configured":
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
