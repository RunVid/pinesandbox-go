package coordinator

import (
	"encoding/json"
	"testing"
	"time"
)

// TestControlPatch_ToWire pins the PATCH body: relative deadline beats absolute and
// serializes to "+<seconds>s"; unset (nil) fields are omitted.
func TestControlPatch_ToWire(t *testing.T) {
	human := "human"
	paused := true
	abs := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	rel := 15 * time.Minute

	w := ControlPatch{Controller: &human, IdlePaused: &paused, IdleDeadline: &abs, IdleDeadlineIn: &rel, ActorType: "user_click"}.toWire()
	if w["controller"] != "human" || w["idle_paused"] != true || w["actor_type"] != "user_click" {
		t.Errorf("toWire = %v", w)
	}
	if w["idle_deadline"] != "+900s" {
		t.Errorf("relative idle_deadline = %v, want +900s", w["idle_deadline"])
	}

	if w2 := (ControlPatch{IdleDeadline: &abs, ActorType: "user_click"}).toWire(); w2["idle_deadline"] != "2026-01-02T15:04:05Z" {
		t.Errorf("absolute idle_deadline = %v", w2["idle_deadline"])
	}

	w3 := ControlPatch{ActorType: "user_click"}.toWire()
	if _, ok := w3["controller"]; ok {
		t.Error("unset controller must be omitted")
	}
	if _, ok := w3["idle_deadline"]; ok {
		t.Error("unset idle_deadline must be omitted")
	}

	// A non-positive relative deadline must NOT emit a malformed "+-Ns" / "+0s" —
	// it's ignored (the coord would 400 the malformed value).
	neg := -5 * time.Minute
	if w4 := (ControlPatch{IdleDeadlineIn: &neg, ActorType: "user_click"}).toWire(); w4["idle_deadline"] != nil {
		t.Errorf("negative IdleDeadlineIn must be omitted, got %v", w4["idle_deadline"])
	}
}

// TestParseHandoff_LenientTimestamps: a malformed timestamp zeroes that field but
// does NOT fail the parse — one drifted handoff must not break a whole list fetch
// (the raw body stays in .Raw; IsZero flags the issue).
func TestParseHandoff_LenientTimestamps(t *testing.T) {
	h, err := parseHandoff(json.RawMessage(`{"handoff_id":"s:1","started_at":"not-a-time","ended_at":"2026-01-02T15:05:05Z"}`))
	if err != nil {
		t.Fatalf("parseHandoff errored on a bad timestamp (should tolerate): %v", err)
	}
	if !h.StartedAt.IsZero() || h.EndedAt.Year() != 2026 || h.HandoffID != "s:1" {
		t.Errorf("parseHandoff = %+v", h)
	}
	h2, err := parseHandoff(json.RawMessage(`{"handoff_id":"s:1","started_at":"2026-01-02T15:04:05Z","ended_at":"2026-01-02T15:05:05Z"}`))
	if err != nil || h2.StartedAt.Year() != 2026 {
		t.Errorf("parseHandoff = %+v, %v", h2, err)
	}
}
