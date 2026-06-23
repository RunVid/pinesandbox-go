package coordinator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/transport"
)

// streamTestClient is newTestClient with the reconnect backoff zeroed (so resume tests don't
// sleep) and a caller-chosen budget (so exhaustion tests terminate fast).
func streamTestClient(t *testing.T, budget int, handler http.HandlerFunc) *Client {
	t.Helper()
	c := newTestClient(t, handler)
	c.streamBackoff = func(int) time.Duration { return 0 }
	c.streamBudget = budget
	return c
}

// TestAgentEvents_StreamDecode drives one established stream: the initial Last-Event-ID
// header, an empty-data keepalive skipped, typed decode (Type / EventID / Raw escape hatch),
// and the cursor advancing — with no reconnect (the consumer breaks once it has both).
func TestAgentEvents_StreamDecode(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	var firstHdr string
	c := streamTestClient(t, 5, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		mu.Lock()
		conns++
		if conns == 1 {
			firstHdr = r.Header.Get("Last-Event-ID")
		}
		mu.Unlock()
		fmt.Fprint(w, ": keep-alive\n\n") // no data → skipped
		fmt.Fprint(w, "id: 1\ndata: {\"type\":\"started\",\"event_id\":1}\n\n")
		fmt.Fprint(w, "id: 2\ndata: {\"type\":\"progress\",\"event_id\":2}\n\n")
	})

	var types []string
	var ids []int64
	rawOK := true
	for ev, err := range c.AgentEvents(context.Background(), "ps_", "s", "0") {
		if err != nil {
			t.Fatalf("unexpected stream error: %v", err)
		}
		types = append(types, ev.Type)
		ids = append(ids, ev.EventID)
		if len(ev.Raw) == 0 {
			rawOK = false
		}
		if len(types) == 2 {
			break // collected both; stop before a clean-EOF reconnect
		}
	}
	if !rawOK {
		t.Error("AgentEvent.Raw should carry the full TaskEvent JSON")
	}
	if len(types) != 2 || types[0] != "started" || types[1] != "progress" {
		t.Errorf("types = %v, want [started progress]", types)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Errorf("event ids = %v, want [1 2]", ids)
	}
	mu.Lock()
	defer mu.Unlock()
	if conns != 1 {
		t.Errorf("opened %d connections, want 1 (no reconnect needed)", conns)
	}
	if firstHdr != "0" {
		t.Errorf("first Last-Event-ID = %q, want 0 (the passed-in cursor)", firstHdr)
	}
}

// TestAgentEvents_MidStreamDropResumes: a connection dropped mid-feed (a real read fault, not
// a clean EOF) is transparently resumed — the second connection carries the advanced cursor
// as Last-Event-ID and the consumer sees an unbroken event sequence.
func TestAgentEvents_MidStreamDropResumes(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	var resumeHdr string
	c := streamTestClient(t, 5, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns++
		n := conns
		if n == 2 {
			resumeHdr = r.Header.Get("Last-Event-ID")
		}
		mu.Unlock()
		if n == 1 {
			// Hijack and drop the TCP connection after one complete frame, before the
			// chunked terminator → the client read faults rather than seeing a clean EOF.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter is not a Hijacker")
			}
			conn, bufrw, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			defer conn.Close()
			fmt.Fprint(bufrw, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
			frame := "id: 1\ndata: {\"type\":\"started\",\"event_id\":1}\n\n"
			fmt.Fprintf(bufrw, "%x\r\n%s\r\n", len(frame), frame) // one chunk, no 0-terminator
			_ = bufrw.Flush()
			return // close mid-stream
		}
		fmt.Fprint(w, "id: 2\ndata: {\"type\":\"progress\",\"event_id\":2}\n\n")
	})

	var types []string
	for ev, err := range c.AgentEvents(context.Background(), "ps_", "s", "0") {
		if err != nil {
			t.Fatalf("unexpected stream error: %v", err)
		}
		types = append(types, ev.Type)
		if len(types) == 2 {
			break
		}
	}
	if len(types) != 2 || types[0] != "started" || types[1] != "progress" {
		t.Errorf("types = %v, want [started progress] (resumed across a mid-stream drop)", types)
	}
	mu.Lock()
	defer mu.Unlock()
	if conns < 2 {
		t.Fatalf("expected a reconnect, got %d connection(s)", conns)
	}
	if resumeHdr != "1" {
		t.Errorf("resume Last-Event-ID = %q, want 1 (the advanced cursor)", resumeHdr)
	}
}

