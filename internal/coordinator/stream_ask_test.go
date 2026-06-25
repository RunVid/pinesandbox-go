package coordinator

import (
	"encoding/json"
	"testing"
)

// TestAgentEvent_Ask pins the typed needs_input accessor against the coord's real
// askPayload shape (request_id guaranteed + the pine_ask tool args question/context/
// options flattened in — pkg/resident/executor.go askPayload).
func TestAgentEvent_Ask(t *testing.T) {
	ev := &AgentEvent{
		Type:    "needs_input",
		Reason:  "needs_input",
		TurnID:  "turn_1",
		Payload: json.RawMessage(`{"request_id":"call_42","question":"Which seat?","context":"booking flow","options":["window","aisle"]}`),
	}
	ask, ok := ev.Ask()
	if !ok {
		t.Fatal("Ask() ok=false for a needs_input event")
	}
	if ask.RequestID != "call_42" {
		t.Errorf("RequestID = %q, want call_42", ask.RequestID)
	}
	if ask.Question != "Which seat?" {
		t.Errorf("Question = %q, want 'Which seat?'", ask.Question)
	}
	if ask.Context != "booking flow" {
		t.Errorf("Context = %q, want 'booking flow'", ask.Context)
	}
	if len(ask.Options) != 2 || ask.Options[0] != "window" || ask.Options[1] != "aisle" {
		t.Errorf("Options = %v, want [window aisle]", ask.Options)
	}

	// A minimal ask (only the guaranteed request_id) still parses.
	if a, ok := (&AgentEvent{Type: "needs_input", Payload: json.RawMessage(`{"request_id":"call_1"}`)}).Ask(); !ok ||
		a.RequestID != "call_1" || a.Question != "" || len(a.Options) != 0 {
		t.Errorf("minimal ask: ok=%v ask=%+v", ok, a)
	}

	// Not an ask: a non-needs_input event, a malformed payload (fail closed → Raw
	// fallback), and a nil receiver (no panic) all return ok=false.
	if _, ok := (&AgentEvent{Type: "status"}).Ask(); ok {
		t.Error("Ask() ok=true for a status event")
	}
	if _, ok := (&AgentEvent{Type: "needs_input", Payload: json.RawMessage(`not json`)}).Ask(); ok {
		t.Error("Ask() ok=true for a malformed payload")
	}
	var nilEv *AgentEvent
	if _, ok := nilEv.Ask(); ok {
		t.Error("nil event Ask() ok=true")
	}
}
