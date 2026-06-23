package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"go.pinesandbox.io/computer/internal/base/problem"
)

func TestAgentRun_BodyAndPath(t *testing.T) {
	var body map[string]any
	var path, auth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("X-Pine-Auth")
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"turn_id":"t1","status":"running"}`)
	})

	skills := []string{"book-flight"}
	raw, err := c.AgentRun(context.Background(), "ct_a", "s", "buy milk", AgentRunOptions{Skills: skills, Context: "extra"})
	if err != nil {
		t.Fatalf("AgentRun: %v", err)
	}
	if path != "/v1/sessions/s/agent/run" {
		t.Errorf("path = %q", path)
	}
	if auth != "ct_a" {
		t.Errorf("auth = %q", auth)
	}
	if body["goal"] != "buy milk" || body["context"] != "extra" {
		t.Errorf("body = %v", body)
	}
	if _, ok := body["constraints"]; ok {
		t.Errorf("constraints should be omitted when nil: %v", body)
	}
	if s, _ := body["skills"].([]any); len(s) != 1 || s[0] != "book-flight" {
		t.Errorf("skills = %v", body["skills"])
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil || got["turn_id"] != "t1" {
		t.Errorf("raw body = %s (%v)", raw, err)
	}
}

func TestAgentSteer_OptionalFields(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agent/steer") {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{}`)
	})
	attempt := 2
	if _, err := c.AgentSteer(context.Background(), "ct_", "s", "go left", AgentSteerOptions{ExpectedTurnID: "t1", TurnAttempt: &attempt}); err != nil {
		t.Fatal(err)
	}
	if body["text"] != "go left" || body["expected_turn_id"] != "t1" || body["turn_attempt"].(float64) != 2 {
		t.Errorf("body = %v", body)
	}
}

func TestAgentSteer_OmitsEmpty(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{}`)
	})
	if _, err := c.AgentSteer(context.Background(), "ct_", "s", "x", AgentSteerOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["expected_turn_id"]; ok {
		t.Errorf("expected_turn_id should be omitted: %v", body)
	}
	if _, ok := body["turn_attempt"]; ok {
		t.Errorf("turn_attempt should be omitted: %v", body)
	}
}

func TestAgentCancelResetTaskResult(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/sessions/s/agent/cancel" && r.Method == "POST":
			fmt.Fprint(w, `{"cancelled":true}`)
		case r.URL.Path == "/v1/sessions/s/agent/reset" && r.Method == "POST":
			fmt.Fprint(w, `{"reset":true}`)
		case r.URL.Path == "/v1/sessions/s/agent" && r.Method == "GET":
			fmt.Fprint(w, `{"status":"idle"}`)
		case r.URL.Path == "/v1/sessions/s/agent/result" && r.Method == "GET":
			fmt.Fprint(w, `{"terminal_reason":"done"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	for _, call := range []func() (json.RawMessage, error){
		func() (json.RawMessage, error) { return c.AgentCancel(context.Background(), "ct_", "s") },
		func() (json.RawMessage, error) { return c.AgentReset(context.Background(), "ct_", "s") },
		func() (json.RawMessage, error) { return c.AgentTask(context.Background(), "ps_", "s") },
		func() (json.RawMessage, error) { return c.AgentResult(context.Background(), "ps_", "s") },
	} {
		if _, err := call(); err != nil {
			t.Errorf("call: %v", err)
		}
	}
}

func TestDriveTrack(t *testing.T) {
	var cuBody map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions/s/observe":
			fmt.Fprint(w, `{"tree":"..."}`)
		case "/v1/sessions/s/computer-use":
			_ = json.NewDecoder(r.Body).Decode(&cuBody)
			fmt.Fprint(w, `{"ok":true}`)
		case "/v1/sessions/s/upload_file":
			fmt.Fprint(w, `{"staged":true}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	if _, err := c.Observe(context.Background(), "ps_", "s"); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := c.ComputerUse(context.Background(), "ps_", "s", "click", map[string]any{"x": 10, "y": 20}); err != nil {
		t.Fatalf("ComputerUse: %v", err)
	}
	if cuBody["action"] != "click" || cuBody["x"].(float64) != 10 {
		t.Errorf("computer-use body = %v (action + params should merge)", cuBody)
	}
	if _, err := c.UploadFile(context.Background(), "ps_", "s", "#f", "/tmp/a.png"); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
}

// TestAgentEvents_StreamAndResume drives the real SSE path: multiple frames, id tracking,
// the Last-Event-ID resume header, empty-data skip, and the returned cursor.
func TestAgentEvents_StreamAndResume(t *testing.T) {
	var gotLastEventID string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		gotLastEventID = r.Header.Get("Last-Event-ID")
		fl, _ := w.(http.Flusher)
		// A comment-only frame (no data) is skipped; two data frames carry ids.
		fmt.Fprint(w, ": keep-alive\n\n")
		fmt.Fprint(w, "id: 1\ndata: {\"type\":\"started\"}\n\n")
		fmt.Fprint(w, "id: 2\ndata: {\"type\":\"progress\"}\n\n")
		if fl != nil {
			fl.Flush()
		}
	})

	var seen []string
	last, err := c.AgentEvents(context.Background(), "ps_", "s", "0", func(data []byte) error {
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		seen = append(seen, m["type"].(string))
		return nil
	})
	if err != nil {
		t.Fatalf("AgentEvents: %v", err)
	}
	if gotLastEventID != "0" {
		t.Errorf("Last-Event-ID sent = %q, want 0", gotLastEventID)
	}
	if len(seen) != 2 || seen[0] != "started" || seen[1] != "progress" {
		t.Errorf("events = %v, want [started progress]", seen)
	}
	if last != "2" {
		t.Errorf("returned cursor = %q, want 2", last)
	}
}

// TestAgentEvents_CallbackStop: returning ErrStop halts the stream and is returned.
func TestAgentEvents_CallbackStop(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "id: 1\ndata: {\"n\":1}\n\n")
		fmt.Fprint(w, "id: 2\ndata: {\"n\":2}\n\n")
	})
	n := 0
	last, err := c.AgentEvents(context.Background(), "ps_", "s", "", func(data []byte) error {
		n++
		return ErrStop
	})
	if !errors.Is(err, ErrStop) {
		t.Fatalf("err = %v, want ErrStop", err)
	}
	if n != 1 {
		t.Errorf("callback ran %d times, want 1 (stopped after first)", n)
	}
	if last != "1" {
		t.Errorf("cursor = %q, want 1", last)
	}
}

// TestAgentEvents_Non2xxIsError: a non-2xx is a problem+json body, surfaced as APIError —
// not a clean EOF.
func TestAgentEvents_Non2xxIsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(404)
		fmt.Fprint(w, `{"type":"/errors/session-not-found","status":404,"detail":"gone"}`)
	})
	var ae *problem.APIError
	if _, err := c.AgentEvents(context.Background(), "ps_", "s", "", func([]byte) error { return nil }); !errors.As(err, &ae) {
		t.Fatalf("err = %T (%v), want *problem.APIError", err, err)
	} else if ae.Status != 404 {
		t.Errorf("status = %d, want 404", ae.Status)
	}
}
