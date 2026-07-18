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

func TestResourceSentinelsRemainDistinct(t *testing.T) {
	missingSession := &APIError{Status: 404, ProblemType: "/errors/session-not-found"}
	if !errors.Is(missingSession, ErrSessionNotFound) {
		t.Error("session-not-found must match ErrSessionNotFound")
	}
	if errors.Is(&APIError{Status: 404, ProblemType: "/errors/sandbox-not-found"}, ErrSessionNotFound) {
		t.Error("sandbox-not-found must not match ErrSessionNotFound")
	}
	if !errors.Is(&APIError{Status: 409, ProblemType: "/errors/control-not-held"}, ErrControlNotHeld) {
		t.Error("control-not-held must match ErrControlNotHeld")
	}
}

func TestIsRetryableUsesOnlyServerAPIJudgment(t *testing.T) {
	if !IsRetryable(&APIError{Status: 503, ProblemType: "/errors/sandbox-not-ready", Retryable: true}) {
		t.Error("retryable APIError must be retryable")
	}
	if IsRetryable(&APIError{Status: 503, ProblemType: "/errors/bind-not-configured", Retryable: false}) {
		t.Error("explicitly terminal 503 must not be retryable")
	}
	if IsRetryable(errors.New("connection reset")) {
		t.Error("opaque/transport errors must not be declared replay-safe")
	}
}

// TestNoTaskSentinels_MatchTheWirePairsTheCoordinatorEmits: the two idle-session cases the
// coordinator actually produces map to two DISTINCT sentinels. The historical single
// sentinel carried {404, /errors/no-active-task} — a pair emitted by neither path — so
// read-path checks silently never matched and consumers mislabeled valid idle sessions as
// stale (duplicate CreateSession → 409 → 500, the 2026-07-14 incident branch).
func TestNoTaskSentinels_MatchTheWirePairsTheCoordinatorEmits(t *testing.T) {
	// Task READ on a never-ran session: 404 /errors/task-not-found.
	read := &APIError{Status: 404, ProblemType: "/errors/task-not-found", Detail: "no task"}
	if !errors.Is(read, ErrTaskNotFound) {
		t.Error("errors.Is(read 404 task-not-found, ErrTaskNotFound) = false, want true")
	}
	if errors.Is(read, ErrNoActiveTurn) {
		t.Error("a task-not-found read must NOT match ErrNoActiveTurn")
	}

	// Task MUTATION with no turn in flight: 409 /errors/no-active-task.
	mutation := &APIError{Status: 409, ProblemType: "/errors/no-active-task", Detail: "nothing to steer"}
	if !errors.Is(mutation, ErrNoActiveTurn) {
		t.Error("errors.Is(mutation 409 no-active-task, ErrNoActiveTurn) = false, want true")
	}
	if errors.Is(mutation, ErrTaskNotFound) {
		t.Error("a no-active-task mutation must NOT match ErrTaskNotFound")
	}

	// Task MUTATION that RACES a turn ending mid-request: the coordinator's
	// writeTaskControlResult emits 409 /errors/no-active-turn (taskbroker.ErrNoActiveTurn).
	// The same sentinel must match it — same actionable outcome (start a turn).
	race := &APIError{Status: 409, ProblemType: "/errors/no-active-turn", Detail: "turn ended"}
	if !errors.Is(race, ErrNoActiveTurn) {
		t.Error("errors.Is(mutation 409 no-active-turn race, ErrNoActiveTurn) = false, want true")
	}

	// The deprecated sentinel keeps matching exactly what it always matched in practice
	// (the mutation slug) — no silent behavior change for existing consumers.
	if !errors.Is(mutation, ErrNoActiveTask) {
		t.Error("deprecated ErrNoActiveTask must still match the mutation pair")
	}
	if errors.Is(read, ErrNoActiveTask) {
		t.Error("deprecated ErrNoActiveTask must not silently start matching reads — consumers must adopt ErrTaskNotFound deliberately")
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
	for _, s := range []*APIError{ErrControlNotHeld, ErrSessionNotFound, ErrTaskNotReady, ErrSessionBusy, ErrSessionLimit, ErrTaskNotFound, ErrNoActiveTurn, ErrNoActiveTask, ErrActionNotImplemented} {
		for _, slug := range append([]string{s.ProblemType}, s.AltProblemTypes...) {
			if !known[slug] {
				t.Errorf("sentinel slug %q is not in error-taxonomy.json (typo or renamed server slug)", slug)
			}
		}
	}
}