// TestAgentEvents_ConsumerBreak: breaking out of the range halts immediately — the second
// buffered frame is never delivered and no reconnect is attempted.
func TestAgentEvents_ConsumerBreak(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	c := streamTestClient(t, 5, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns++
		mu.Unlock()
		fmt.Fprint(w, "id: 1\ndata: {\"type\":\"a\"}\n\n")
		fmt.Fprint(w, "id: 2\ndata: {\"type\":\"b\"}\n\n")
	})
	n := 0
	for _, err := range c.AgentEvents(context.Background(), "ps_", "s", "") {
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		n++
		break
	}
	if n != 1 {
		t.Errorf("delivered %d events, want 1 (broke after first)", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if conns != 1 {
		t.Errorf("opened %d connections, want 1 (no reconnect after break)", conns)
	}
}

// TestAgentEvents_Non2xxIsError: a non-2xx open is a problem+json body surfaced as a typed
// APIError — terminal, never retried (it's not a transient transport fault).
func TestAgentEvents_Non2xxIsError(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	c := streamTestClient(t, 5, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(404)
		fmt.Fprint(w, `{"type":"/errors/session-not-found","status":404,"detail":"gone"}`)
	})
	var got error
	n := 0
	for _, err := range c.AgentEvents(context.Background(), "ps_", "s", "") {
		if err != nil {
			got = err
			break
		}
		n++
	}
	if n != 0 {
		t.Errorf("delivered %d events, want 0", n)
	}
	var ae *problem.APIError
	if !errors.As(got, &ae) {
		t.Fatalf("err = %T (%v), want *problem.APIError", got, got)
	}
	if ae.Status != 404 {
		t.Errorf("status = %d, want 404", ae.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if conns != 1 {
		t.Errorf("opened %d connections, want 1 (a 404 must not be retried)", conns)
	}
}

// TestAgentEvents_OpenFaultBudget: when the feed cannot be opened (a dead host → repeated
// ConnectionError), the iterator gives up after the budget and yields ErrStreamLost wrapping
// the cause — it does not spin forever.
func TestAgentEvents_OpenFaultBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	raw := transport.New("http", strings.TrimPrefix(srv.URL, "http://"))
	srv.Close() // every dial is now refused → *ConnectionError on open
	c := NewClient(raw, 1)
	c.streamBackoff = func(int) time.Duration { return 0 }
	c.streamBudget = 2

	var got error
	for _, err := range c.AgentEvents(context.Background(), "ps_", "s", "") {
		if err != nil {
			got = err
			break
		}
		t.Fatal("a dead host should deliver no events")
	}
	if !errors.Is(got, ErrStreamLost) {
		t.Fatalf("err = %v, want ErrStreamLost", got)
	}
	var ce *transport.ConnectionError
	if !errors.As(got, &ce) {
		t.Errorf("ErrStreamLost should wrap the underlying *ConnectionError, got %v", got)
	}
}

// TestAgentEvents_EmptyFeedBudget: a server that opens the stream then immediately closes it
// without sending anything makes no progress; the iterator bounds these empty reconnects and
// surfaces ErrStreamLost (the clean-EOF, nil-cause budget branch) rather than hot-looping.
func TestAgentEvents_EmptyFeedBudget(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	c := streamTestClient(t, 2, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200) // empty body → clean EOF on return
	})
	var got error
	for _, err := range c.AgentEvents(context.Background(), "ps_", "s", "") {
		if err != nil {
			got = err
			break
		}
		t.Fatal("an empty feed should deliver no events")
	}
	if !errors.Is(got, ErrStreamLost) {
		t.Fatalf("err = %v, want ErrStreamLost", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if conns != c.streamBudget+1 {
		t.Errorf("opened %d connections, want %d (budget+1 then give up)", conns, c.streamBudget+1)
	}
}

// TestAgentEvents_TypedEnvelopeFields: the spec TaskEvent envelope parses into typed fields
// (ts, task_state/reason pause semantics, source/visibility/turn_attempt), and a bad ts is
// tolerated (nil, not a dropped event) — a 200 must never hard-fail on one malformed field.
func TestAgentEvents_TypedEnvelopeFields(t *testing.T) {
	c := streamTestClient(t, 5, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "id: 9\ndata: {\"event_id\":9,\"type\":\"needs_input\",\"ts\":\"2026-06-12T10:00:00Z\","+
			"\"source\":\"agent\",\"visibility\":\"user\",\"task_state\":\"paused\",\"reason\":\"needs_input\","+
			"\"turn_attempt\":2,\"payload\":{\"prompt\":\"which seat?\"}}\n\n")
		fmt.Fprint(w, "id: 10\ndata: {\"event_id\":10,\"type\":\"result\",\"ts\":\"garbage\",\"terminal\":true}\n\n")
	})
	var got []AgentEvent
	for ev, err := range c.AgentEvents(context.Background(), "ps_", "s", "") {
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		got = append(got, ev)
		if len(got) == 2 {
			break
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	a := got[0]
	if a.EventID != 9 || a.Type != "needs_input" {
		t.Fatalf("event 0 = %+v", a)
	}
	if a.TaskState != "paused" || a.Reason != "needs_input" {
		t.Errorf("pause fields = %q/%q, want paused/needs_input", a.TaskState, a.Reason)
	}
	if a.Source != "agent" || a.Visibility != "user" || a.TurnAttempt != 2 {
		t.Errorf("envelope = source=%q visibility=%q turn_attempt=%d", a.Source, a.Visibility, a.TurnAttempt)
	}
	if a.Ts == nil || a.Ts.Year() != 2026 {
		t.Errorf("Ts = %v, want the parsed 2026 timestamp", a.Ts)
	}
	if len(a.Payload) == 0 {
		t.Error("Payload should carry the raw per-type body")
	}
	if b := got[1]; b.Ts != nil {
		t.Errorf("a bad ts should parse to nil, got %v (the event must still decode)", b.Ts)
	} else if !b.Terminal {
		t.Error("event 1 should still decode (terminal=true) despite the bad ts")
	}
}

// TestAgentEvents_CursorAdvancesOnSkippedFrame: an id-bearing frame that is SKIPPED (a
// keepalive with no data) must still advance the resume cursor, so a reconnect does not
// replay it — the second connection carries that id as Last-Event-ID.
func TestAgentEvents_CursorAdvancesOnSkippedFrame(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	var resumeHdr string
	c := streamTestClient(t, 5, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns++
		n := conns
		if n == 2 {
			resumeHdr = r.Header.Get("Last-Event-ID")
		}
		mu.Unlock()
		if n == 1 {
			fmt.Fprint(w, "id: 5\n\n") // id-bearing, no data → skipped, but cursor must advance
			return                     // drop
		}
		fmt.Fprint(w, "id: 6\ndata: {\"type\":\"after\"}\n\n")
	})
	var types []string
	for ev, err := range c.AgentEvents(context.Background(), "ps_", "s", "0") {
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		types = append(types, ev.Type)
		break // only the post-resume event matters
	}
	if len(types) != 1 || types[0] != "after" {
		t.Errorf("types = %v, want [after]", types)
	}
	mu.Lock()
	defer mu.Unlock()
	if resumeHdr != "5" {
		t.Errorf("resume Last-Event-ID = %q, want 5 (the skipped frame's id advanced the cursor)", resumeHdr)
	}
}

