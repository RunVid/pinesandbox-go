package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"math/rand"
	"net/url"
	"time"

	"go.pinesandbox.io/computer/internal/base/sse"
	"go.pinesandbox.io/computer/internal/base/transport"
)

// Typed SSE event iterators (design §8). `for ev, err := range AgentEvents(...)` yields
// typed events; the feed is CONTINUOUS (per-session, across turns), so a drop (EOF / read
// fault / connection reset on reopen) is transparently resumed via Last-Event-ID under a
// bounded reconnect budget. A non-nil err is terminal: a non-reconnectable error (auth /
// gone) or budget exhaustion ends the loop. The caller stops early by breaking; cancelling
// ctx ends it cleanly.

// defaultReconnectBudget bounds CONSECUTIVE failed reconnects (a dead/recycling Computer)
// before the iterator surfaces ErrStreamLost. Reset whenever an event is delivered.
const defaultReconnectBudget = 5

// ErrStreamLost is the terminal error yielded when the feed can't be re-established within
// the reconnect budget.
var ErrStreamLost = errors.New("pinesandbox: event stream lost (reconnect budget exhausted)")

// AgentEvent is one agent TaskEvent (spec TaskEvent): the typed envelope + a per-type
// Payload kept raw (the payload shapes are loose by design). Fields not modelled here
// (usage_delta, redactions, trace_id, mode_epoch) remain available via Raw.
type AgentEvent struct {
	SchemaVersion int
	// EventID is the per-task monotonic seq AND the SSE resume cursor: persist it across a
	// restart and pass it back as AgentMode.Events' lastEventID (strconv.FormatInt) to
	// resume the feed without gaps.
	EventID     int64
	TaskID      string
	Session     string
	ComputerID  string
	ThreadID    string
	TurnID      string
	TurnAttempt int
	Ts          *time.Time // event timestamp; nil if absent/unparseable (a bad ts never fails the event)
	Type        string     // status|reasoning|command|step|needs_input|usage|screenshot|result|usage.finalized
	Source      string     // agent|control|files|system
	Visibility  string     // user|operator|debug
	Terminal    bool       // true iff this frame ends a TURN (not the persistent Task)
	TaskState   string     // idle|running|paused — a non-terminal state transition (empty = none)
	Reason      string     // accompanies TaskState, e.g. needs_input (the pause reason)
	Payload     json.RawMessage
	Raw         json.RawMessage // the full TaskEvent envelope (forward-compat escape hatch)
}

// ControlEvent is one control-plane SSE event (spec ControlEventV1), discriminated by the
// SSE event: line. Data is the per-type payload (kept raw — the four shapes are small +
// stable; read the fields you need).
type ControlEvent struct {
	ID   int64
	Type string // controller_changed | idle_changed | handoff_completed | handoff_failed
	Data json.RawMessage
}

// AgentEvents streams the session's agent TaskEvent feed as a typed, resuming iterator.
func (c *Client) AgentEvents(ctx context.Context, token, name, lastEventID string) iter.Seq2[AgentEvent, error] {
	return streamTyped(ctx, c, c.agentPath(name, "/events"), token, lastEventID, decodeAgentEvent)
}

// ControlEvents streams the session's control-plane event feed as a typed, resuming iterator.
func (c *Client) ControlEvents(ctx context.Context, token, name, lastEventID string) iter.Seq2[ControlEvent, error] {
	return streamTyped(ctx, c, "/v1/sessions/"+url.PathEscape(name)+"/control/events", token, lastEventID, decodeControlEvent)
}

// decodeAgentEvent parses a frame's data (the full TaskEvent JSON). A malformed frame is
// skipped (ok=false), not surfaced — one bad frame must not kill a continuous feed.
func decodeAgentEvent(f sse.Frame) (AgentEvent, bool) {
	var w struct {
		SchemaVersion int             `json:"schema_version"`
		EventID       int64           `json:"event_id"`
		TaskID        string          `json:"task_id"`
		Session       string          `json:"session"`
		ComputerID    string          `json:"computer_id"`
		ThreadID      string          `json:"thread_id"`
		TurnID        string          `json:"turn_id"`
		TurnAttempt   int             `json:"turn_attempt"`
		Ts            string          `json:"ts"`
		Type          string          `json:"type"`
		Source        string          `json:"source"`
		Visibility    string          `json:"visibility"`
		Terminal      bool            `json:"terminal"`
		TaskState     string          `json:"task_state"`
		Reason        string          `json:"reason"`
		Payload       json.RawMessage `json:"payload"`
	}
	if json.Unmarshal([]byte(f.Data), &w) != nil {
		return AgentEvent{}, false
	}
	return AgentEvent{
		SchemaVersion: w.SchemaVersion, EventID: w.EventID, TaskID: w.TaskID,
		Session: w.Session, ComputerID: w.ComputerID, ThreadID: w.ThreadID,
		TurnID: w.TurnID, TurnAttempt: w.TurnAttempt, Ts: parseTime(w.Ts),
		Type: w.Type, Source: w.Source, Visibility: w.Visibility,
		Terminal: w.Terminal, TaskState: w.TaskState, Reason: w.Reason,
		Payload: w.Payload, Raw: json.RawMessage(f.Data),
	}, true
}

