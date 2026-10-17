package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/transport"
)

const turnJSON = `{"turn_id":"turn_t_1","state":"running","started_at":"2026-01-02T03:04:05Z","first_event_id":1,"last_event_id":1}`

func TestAgentRun_BodyPathAndKey(t *testing.T) {
	var body map[string]any
	var path, auth, key string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path, auth, key = r.URL.Path, r.Header.Get("X-Pine-Auth"), r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, turnJSON)
	})
	turn, err := c.AgentRun(context.Background(), "ct_a", "s", "buy milk", AgentRunOptions{
		Skills: []string{"book-flight"}, Context: "extra", IdempotencyKey: "message-41",
		Constraints: &AgentRunConstraints{MaxTokens: 1000, MaxWallMs: 60000},
	})
	if err != nil {
		t.Fatalf("AgentRun: %v", err)
	}
	if path != "/v2/sessions/s/agent/run" || auth != "ct_a" || key != "message-41" {
		t.Errorf("path=%q auth=%q key=%q", path, auth, key)
	}
	limits, _ := body["constraints"].(map[string]any)
	if body["goal"] != "buy milk" || body["context"] != "extra" || len(limits) != 2 || limits["max_wall_ms"] != float64(60000) {
		t.Errorf("body = %v", body)
	}
	if s, _ := body["skills"].([]any); len(s) != 1 || s[0] != "book-flight" {
		t.Errorf("skills = %v", body["skills"])
	}
	if turn.TurnID != "turn_t_1" || turn.State != "running" || turn.StartedAt == nil || turn.FirstEventID != 1 || len(turn.Raw) == 0 {
		t.Errorf("parsed turn = %+v", turn)
	}
}

// A run's lost response is resent with the same key, which names the same
// turn. A run without a key is refused before anything is sent: its resend
// could start a second turn.
func TestAgentRun_ResendsUnderItsKey(t *testing.T) {
	var calls atomic.Int32
	var keys []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		fmt.Fprint(w, turnJSON)
	})
	turn, err := c.AgentRun(context.Background(), "ct_a", "s", "g", AgentRunOptions{IdempotencyKey: "message-41"})
	if err != nil || turn.TurnID != "turn_t_1" || calls.Load() != 2 || !slices.Equal(keys, []string{"message-41", "message-41"}) {
		t.Fatalf("keyed run: turn=%+v err=%v keys=%v", turn, err, keys)
	}
	if _, err := c.AgentRun(context.Background(), "ct_a", "s", "g", AgentRunOptions{}); err == nil || calls.Load() != 2 {
		t.Fatalf("keyless run: err=%v calls=%d", err, calls.Load())
	}
}

