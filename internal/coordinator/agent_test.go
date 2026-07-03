package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAgentRun_BodyAndPath(t *testing.T) {
	var body map[string]any
	var path, auth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("X-Pine-Auth")
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"task_id":"t1","state":"running","session":"s","computer_id":"c"}`)
	})

	skills := []string{"book-flight"}
	task, err := c.AgentRun(context.Background(), "ct_a", "s", "buy milk", AgentRunOptions{Skills: skills, Context: "extra"})
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
	if task.TaskID != "t1" || task.State != "running" {
		t.Errorf("parsed task = %+v, want task_id=t1 state=running", task)
	}
	if len(task.Raw) == 0 {
		t.Error("task.Raw escape hatch should carry the full wire object")
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
			fmt.Fprint(w, `{"task_id":"t1","state":"idle"}`)
		case r.URL.Path == "/v1/sessions/s/agent/reset" && r.Method == "POST":
			fmt.Fprint(w, `{"task_id":"t1","state":"idle"}`)
		case r.URL.Path == "/v1/sessions/s/agent" && r.Method == "GET":
			fmt.Fprint(w, `{"task_id":"t1","state":"running","goal":"buy milk"}`)
		case r.URL.Path == "/v1/sessions/s/agent/result" && r.Method == "GET":
			fmt.Fprint(w, `{"status":"ok","terminal_reason":"completed","summary":"done","artifacts":[],"findings":[],"usage":{"llm":{"input_tokens":30,"output_tokens":12,"cache_read_tokens":0,"cache_write_tokens":0,"total_tokens":42},"duration":{"total_ms":10,"active_ms":10},"cost":{"currency":"USD","total":0,"llm":0,"compute":null}}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	// mutations + status return the typed Task; result returns the typed AgentResult.
	if task, err := c.AgentCancel(context.Background(), "ct_", "s"); err != nil || task.State != "idle" {
		t.Errorf("AgentCancel: %+v, %v", task, err)
	}
	if task, err := c.AgentReset(context.Background(), "ct_", "s"); err != nil || task.State != "idle" {
		t.Errorf("AgentReset: %+v, %v", task, err)
	}
	if task, err := c.AgentTask(context.Background(), "ps_", "s"); err != nil || task.State != "running" || task.Goal != "buy milk" {
		t.Errorf("AgentTask: %+v, %v", task, err)
	}
	res, err := c.AgentResult(context.Background(), "ps_", "s")
	if err != nil {
		t.Fatalf("AgentResult: %v", err)
	}
	if res.Status != "ok" || res.TerminalReason != "completed" || res.Usage.LLM.TotalTokens != 42 {
		t.Errorf("AgentResult parsed = %+v, want status=ok terminal=completed total_tokens=42", res)
	}
}

// TestAgentTask_TolerantTimestamps: an empty or non-RFC3339 timestamp must NOT fail the
// parse — the SDK doesn't functionally need it, and a 200 never hard-failed under the old
// raw return. Mirrors Session/parseTime tolerance (a bad timestamp → nil *time.Time).
func TestAgentTask_TolerantTimestamps(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// created_at empty, updated_at non-RFC3339 — both must be tolerated.
		fmt.Fprint(w, `{"task_id":"t1","state":"idle","created_at":"","updated_at":"2026/01/02 03:04"}`)
	})
	task, err := c.AgentTask(context.Background(), "ps_", "s")
	if err != nil {
		t.Fatalf("AgentTask must not hard-fail on a bad/empty timestamp: %v", err)
	}
	if task.CreatedAt != nil || task.UpdatedAt != nil {
		t.Errorf("bad/empty timestamps should parse to nil, got created=%v updated=%v", task.CreatedAt, task.UpdatedAt)
	}
	if task.State != "idle" {
		t.Errorf("state = %q, want idle", task.State)
	}
}

// TestAgentResult_TolerantArtifactTimestamp: a bad artifact modified_at must not drop the
// whole terminal outcome (summary/findings/usage) over one cosmetic field.
func TestAgentResult_TolerantArtifactTimestamp(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok","terminal_reason":"completed","summary":"done","findings":[],"usage":{"llm":{"input_tokens":1,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"total_tokens":1},"duration":{"total_ms":0,"active_ms":0},"cost":{"currency":"USD","total":0,"llm":0,"compute":null}},"artifacts":[{"root":"workdir","relative_path":"a.txt","content_type":"text/plain","size":3,"sha256":"x","modified_at":""}]}`)
	})
	res, err := c.AgentResult(context.Background(), "ps_", "s")
	if err != nil {
		t.Fatalf("AgentResult must not hard-fail on a bad artifact timestamp: %v", err)
	}
	if res.Status != "ok" || len(res.Artifacts) != 1 || res.Artifacts[0].RelativePath != "a.txt" {
		t.Errorf("result = %+v, want status=ok + 1 artifact a.txt", res)
	}
	if res.Artifacts[0].ModifiedAt != nil {
		t.Errorf("empty artifact modified_at should parse to nil, got %v", res.Artifacts[0].ModifiedAt)
	}
}

// TestComputerUse_ActionWins: a params key named "action" must NOT clobber the action verb
// (params carry the action's arguments, not the verb).
func TestComputerUse_ActionWins(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"ok":true}`)
	})
	if _, err := c.ComputerUse(context.Background(), "ps_", "s", "left_click", map[string]any{"action": "evil", "x": 1}); err != nil {
		t.Fatalf("ComputerUse: %v", err)
	}
	if body["action"] != "left_click" {
		t.Errorf("action = %v, want left_click (a params 'action' key must not clobber the verb)", body["action"])
	}
	if body["x"].(float64) != 1 {
		t.Errorf("params should still merge: x = %v", body["x"])
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

// The typed agent/control event-iterator tests live in stream_test.go.
