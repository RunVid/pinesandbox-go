package bind

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// decisionTable mirrors the parts of bind-decision-table.json this test drives.
type decisionTable struct {
	ReadinessRetry struct {
		Signals struct {
			HTTPPlain []struct {
				Status      int     `json:"status"`
				ProblemType *string `json:"problem_type"`
			} `json:"http_plain"`
			ProblemTypes []string `json:"problem_types"`
			Transport    []string `json:"transport"`
		} `json:"signals"`
	} `json:"readiness_retry"`
	RaceRetry struct {
		ProblemTypes []string `json:"problem_types"`
		Conditional  []struct {
			ProblemType string `json:"problem_type"`
			When        string `json:"when"`
		} `json:"conditional"`
	} `json:"race_retry"`
	Terminal []struct {
		ProblemType string `json:"problem_type"`
		ErrorClass  string `json:"error_class"`
		When        string `json:"when"`
	} `json:"terminal"`
	StatusFallback []struct {
		Statuses   json.RawMessage `json:"statuses"`
		ErrorClass string          `json:"error_class"`
	} `json:"status_fallback"`
}

func loadDecisionTable(t *testing.T) decisionTable {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "bind-decision-table.json"))
	if err != nil {
		t.Fatalf("read decision table: %v", err)
	}
	var dt decisionTable
	if err := json.Unmarshal(b, &dt); err != nil {
		t.Fatalf("parse decision table: %v", err)
	}
	return dt
}

// matchesErrorClass reports whether err is the Go type named by the artifact's
// error_class string (errors.As against the concrete typed bind error).
func matchesErrorClass(err error, className string) bool {
	switch className {
	case "BindError":
		var e *BindError
		return errors.As(err, &e)
	case "BindAuthError":
		var e *BindAuthError
		return errors.As(err, &e)
	case "ComputerAlreadyAttachedError":
		var e *ComputerAlreadyAttachedError
		return errors.As(err, &e)
	case "PodPoisonedError":
		var e *PodPoisonedError
		return errors.As(err, &e)
	case "BrokerUnreachableError":
		var e *BrokerUnreachableError
		return errors.As(err, &e)
	default:
		return false
	}
}

// TestClassify_DecisionTable drives every documented signal in the contract artifact
// through Classify and asserts the resulting class (and, for terminal, the typed error)
// matches. A divergence between the classifier and the language-neutral contract — a
// problem type moved between classes, an error_class renamed — fails here.
func TestClassify_DecisionTable(t *testing.T) {
	dt := loadDecisionTable(t)

	// Readiness — plain ingress statuses (problem_type null).
	for _, s := range dt.ReadinessRetry.Signals.HTTPPlain {
		if s.ProblemType != nil {
			t.Fatalf("http_plain entry %d unexpectedly has a problem_type", s.Status)
		}
		if c, _ := Classify(Outcome{Status: s.Status}); c != ClassReadiness {
			t.Errorf("plain status %d → %s, want readiness", s.Status, c)
		}
	}
	// Readiness — problem types (a representative status; class keys on the type).
	for _, pt := range dt.ReadinessRetry.Signals.ProblemTypes {
		if c, _ := Classify(Outcome{Status: 409, ProblemType: pt}); c != ClassReadiness {
			t.Errorf("readiness problem_type %s → %s, want readiness", pt, c)
		}
	}
	// Readiness — transport faults (any fault, regardless of the named kind).
	for _, kind := range dt.ReadinessRetry.Signals.Transport {
		if c, _ := Classify(Outcome{TransportFault: true}); c != ClassReadiness {
			t.Errorf("transport fault %q → %s, want readiness", kind, c)
		}
	}

	// Race — pod-identity-shift problem types.
	for _, pt := range dt.RaceRetry.ProblemTypes {
		if c, _ := Classify(Outcome{Status: 409, ProblemType: pt}); c != ClassRace {
			t.Errorf("race problem_type %s → %s, want race", pt, c)
		}
	}
	// Race — conditional (bind-restore-failed, status>=500, no epoch conflict).
	for _, cond := range dt.RaceRetry.Conditional {
		out := Outcome{Status: 500, ProblemType: cond.ProblemType, Message: "broker pipeline failure"}
		if c, _ := Classify(out); c != ClassRace {
			t.Errorf("race conditional %s (status 500, non-epoch) → %s, want race", cond.ProblemType, c)
		}
	}

	// Terminal — each typed entry. bind-restore-failed has two conditional variants
	// keyed by `when` (epoch conflict → ComputerAlreadyAttached; otherwise <500 →
	// BrokerUnreachable); shape the Outcome accordingly.
	for _, term := range dt.Terminal {
		out := Outcome{ProblemType: term.ProblemType, Status: 409}
		if term.ProblemType == "/errors/bind-restore-failed" {
			if indicatesEpochConflict(term.When) {
				out.Status, out.Message = 500, "attach epoch conflict"
			} else {
				out.Status = 409 // <500, non-epoch
			}
		}
		c, err := Classify(out)
		if c != ClassTerminal {
			t.Errorf("terminal %s (when=%q) → %s, want terminal", term.ProblemType, term.When, c)
			continue
		}
		if !matchesErrorClass(err, term.ErrorClass) {
			t.Errorf("terminal %s (when=%q) → %T, want %s", term.ProblemType, term.When, err, term.ErrorClass)
		}
	}

	// Status fallback — a status carrying a problem type we don't specifically map falls
	// through to the by-status default. ("other" is exercised via a representative 500.)
	for _, fb := range dt.StatusFallback {
		for _, st := range fallbackStatuses(t, fb.Statuses) {
			c, err := Classify(Outcome{Status: st, ProblemType: "/errors/zzz-unmapped"})
			if c != ClassTerminal {
				t.Errorf("status fallback %d → %s, want terminal", st, c)
				continue
			}
			if !matchesErrorClass(err, fb.ErrorClass) {
				t.Errorf("status fallback %d → %T, want %s", st, err, fb.ErrorClass)
			}
		}
	}
}