func TestAgentAnswerSteerCancel_AddressTheirTurn(t *testing.T) {
	bodies := map[string]map[string]any{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies[r.URL.Path] = body
		if r.URL.RawQuery != "" {
			t.Errorf("mutation carried a query: %s", r.URL)
		}
		fmt.Fprint(w, turnJSON)
	})
	ctx := context.Background()
	secrets := map[string]string{"LOGIN_PASSWORD": "answer-secret-123"}
	if _, err := c.AgentAnswer(ctx, "ct_", "s", "turn_t_1", "use $LOGIN_PASSWORD", AgentAnswerOptions{Secrets: secrets}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AgentSteer(ctx, "ct_", "s", "turn_t_8", "go left", AgentSteerOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AgentCancel(ctx, "ct_", "s", "turn_t_8"); err != nil {
		t.Fatal(err)
	}
	answer := bodies["/v2/sessions/s/agent/answer"]
	if answer["turn_id"] != "turn_t_1" || answer["answer"] != "use $LOGIN_PASSWORD" || len(answer) != 3 {
		t.Errorf("answer body = %v", answer)
	}
	if got, _ := answer["secrets"].(map[string]any); got["LOGIN_PASSWORD"] != "answer-secret-123" {
		t.Errorf("answer secrets = %v", answer["secrets"])
	}
	if steer := bodies["/v2/sessions/s/agent/steer"]; steer["turn_id"] != "turn_t_8" || steer["text"] != "go left" || len(steer) != 2 {
		t.Errorf("steer body = %v", steer)
	}
	if cancel := bodies["/v2/sessions/s/agent/cancel"]; cancel["turn_id"] != "turn_t_8" || len(cancel) != 1 {
		t.Errorf("cancel body = %v", cancel)
	}
}

// Answer and cancel replays name the same turn, so a lost response is resent:
// a dropped connection, or a gateway status that cannot say whether the
// Computer acted. A steer is never resent, and neither is a definite refusal.
func TestAgentMutationRetries(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(*Client) error{
		"answer": func(c *Client) error {
			_, err := c.AgentAnswer(ctx, "ct_", "s", "turn_t_1", "yes", AgentAnswerOptions{})
			return err
		},
		"cancel": func(c *Client) error { _, err := c.AgentCancel(ctx, "ct_", "s", "turn_t_1"); return err },
		"steer": func(c *Client) error {
			_, err := c.AgentSteer(ctx, "ct_", "s", "turn_t_1", "go", AgentSteerOptions{})
			return err
		},
	}
	lost := map[string]func(http.ResponseWriter){
		"dropped connection": func(w http.ResponseWriter) {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
		},
		"502": func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) },
		"503": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"type":"/errors/sandbox-not-ready","status":503,"retryable":true}`)
		},
		"504": func(w http.ResponseWriter) { w.WriteHeader(http.StatusGatewayTimeout) },
		"408": func(w http.ResponseWriter) { w.WriteHeader(http.StatusRequestTimeout) },
		"409": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"type":"/errors/stale-turn","status":409}`)
		},
	}
	for name, call := range calls {
		for fault, respond := range lost {
			var sent atomic.Int32
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if sent.Add(1) == 1 {
					respond(w)
					return
				}
				fmt.Fprint(w, turnJSON)
			})
			err := call(c)
			resent := name != "steer" && fault != "409"
			if want := map[bool]int32{true: 2, false: 1}[resent]; sent.Load() != want || resent != (err == nil) {
				t.Errorf("%s after %s: sent=%d err=%v", name, fault, sent.Load(), err)
			}
		}
	}

	// Resends wait longer each time, at least the gateway's Retry-After, for
	// at most the resend budget; then the last lost response is returned.
	for _, tc := range []struct {
		retryAfter string
		waits      []time.Duration
	}{
		{"", []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second}},
		{"7", []time.Duration{7 * time.Second, 7 * time.Second, 7 * time.Second, 7 * time.Second}},
	} {
		var sent atomic.Int32
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			sent.Add(1)
			if tc.retryAfter != "" {
				w.Header().Set("Retry-After", tc.retryAfter)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		var waits []time.Duration
		c.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
		var apiErr *problem.APIError
		if err := calls["answer"](c); !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable ||
			int(sent.Load()) != len(tc.waits)+1 || !slices.Equal(waits, tc.waits) {
			t.Errorf("Retry-After %q: sent=%d waits=%v err=%v", tc.retryAfter, sent.Load(), waits, err)
		}
	}
}

