package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

func TestAgentEventActivity(t *testing.T) {
	a, ok := (&AgentEvent{Type: "activity", Payload: json.RawMessage(`{"activity_id":"act_1","state":"completed","title":"Edit notes.md"}`)}).Activity()
	if !ok || !reflect.DeepEqual(a, &AgentActivity{ID: "act_1", State: "completed", Title: "Edit notes.md"}) {
		t.Fatalf("Activity() = %+v, %v", a, ok)
	}

	step, ok := (&AgentEvent{Type: "activity", Payload: json.RawMessage(`{"activity_id":"act_2","state":"failed"}`)}).Activity()
	if !ok || !reflect.DeepEqual(step, &AgentActivity{ID: "act_2", State: "failed"}) {
		t.Fatalf("untitled step = %+v, %v", step, ok)
	}

	// Future values are preserved for the caller to skip; a malformed optional
	// title never costs the step.
	future, ok := (&AgentEvent{Type: "activity", Payload: json.RawMessage(`{"activity_id":"act_3","state":"paused","title":7,"future":"x"}`)}).Activity()
	if !ok || future.State != "paused" || future.Title != "" {
		t.Fatalf("future activity = %+v, %v", future, ok)
	}

	for _, bad := range []*AgentEvent{
		nil,
		{Type: "progress", Payload: json.RawMessage(`{"activity_id":"act_1","state":"running"}`)},
		{Type: "activity", Payload: json.RawMessage(`{"state":"running"}`)},
		{Type: "activity", Payload: json.RawMessage(`{"activity_id":"act_1"}`)},
		{Type: "activity", Payload: json.RawMessage(`not json`)},
	} {
		if _, ok := bad.Activity(); ok {
			t.Errorf("Activity() ok for %+v", bad)
		}
	}
}

func TestAgentEventProgressInputResult(t *testing.T) {
	if p, ok := (&AgentEvent{Type: "progress", Payload: json.RawMessage(`{"text":"Reading the K2 article."}`)}).Progress(); !ok || p.Text != "Reading the K2 article." {
		t.Fatalf("Progress() = %+v, %v", p, ok)
	}
	if _, ok := (&AgentEvent{Type: "progress", Payload: json.RawMessage(`{}`)}).Progress(); ok {
		t.Fatal("empty progress accepted")
	}

	in, ok := (&AgentEvent{Type: "input", Payload: json.RawMessage(`{"kind":"answer","text":"Lisbon"}`)}).Input()
	if !ok || *in != (AgentInput{Kind: "answer", Text: "Lisbon"}) {
		t.Fatalf("Input() = %+v, %v", in, ok)
	}
	if _, ok := (&AgentEvent{Type: "progress", Payload: json.RawMessage(`{"kind":"run","text":"x"}`)}).Input(); ok {
		t.Fatal("Input() ok for a progress event")
	}

	payload := json.RawMessage(`{"terminal_reason":"completed","summary":"Everest is taller.",
		"artifacts":[{"root":"workdir","relative_path":"mountains.csv","content_type":"text/csv","size":96,"sha256":"abc","modified_at":"2026-10-10T00:00:00Z"}]}`)
	r, ok := (&AgentEvent{Type: "result", Payload: payload}).Result()
	if !ok || r.Summary != "Everest is taller." || r.TerminalReason != "completed" || len(r.Artifacts) != 1 ||
		r.Artifacts[0].RelativePath != "mountains.csv" || string(r.Raw) != string(payload) {
		t.Fatalf("Result() = %+v, %v", r, ok)
	}
	if _, ok := (&AgentEvent{Type: "status", Payload: payload}).Result(); ok {
		t.Fatal("Result() ok for a status event")
	}
}

