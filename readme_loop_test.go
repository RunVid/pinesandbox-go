package pinesandbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The README's delegate loop ends on the result of the turn it started, not on an
// earlier turn's result replayed from an empty cursor.
func TestReadmeDelegateLoopSkipsReplayedResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /v2/sessions/s1/agent/run":
			fmt.Fprint(w, `{"turn_id":"turn_2","state":"running","first_event_id":3,"last_event_id":3}`)
		case "GET /v2/sessions/s1/agent":
			fmt.Fprint(w, `{"task_id":"task_1","session":"s1","computer_id":"c1","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`)
		case "GET /v2/sessions/s1/agent/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "id: 2\nevent: result\ndata: {\"event_id\":2,\"task_id\":\"task_1\",\"turn_id\":\"turn_1\",\"type\":\"result\",\"payload\":{\"terminal_reason\":\"completed\",\"summary\":\"OLD\"}}\n\n")
			fmt.Fprint(w, "id: 4\nevent: result\ndata: {\"event_id\":4,\"task_id\":\"task_1\",\"turn_id\":\"turn_2\",\"type\":\"result\",\"payload\":{\"terminal_reason\":\"completed\",\"summary\":\"NEW\"}}\n\n")
		case "GET /v2/sessions/s1/agent/turns/turn_1", "GET /v2/sessions/s1/agent/turns/turn_2":
			id := r.URL.Path[len("/v2/sessions/s1/agent/turns/"):]
			fmt.Fprintf(w, `{"turn_id":%q,"state":"completed","result":{"terminal_reason":"completed","summary":%q,"artifacts":[],"usage":{}},"first_event_id":1,"last_event_id":4}`, id, id)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(buildTestConnection(t, "http://unused", srv.URL), "sb-1", "ct_x", "running"); err != nil {
		t.Fatal(err)
	}
	sess, err := comp.AdoptSession("s1", "ps_x")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	ended, err := func() (*AgentTurn, error) {
		ag := sess.Agent()
		turn, err := ag.Run(ctx, "goal", RunOptions{})
		if err != nil {
			return nil, err
		}
		task, err := ag.Status(ctx, TaskOptions{})
		if err != nil {
			return nil, err
		}
		cursor := ""
		for ev, err := range ag.Events(ctx, cursor, EventsOptions{ExpectedTaskID: task.TaskID}) {
			if err != nil {
				return nil, err
			}
			switch ev.Type {
			case EventResult:
				if ev.TurnID != turn.TurnID {
					continue
				}
				return ag.Wait(ctx, ev.TurnID, 0)
			}
		}
		return nil, fmt.Errorf("stream ended")
	}()
	if err != nil || ended.TurnID != "turn_2" {
		t.Fatalf("loop ended on %+v err=%v, want turn_2", ended, err)
	}
}