// A run whose response never arrives may have started: the error names the
// key to send it again with, and still unwraps to the gateway's error.
func TestAgentRun_UnconfirmedRunNamesItsKey(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	_, err := c.AgentRun(context.Background(), "ct_a", "s", "g", AgentRunOptions{IdempotencyKey: "message-41"})
	var unconfirmed *RunUnconfirmedError
	var apiErr *problem.APIError
	if !errors.As(err, &unconfirmed) || unconfirmed.IdempotencyKey != "message-41" || !errors.As(err, &apiErr) {
		t.Fatalf("err = %v", err)
	}
	// A refusal is definite: the run did not start, so no key is offered.
	c = newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"type":"/errors/session-busy","status":409}`)
	})
	if _, err := c.AgentRun(context.Background(), "ct_a", "s", "g", AgentRunOptions{IdempotencyKey: "message-42"}); errors.As(err, &unconfirmed) {
		t.Fatalf("refused run = %v", err)
	}
}

// Canceling while waiting to resend ends the call with the context's error.
func TestAgentResendStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c.sleep = sleepCtx
	if _, err := c.AgentCancel(ctx, "ct_", "s", "turn_t_1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// A run whose context ends before its response arrives may have started: the
// error still names the key, keeps the lost response, and reports the
// context's end.
func TestAgentRun_ContextEndKeepsItsKey(t *testing.T) {
	// The deadline passes while waiting to resend a lost response.
	var sent atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})
	c.sleep = sleepCtx
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := c.AgentRun(ctx, "ct_a", "s", "g", AgentRunOptions{IdempotencyKey: "message-41"})
	var unconfirmed *RunUnconfirmedError
	var apiErr *problem.APIError
	if !errors.As(err, &unconfirmed) || unconfirmed.IdempotencyKey != "message-41" || !errors.Is(err, context.DeadlineExceeded) ||
		!errors.As(err, &apiErr) || apiErr.Status != http.StatusBadGateway || sent.Load() != 1 {
		t.Fatalf("deadline while resending: sent=%d err=%v", sent.Load(), err)
	}

	// The caller cancels while the request is in flight.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	c = newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		cancel()
		<-r.Context().Done()
	})
	_, err = c.AgentRun(ctx, "ct_a", "s", "g", AgentRunOptions{IdempotencyKey: "message-42"})
	if !errors.As(err, &unconfirmed) || unconfirmed.IdempotencyKey != "message-42" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled in flight: err=%v", err)
	}
}

// Wait repeats the bounded server hold until the turn leaves running, never
// following its successor; at the timeout it returns the turn as it is.
func TestAgentWait(t *testing.T) {
	var holds []string
	reads := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/sessions/s/agent/turns/turn_t_1" || r.Header.Get("X-Pine-Auth") != "ps_" {
			t.Errorf("read %s auth=%q", r.URL.Path, r.Header.Get("X-Pine-Auth"))
		}
		holds = append(holds, r.URL.Query().Get("wait_seconds"))
		reads++
		switch reads {
		case 1:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"type":"/errors/events-unavailable","status":503,"retryable":true}`)
		case 2:
			fmt.Fprint(w, turnJSON)
		default:
			fmt.Fprint(w, `{"turn_id":"turn_t_1","state":"answered","next_turn_id":"turn_t_8","result":{"terminal_reason":"needs_input","summary":"Which account?","artifacts":[],"usage":{}},"started_at":"2026-01-02T03:04:05Z","ended_at":"2026-01-02T03:04:09Z","first_event_id":1,"last_event_id":6}`)
		}
	})
	var sleeps []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	turn, err := c.AgentWait(context.Background(), "ps_", "s", "turn_t_1", time.Minute)
	if err != nil || turn.State != "answered" || turn.NextTurnID != "turn_t_8" || turn.Question() != "Which account?" || turn.EndedAt == nil {
		t.Fatalf("turn=%+v err=%v", turn, err)
	}
	if len(holds) != 3 || holds[0] != "25" || holds[2] != "25" {
		t.Errorf("holds = %v, want the bounded server wait each time", holds)
	}
	if !slices.Equal(sleeps, []time.Duration{time.Second}) {
		t.Errorf("sleeps = %v, want one retry delay", sleeps)
	}

	// A zero timeout reads once; an expired wait returns the running turn.
	holds, reads = nil, 1
	if turn, err := c.AgentWait(context.Background(), "ps_", "s", "turn_t_1", 0); err != nil || turn.State != "running" || len(holds) != 1 || holds[0] != "0" {
		t.Fatalf("immediate read: turn=%+v err=%v holds=%v", turn, err, holds)
	}
}