// fallbackStatuses resolves a status_fallback "statuses" field, which is either a JSON
// list of ints or the string "other" (represented by a non-special 500).
func fallbackStatuses(t *testing.T, raw json.RawMessage) []int {
	t.Helper()
	var list []int
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s == "other" {
		return []int{500}
	}
	t.Fatalf("unrecognized status_fallback statuses: %s", raw)
	return nil
}

// TestClassify_PlainServiceUnavailableIsReadiness pins the precedence subtlety the table
// encodes: a PLAIN 503 (no problem body) is an ingress readiness signal, NOT the
// status-fallback BrokerUnreachableError that a 503-with-a-problem-type yields.
func TestClassify_PlainServiceUnavailableIsReadiness(t *testing.T) {
	if c, _ := Classify(Outcome{Status: 503}); c != ClassReadiness {
		t.Errorf("plain 503 → %s, want readiness", c)
	}
	c, err := Classify(Outcome{Status: 503, ProblemType: "/errors/zzz-unmapped"})
	if c != ClassTerminal {
		t.Fatalf("503 with unmapped problem_type → %s, want terminal", c)
	}
	var bu *BrokerUnreachableError
	if !errors.As(err, &bu) {
		t.Errorf("503 with unmapped problem_type → %T, want *BrokerUnreachableError", err)
	}
}

// TestClassify_BindRestoreFailedBranches exercises all three bind-restore-failed paths
// explicitly (the most error-prone classifier branch).
func TestClassify_BindRestoreFailedBranches(t *testing.T) {
	const pt = "/errors/bind-restore-failed"

	// epoch conflict → terminal ComputerAlreadyAttachedError (even at >=500).
	if c, err := Classify(Outcome{Status: 503, ProblemType: pt, Message: "lease held by another epoch"}); c != ClassTerminal {
		t.Errorf("epoch-conflict bind-restore-failed → %s, want terminal", c)
	} else {
		var e *ComputerAlreadyAttachedError
		if !errors.As(err, &e) {
			t.Errorf("epoch-conflict bind-restore-failed → %T, want *ComputerAlreadyAttachedError", err)
		}
	}
	// transient >=500, non-epoch → race (re-mint).
	if c, _ := Classify(Outcome{Status: 502, ProblemType: pt, Message: "broker timeout"}); c != ClassRace {
		t.Errorf("transient bind-restore-failed → %s, want race", c)
	}
	// <500, non-epoch → terminal BrokerUnreachableError.
	if c, err := Classify(Outcome{Status: 409, ProblemType: pt, Message: "restore pipeline declined"}); c != ClassTerminal {
		t.Errorf("<500 bind-restore-failed → %s, want terminal", c)
	} else {
		var e *BrokerUnreachableError
		if !errors.As(err, &e) {
			t.Errorf("<500 bind-restore-failed → %T, want *BrokerUnreachableError", err)
		}
	}
}

// TestDecisionTableMatchesCanonical guards the testdata copy against the canonical
// contract artifact (skips on the mirror where the canonical is absent — §9.1).
func TestDecisionTableMatchesCanonical(t *testing.T) {
	canonical := filepath.Join("..", "..", "..", "contract", "bind-decision-table.json")
	want, err := os.ReadFile(canonical)
	if err != nil {
		t.Skipf("canonical artifact not present (mirror build): %v", err)
	}
	got, err := os.ReadFile(filepath.Join("testdata", "bind-decision-table.json"))
	if err != nil {
		t.Fatalf("read testdata copy: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("testdata bind-decision-table.json drifted from %s — re-copy the canonical", canonical)
	}
}

// TestBindErrors_UnwrapAndStatus checks the typed errors carry their cause (errors.Is via
// Unwrap) and status, so callers can both type-switch and inspect.
func TestBindErrors_UnwrapAndStatus(t *testing.T) {
	cause := errors.New("root cause")
	e := &BindAuthError{base{Status: 401, Msg: "bad token", Cause: cause}}
	if !errors.Is(e, cause) {
		t.Error("BindAuthError does not unwrap to its cause")
	}
	if e.Status != 401 {
		t.Errorf("Status = %d, want 401", e.Status)
	}
	if e.Error() == "" {
		t.Error("empty Error() string")
	}
}