func TestAgentEventActivityFacts(t *testing.T) {
	a, ok := (&AgentEvent{Type: "activity", Payload: json.RawMessage(`{"activity_id":"act_1","state":"failed","kind":"browser",` +
		`"title":"Click “Log in”","facts":{"page":{"title":"Sign in","url":"https://example.com/login"},"target":{"role":"button","name":"Log in"},` +
		`"files":[{"path":"invoice.pdf","change":"read"},7],"steps":["Fill in “Email”","Click “Log in”",3],"skill":"malformed","future":{"x":1}}}`)}).Activity()
	want := &AgentActivity{ID: "act_1", State: "failed", Kind: "browser", Title: "Click “Log in”", Facts: &ActivityFacts{
		Page:   &ActivityPage{Title: "Sign in", URL: "https://example.com/login"},
		Target: &ActivityTarget{Role: "button", Name: "Log in"},
		Files:  []ActivityFile{{Path: "invoice.pdf", Change: "read"}},
		Steps:  []string{"Fill in “Email”", "Click “Log in”"},
	}}
	if !ok || !reflect.DeepEqual(a, want) {
		t.Fatalf("Activity() = %+v %+v, %v", a, a.Facts, ok)
	}
	// A facts value that is not an object costs only the facts.
	bare, ok := (&AgentEvent{Type: "activity", Payload: json.RawMessage(`{"activity_id":"act_2","state":"completed","kind":"command","title":"Run a command","facts":"x"}`)}).Activity()
	if !ok || !reflect.DeepEqual(bare, &AgentActivity{ID: "act_2", State: "completed", Kind: "command", Title: "Run a command"}) {
		t.Fatalf("malformed facts = %+v, %v", bare, ok)
	}
}

func TestAgentEventFrame(t *testing.T) {
	f, ok := (&AgentEvent{Type: "frame", Payload: json.RawMessage(`{"media_type":"image/webp","width":1280,"height":720,"data":"UklGRg=="}`)}).Frame()
	if !ok || !reflect.DeepEqual(f, &AgentFrame{MediaType: "image/webp", Width: 1280, Height: 720, Data: []byte("RIFF")}) {
		t.Fatalf("Frame() = %+v, %v", f, ok)
	}
	for _, bad := range []*AgentEvent{
		nil,
		{Type: "activity", Payload: json.RawMessage(`{"media_type":"image/webp","width":1,"height":1,"data":"UklGRg=="}`)},
		{Type: "frame", Payload: json.RawMessage(`{"media_type":"image/webp","width":1,"height":1,"data":""}`)},
		{Type: "frame", Payload: json.RawMessage(`{"media_type":"image/webp","width":1,"height":1,"data":"not base64!"}`)},
		{Type: "frame", Payload: json.RawMessage(`{"width":1,"height":1,"data":"UklGRg=="}`)},
	} {
		if _, ok := bad.Frame(); ok {
			t.Errorf("Frame() ok for %+v", bad)
		}
	}
}

func TestAgentEventsCanLeaveFramesOut(t *testing.T) {
	for _, skip := range []bool{false, true} {
		var query string
		c := streamTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			fmt.Fprint(w, "id: 1\ndata: {\"type\":\"progress\",\"event_id\":1}\n\n")
		})
		for _, err := range c.AgentEvents(context.Background(), "ps_", "s", "", AgentEventsOptions{SkipFrames: skip}) {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if want := map[bool]string{false: "", true: "frames=none"}[skip]; query != want {
			t.Fatalf("skip=%v: query %q, want %q", skip, query, want)
		}
	}
}

func TestAgentEventsPinsResumedCursorToTask(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("expected_task_id") != "old&task" || r.URL.Query().Get("frames") != "none" || r.Header.Get("Last-Event-ID") != "19" {
			t.Errorf("lost replay context: %s header=%s", r.URL, r.Header.Get("Last-Event-ID"))
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"type":"/errors/stale-task","status":409,"title":"Task changed","retryable":false}`)
	})
	var gotErr error
	for _, err := range c.AgentEvents(context.Background(), "ps_", "s", "19", AgentEventsOptions{ExpectedTaskID: "old&task", SkipFrames: true}) {
		gotErr = err
	}
	if gotErr == nil || calls != 1 {
		t.Fatalf("stale cursor retried: calls=%d err=%v", calls, gotErr)
	}
}