// The Computer holds whole seconds: a fractional timeout is waited out by the
// SDK, never rounded up into a longer server hold or down into no wait. A turn
// that has already ended returns at once.
func TestAgentWait_SubsecondTimeout(t *testing.T) {
	var holds []string
	state := "running"
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		holds = append(holds, r.URL.Query().Get("wait_seconds"))
		fmt.Fprint(w, strings.Replace(turnJSON, `"running"`, `"`+state+`"`, 1))
	})
	var sleeps []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return sleepCtx(ctx, d)
	}
	turn, err := c.AgentWait(context.Background(), "ps_", "s", "turn_t_1", 300*time.Millisecond)
	if err != nil || turn.State != "running" {
		t.Fatalf("turn=%+v err=%v", turn, err)
	}
	if !slices.Equal(holds, []string{"0", "0"}) {
		t.Fatalf("holds = %v, want a read now and one at the deadline", holds)
	}
	if len(sleeps) != 1 || sleeps[0] <= 0 || sleeps[0] > 300*time.Millisecond {
		t.Fatalf("sleeps = %v, want the remainder of the timeout", sleeps)
	}

	holds, sleeps, state = nil, nil, "completed"
	if turn, err := c.AgentWait(context.Background(), "ps_", "s", "turn_t_1", 300*time.Millisecond); err != nil ||
		turn.State != "completed" || len(holds) != 1 || len(sleeps) != 0 {
		t.Fatalf("ended turn: %+v %v, holds %v, sleeps %v", turn, err, holds, sleeps)
	}
}

