package coordinator

import (
	"encoding/json"
	"testing"
)

func TestAgentEventControlChange(t *testing.T) {
	event := &AgentEvent{
		Type:       "controller_changed",
		Source:     "control",
		Controller: "human",
		ModeEpoch:  7,
		Payload: json.RawMessage(`{
			"computer_epoch": 11,
			"trigger": "pine_v2_click",
			"changed_at": "2026-07-21T10:00:00Z"
		}`),
	}
	change, ok := event.ControlChange()
	if !ok {
		t.Fatal("ControlChange returned !ok")
	}
	if change.Controller != "human" || change.ModeEpoch != 7 || change.ComputerEpoch != 11 || change.Trigger != "pine_v2_click" {
		t.Errorf("change = %+v", change)
	}
	if change.ChangedAt == nil || change.ChangedAt.Year() != 2026 {
		t.Errorf("ChangedAt = %v", change.ChangedAt)
	}

	for _, other := range []*AgentEvent{
		nil,
		{Type: "step", Controller: "human", ModeEpoch: 7},
		{Type: "controller_changed", Payload: json.RawMessage(`{}`)},
		{Type: "controller_changed", Controller: "human", Payload: json.RawMessage(`not-json`)},
	} {
		if _, ok := other.ControlChange(); ok {
			t.Errorf("ControlChange(%+v) returned ok", other)
		}
	}
}
