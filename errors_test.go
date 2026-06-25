package pinesandbox

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestControlSentinels_ErrorsIsBySlug: the agent-lane sentinels match the live wire error by
// problem-type slug, while errors.As still reaches the full detail.
func TestControlSentinels_ErrorsIsBySlug(t *testing.T) {
	inFlight := &APIError{Status: 409, ProblemType: "/errors/task-not-ready", Detail: "result not ready", RequestID: "req-1"}

	if !errors.Is(inFlight, ErrTaskNotReady) {
		t.Error("errors.Is(task-not-ready, ErrTaskNotReady) = false, want true")
	}
	if errors.Is(inFlight, ErrSessionBusy) {
		t.Error("a task-not-ready error must NOT match ErrSessionBusy")
	}

	// The session cap (CreateSession at the concurrent limit) matches ErrSessionLimit,
	// not the unrelated ErrSessionBusy (both are 409).
	atCap := &APIError{Status: 409, ProblemType: "/errors/session-limit"}
	if !errors.Is(atCap, ErrSessionLimit) {
		t.Error("errors.Is(session-limit, ErrSessionLimit) = false, want true")
	}
	if errors.Is(atCap, ErrSessionBusy) {
		t.Error("a session-limit error must NOT match ErrSessionBusy")
	}

	// errors.As still reaches the full wire detail (the sentinel is not a lossy wrapper).
	var ae *APIError
	if !errors.As(inFlight, &ae) || ae.Status != 409 || ae.RequestID != "req-1" {
		t.Errorf("errors.As lost the wire detail: %+v", ae)
	}

	// A slug-less error (a non-problem+json failure) matches no sentinel.
	if errors.Is(&APIError{Status: 500}, ErrActionNotImplemented) {
		t.Error("a slug-less error must not match a sentinel")
	}
}

// TestControlSentinels_SlugsInTaxonomy: every sentinel's slug is a REAL coordinator problem
// type (pinned in error-taxonomy.json) — a typo or a renamed slug fails here, keeping the
// SDK's named errors in lockstep with the server's taxonomy.
// Skips on the mirror (the contract artifact isn't published with the SDK).
func TestControlSentinels_SlugsInTaxonomy(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "contract", "error-taxonomy.json"))
	if err != nil {
		t.Skipf("error-taxonomy artifact not present (mirror build): %v", err)
	}
	var f struct {
		Entries []struct {
			Type string `json:"type"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse taxonomy: %v", err)
	}
	known := make(map[string]bool, len(f.Entries))
	for _, e := range f.Entries {
		known[e.Type] = true
	}
	for _, s := range []*APIError{ErrTaskNotReady, ErrSessionBusy, ErrSessionLimit, ErrNoActiveTask, ErrActionNotImplemented} {
		if !known[s.ProblemType] {
			t.Errorf("sentinel slug %q is not in error-taxonomy.json (typo or renamed server slug)", s.ProblemType)
		}
	}
}