// Wait reads again after a lost response or a problem marked retryable, and
// returns any other error at once.
func TestAgentWait_RetriesLostReads(t *testing.T) {
	problemJSON := func(status int, extra string) func(http.ResponseWriter) {
		return func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"type":"/errors/x","status":%d%s}`, status, extra)
		}
	}
	for _, tc := range []struct {
		name    string
		respond func(http.ResponseWriter)
		retried bool
	}{
		{"dropped connection", func(w http.ResponseWriter) {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
		}, true},
		{"408", func(w http.ResponseWriter) { w.WriteHeader(http.StatusRequestTimeout) }, true},
		{"502", func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) }, true},
		{"503 marked final", problemJSON(503, `,"retryable":false`), true},
		{"504", func(w http.ResponseWriter) { w.WriteHeader(http.StatusGatewayTimeout) }, true},
		{"500 marked retryable", problemJSON(500, `,"retryable":true`), true},
		{"500 marked final", problemJSON(500, `,"retryable":false`), false},
		{"404", problemJSON(404, `,"retryable":false`), false},
		{"410", problemJSON(410, `,"retryable":false`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads atomic.Int32
			// No transport retry: only Wait decides whether to read again.
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if reads.Add(1) == 1 {
					tc.respond(w)
					return
				}
				fmt.Fprint(w, strings.Replace(turnJSON, `"running"`, `"completed"`, 1))
			}, transport.WithRetry(1, nil))
			turn, err := c.AgentWait(context.Background(), "ps_", "s", "turn_t_1", time.Minute)
			if tc.retried && (err != nil || turn.State != "completed" || reads.Load() != 2) {
				t.Fatalf("not read again: turn=%+v err=%v reads=%d", turn, err, reads.Load())
			}
			if !tc.retried && (err == nil || reads.Load() != 1) {
				t.Fatalf("read again: err=%v reads=%d", err, reads.Load())
			}
		})
	}
}

func TestAgentWait_ExplicitErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusGone)
		fmt.Fprint(w, `{"type":"/errors/history-expired","status":410,"pruned_through":9}`)
	})
	_, err := c.AgentWait(context.Background(), "ps_", "s", "turn_t_1", time.Minute)
	var apiErr *problem.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusGone {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.AgentWait(context.Background(), "ps_", "s", "", time.Minute); err == nil {
		t.Fatal("empty turn ID accepted")
	}
}

func TestAgentTaskTurnsAndReset(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/sessions/s/agent/reset" && r.Method == "POST":
			fmt.Fprint(w, `{"task_id":"t1","session":"s","computer_id":"c","created_at":"","updated_at":"2026/01/02 03:04"}`)
		case r.URL.Path == "/v2/sessions/s/agent" && r.Method == "GET":
			fmt.Fprintf(w, `{"task_id":"t1","session":"s","computer_id":"c","turn":%s,"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`, turnJSON)
		case r.URL.Path == "/v2/sessions/s/agent/turns" && r.Method == "GET":
			fmt.Fprintf(w, `{"turns":[{"turn_id":"turn_t_1","state":"completed","result":{"terminal_reason":"completed","summary":"done","artifacts":[{"root":"workdir","relative_path":"a.txt","content_type":"text/plain","size":3,"sha256":"x","modified_at":""}],"usage":{"llm":{"input_tokens":30,"output_tokens":12,"cache_read_tokens":0,"cache_write_tokens":0,"total_tokens":42},"duration":{"total_ms":10},"cost":{"currency":"USD","total":0,"llm":0,"compute":null}}},"started_at":"","first_event_id":1,"last_event_id":4},%s]}`, turnJSON)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()
	// Bad or empty timestamps never fail a parse.
	if task, err := c.AgentReset(ctx, "ct_", "s", AgentTaskOptions{ExpectedTaskID: "t1"}); err != nil || task.TaskID != "t1" || task.Turn != nil || task.CreatedAt != nil || task.UpdatedAt != nil {
		t.Errorf("AgentReset: %+v, %v", task, err)
	}
	if task, err := c.AgentTask(ctx, "ps_", "s", AgentTaskOptions{}); err != nil || task.Turn == nil || task.Turn.TurnID != "turn_t_1" || task.CreatedAt == nil {
		t.Errorf("AgentTask: %+v, %v", task, err)
	}
	turns, err := c.AgentTurns(ctx, "ps_", "s", AgentTaskOptions{})
	if err != nil || len(turns) != 2 {
		t.Fatalf("AgentTurns: %+v, %v", turns, err)
	}
	done := turns[0]
	if done.State != "completed" || done.Result == nil || done.Result.Summary != "done" || done.Result.Usage.LLM.TotalTokens != 42 || done.Question() != "" ||
		len(done.Result.Artifacts) != 1 || done.Result.Artifacts[0].ModifiedAt != nil || done.StartedAt != nil {
		t.Errorf("completed turn = %+v", done)
	}
}

func TestAgentResult_AuthorPayload(t *testing.T) {
	raw := []byte(`{"terminal_reason":"draft_registered","summary":"draft saved","artifacts":[],"author":{"draft":{"name":"book-x","version":2}}}`)
	res, err := parseAgentResult(raw)
	if err != nil {
		t.Fatalf("parse typed author result: %v", err)
	}
	if res.TerminalReason != "draft_registered" || res.Author == nil || !strings.Contains(string(res.Author.Draft), `"name":"book-x"`) {
		t.Fatalf("author payload = %+v", res)
	}
}

func TestAgentTaskGuardsReachReadsAndReset(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("expected_task_id") != "old task/&" {
			t.Errorf("missing or corrupted task guard: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"type":"/errors/stale-task","status":409,"detail":"task replaced"}`)
	})
	ctx := context.Background()
	opts := AgentTaskOptions{ExpectedTaskID: "old task/&"}
	_, statusErr := c.AgentTask(ctx, "ps_", "s", opts)
	_, turnsErr := c.AgentTurns(ctx, "ps_", "s", opts)
	_, resetErr := c.AgentReset(ctx, "ct_", "s", opts)
	for _, err := range []error{statusErr, turnsErr, resetErr} {
		if err == nil || !strings.Contains(err.Error(), "task replaced") {
			t.Fatalf("stale guard was swallowed: %v", err)
		}
	}
	if _, err := c.AgentReset(ctx, "ct_", "s", AgentTaskOptions{}); err == nil {
		t.Fatal("unguarded reset accepted")
	}
	if calls != 3 {
		t.Fatalf("guards retried or unguarded reset sent: %d requests", calls)
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