// TestAgentEvents_CtxCancelCleanExit: a cancelled ctx ends the iterator cleanly — no event,
// and NO spurious context.Canceled yielded (the open-error path must honor ctx before it
// classifies the transport error).
func TestAgentEvents_CtxCancelCleanExit(t *testing.T) {
	c := streamTestClient(t, 5, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "id: 1\ndata: {\"type\":\"x\"}\n\n")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the first open
	n, errs := 0, 0
	for _, err := range c.AgentEvents(ctx, "ps_", "s", "") {
		if err != nil {
			errs++
			break
		}
		n++
	}
	if n != 0 || errs != 0 {
		t.Errorf("cancelled before start: events=%d errors=%d, want 0/0 (clean exit, no spurious error)", n, errs)
	}
}

// TestControlEvents_TypedDecode: control events are discriminated by the SSE event: line; a
// frame without a type (keepalive / "message") is skipped, the typed ones carry Type + raw
// Data, and the route is the control feed.
func TestControlEvents_TypedDecode(t *testing.T) {
	c := streamTestClient(t, 5, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/control/events") {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, "data: {\"keepalive\":true}\n\n") // no event: line → skipped
		fmt.Fprint(w, "id: 7\nevent: controller_changed\ndata: {\"id\":7,\"controller\":\"agent\"}\n\n")
		fmt.Fprint(w, "id: 8\nevent: idle_changed\ndata: {\"id\":8,\"idle\":true}\n\n")
	})
	var got []ControlEvent
	for ev, err := range c.ControlEvents(context.Background(), "ps_", "s", "") {
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got = append(got, ev)
		if len(got) == 2 {
			break
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (keepalive skipped)", len(got))
	}
	if got[0].Type != "controller_changed" || got[0].ID != 7 {
		t.Errorf("ev0 = %+v, want controller_changed/7", got[0])
	}
	if got[1].Type != "idle_changed" || got[1].ID != 8 {
		t.Errorf("ev1 = %+v, want idle_changed/8", got[1])
	}
	if len(got[0].Data) == 0 {
		t.Error("ControlEvent.Data should carry the raw payload")
	}
}