// decodeControlEvent reads the type off the SSE event: line (the v1 control discriminator)
// + keeps the data payload raw. A frame without a type (keepalive/comment) is skipped.
func decodeControlEvent(f sse.Frame) (ControlEvent, bool) {
	if f.Event == "" || f.Event == "message" {
		return ControlEvent{}, false
	}
	var hdr struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal([]byte(f.Data), &hdr)
	return ControlEvent{ID: hdr.ID, Type: f.Event, Data: json.RawMessage(f.Data)}, true
}

// errConsumerStopped is the internal signal that the caller broke out of the range loop
// (yield returned false). It never escapes streamTyped.
var errConsumerStopped = errors.New("consumer stopped")

// streamTyped is the shared resuming-iterator engine for the continuous event feeds. Two
// failure surfaces are handled distinctly: a failure to OPEN is terminal unless it's a
// transient transport fault (then bounded reconnect), while any end of an ESTABLISHED
// stream — clean EOF or a mid-stream read fault — is by definition resumable for a
// continuous feed, so it always reconnects from the cursor under the bounded budget. The
// budget counts CONSECUTIVE no-progress reconnects and resets whenever a connection
// delivers an event, so a healthy feed that the server periodically recycles streams
// forever; only a feed that can't be re-established surfaces ErrStreamLost.
func streamTyped[T any](ctx context.Context, c *Client, path, token, lastID string, decode func(sse.Frame) (T, bool)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		cursor := lastID
		fails := 0
		for {
			sr, err := c.openSSE(ctx, "GET", path, token, nil, lastEventIDHeader(cursor))
			if err != nil {
				if ctx.Err() != nil {
					return // caller cancelled while the open was in flight — end cleanly,
					// not as a spurious context.Canceled error (transport returns it raw)
				}
				if !reconnectable(err) {
					var zero T
					yield(zero, err) // terminal: auth / session-gone / spec mismatch / non-2xx
					return
				}
				if !streamRetry(ctx, c, &fails, err, yield) {
					return
				}
				continue
			}
			delivered, ferr := pumpFrames(sr.Body, &cursor, decode, yield)
			if errors.Is(ferr, errConsumerStopped) || ctx.Err() != nil {
				return
			}
			if delivered {
				fails = 0
			}
			// The established stream ended (EOF or read fault) → reconnect under budget.
			if !streamRetry(ctx, c, &fails, ferr, yield) {
				return
			}
		}
	}
}

// pumpFrames yields decoded events off one established SSE stream until it ends, advancing
// the resume cursor as it goes. It always closes body. It returns whether any event was
// delivered, plus the terminating cause: nil on clean EOF, errConsumerStopped if the caller
// broke, or the raw read fault on a mid-stream drop.
func pumpFrames[T any](body io.ReadCloser, cursor *string, decode func(sse.Frame) (T, bool), yield func(T, error) bool) (bool, error) {
	defer body.Close()
	delivered := false
	for f, ferr := range sse.Frames(body) {
		if ferr != nil {
			return delivered, ferr
		}
		if f.ID != "" {
			// Advance the resume cursor for ANY id-bearing frame, before the data/decode
			// filters — so a reconnect doesn't replay a frame we've already seen and
			// skipped (a keepalive or an undecodable one).
			*cursor = f.ID
		}
		if f.Data == "" {
			continue // keepalive comment / event with no data
		}
		ev, ok := decode(f)
		if !ok {
			continue // malformed frame — one bad frame must not kill a continuous feed
		}
		delivered = true
		if !yield(ev, nil) {
			return delivered, errConsumerStopped
		}
	}
	return delivered, nil
}

// streamRetry charges one reconnect against the budget. It returns true to reconnect (after
// sleeping the backoff), or false to stop — either because the budget is spent (it yields
// ErrStreamLost, wrapping cause) or because ctx ended during the wait.
func streamRetry[T any](ctx context.Context, c *Client, fails *int, cause error, yield func(T, error) bool) bool {
	*fails++
	if *fails > c.streamBudget {
		var zero T
		if cause != nil {
			// Double-wrap: errors.Is(err, ErrStreamLost) AND errors.As to the underlying
			// transport fault both succeed (Go 1.20+ multi-%w).
			yield(zero, fmt.Errorf("%w: %w", ErrStreamLost, cause))
		} else {
			yield(zero, ErrStreamLost)
		}
		return false
	}
	return c.streamWait(ctx, *fails)
}

// reconnectable reports whether an openSSE error is a transient transport fault worth
// resuming (a connection reset / timeout) vs a terminal one (auth, gone, spec mismatch).
func reconnectable(err error) bool {
	var ce *transport.ConnectionError
	var te *transport.TimeoutError
	return errors.As(err, &ce) || errors.As(err, &te)
}

func lastEventIDHeader(cursor string) map[string]string {
	if cursor == "" {
		return nil
	}
	return map[string]string{"Last-Event-ID": cursor}
}

// streamWait sleeps the reconnect backoff, returning false if ctx ended during the wait.
func (c *Client) streamWait(ctx context.Context, attempt int) bool {
	select {
	case <-time.After(c.streamBackoff(attempt)):
		return true
	case <-ctx.Done():
		return false
	}
}

// defaultStreamBackoff is bounded exponential with jitter, capped at 5s — the reconnect
// cadence for a dropped event feed.
func defaultStreamBackoff(attempt int) time.Duration {
	base := 0.5 * math.Pow(2, float64(attempt-1))
	d := base + rand.Float64()*0.2
	if d > 5.0 {
		d = 5.0
	}
	return time.Duration(d * float64(time.Second))
}
