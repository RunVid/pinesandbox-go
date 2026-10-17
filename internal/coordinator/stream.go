package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"net/url"
	"strconv"
	"time"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/sse"
	"go.pinesandbox.io/computer/internal/base/transport"
)

// Typed SSE event iterators. `for ev, err := range AgentEvents(...)` yields
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

// AgentEvent is one public activity event. Payload follows the event type; Raw
// preserves additional public fields for forward compatibility.
type AgentEvent struct {
	SchemaVersion int
	// EventID is the per-task monotonic seq AND the SSE resume cursor: persist it across a
	// restart and pass it back as AgentMode.Events' lastEventID (strconv.FormatInt) to
	// resume the feed without gaps.
	EventID    int64
	TaskID     string
	Session    string
	ComputerID string
	// TurnID is the turn this event belongs to — the same ID Run, Answer and Wait
	// return. An answer's input opens the turn it belongs to. Empty on older records
	// without a trustworthy turn; never infer one from event order.
	TurnID     string
	Ts         *time.Time // event timestamp; nil if absent/unparseable (a bad ts never fails the event)
	Type       string     // status|input|progress|activity|frame|controller_changed|result
	Controller string     // agent|human|locked; empty on legacy/synthetic events
	ModeEpoch  int64      // per-session controller ordering fence
	Payload    json.RawMessage
	Raw        json.RawMessage // the full event envelope (forward-compat escape hatch)
}

// AgentControlChange is the typed view of a controller_changed TaskEvent. The
// top-level Controller + ModeEpoch form the authoritative per-session control
// projection; the remaining fields describe the Computer-wide commit.
type AgentControlChange struct {
	Controller    string
	ModeEpoch     int64
	ComputerEpoch int64
	Trigger       string
	ChangedAt     *time.Time
}

// ControlChange returns the typed control transition when this event is a
// controller_changed frame, else (nil, false). Unknown trigger values remain
// intact for forward compatibility.
func (e *AgentEvent) ControlChange() (*AgentControlChange, bool) {
	if e == nil || e.Type != "controller_changed" || e.Controller == "" {
		return nil, false
	}
	var wire struct {
		ComputerEpoch int64  `json:"computer_epoch"`
		Trigger       string `json:"trigger"`
		ChangedAt     string `json:"changed_at"`
	}
	if err := json.Unmarshal(e.Payload, &wire); err != nil {
		return nil, false
	}
	return &AgentControlChange{
		Controller:    e.Controller,
		ModeEpoch:     e.ModeEpoch,
		ComputerEpoch: wire.ComputerEpoch,
		Trigger:       wire.Trigger,
		ChangedAt:     parseTime(wire.ChangedAt),
	}, true
}

// AgentProgress is the agent's caller-facing narration (a progress event).
type AgentProgress struct {
	Text string
}

// Progress returns the narration when this event is a progress frame, else (nil, false).
func (e *AgentEvent) Progress() (*AgentProgress, bool) {
	if e == nil || e.Type != "progress" {
		return nil, false
	}
	var wire struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(e.Payload, &wire); err != nil || wire.Text == "" {
		return nil, false
	}
	return &AgentProgress{Text: wire.Text}, true
}

// AgentInput is caller-authored input the coordinator accepted: the run goal,
// an answer to a question, or steering. Run and answer input open the event's turn.
type AgentInput struct {
	Kind    string // run | answer | steer
	Text    string
	Context string // run only
}

// Input returns the accepted input when this event is an input frame, else (nil, false).
func (e *AgentEvent) Input() (*AgentInput, bool) {
	if e == nil || e.Type != "input" {
		return nil, false
	}
	var wire struct {
		Kind    string `json:"kind"`
		Text    string `json:"text"`
		Context string `json:"context"`
	}
	if err := json.Unmarshal(e.Payload, &wire); err != nil || wire.Kind == "" {
		return nil, false
	}
	return &AgentInput{Kind: wire.Kind, Text: wire.Text, Context: wire.Context}, true
}

// AgentActivity is one step of the agent's work (an activity event). Upsert by
// ID: every step ends with exactly one outcome (completed, failed, declined, or
// interrupted when its turn stopped first), after running when the step takes
// time, and parallel steps interleave. A reader resuming mid-stream may see an
// outcome without its start. Completed means the step finished, not that the
// goal succeeded.
//
// Kind says what the step was for a person (browser, command, file, skill,
// tool); the set grows, so treat a kind you don't know as a generic step.
// Title is the Computer's own description of the step, in the imperative
// ("Open Home Page – E-Label", "Click “Log in”", "Edit notes.md", "Use skill
// book-flight"); State says how it went. Every outcome has one. Display it as
// is; do not parse it. Facts are what the step acted on, each member optional;
// nil when there are none.
type AgentActivity struct {
	ID    string
	State string // running | completed | failed | declined | interrupted
	Kind  string
	Title string
	Facts *ActivityFacts
}

// ActivityFacts are what a step acted on, from the Computer's own record of
// the work: never a command line, tool argument or anything typed into a page.
type ActivityFacts struct {
	Page   *ActivityPage   `json:"page,omitempty"`   // the page a browser step ended on
	Target *ActivityTarget `json:"target,omitempty"` // the element a browser step acted on
	Files  []ActivityFile  `json:"files,omitempty"`  // work-directory files the step touched
	Skill  *ActivitySkill  `json:"skill,omitempty"`  // one of your learned skills the step used
	// Steps lists the actions a browser step ran, in order; a failed action is
	// the last one.
	Steps []string `json:"steps,omitempty"`
}

// ActivityPage is a page by its title and address (scheme, host and path).
type ActivityPage struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// ActivityTarget is an element by its accessibility role and visible name.
type ActivityTarget struct {
	Role string `json:"role,omitempty"`
	Name string `json:"name,omitempty"`
}

// ActivityFile is a work-directory file and how the step touched it
// (ActivityFileCreated, …).
type ActivityFile struct {
	Path   string `json:"path"`
	Change string `json:"change"`
}

// ActivitySkill is one of your learned skills, by name.
type ActivitySkill struct {
	Name string `json:"name"`
}

// Activity returns the step when this event is an activity frame, else
// (nil, false).
func (e *AgentEvent) Activity() (*AgentActivity, bool) {
	if e == nil || e.Type != "activity" {
		return nil, false
	}
	return parseActivity(e.Payload)
}

// parseActivity decodes an activity payload. A malformed optional field or
// fact member is dropped on its own, never fatal.
func parseActivity(payload json.RawMessage) (*AgentActivity, bool) {
	var wire struct {
		ActivityID string          `json:"activity_id"`
		State      string          `json:"state"`
		Kind       json.RawMessage `json:"kind"`
		Title      json.RawMessage `json:"title"`
		Facts      json.RawMessage `json:"facts"`
	}
	if json.Unmarshal(payload, &wire) != nil || wire.ActivityID == "" || wire.State == "" {
		return nil, false
	}
	a := &AgentActivity{ID: wire.ActivityID, State: wire.State}
	_ = json.Unmarshal(wire.Kind, &a.Kind)
	_ = json.Unmarshal(wire.Title, &a.Title)
	var facts map[string]json.RawMessage
	if json.Unmarshal(wire.Facts, &facts) == nil {
		a.Facts = decodeActivityFacts(facts)
	}
	return a, true
}

// decodeActivityFacts reads each fact member on its own, so one malformed or
// future member costs only itself.
func decodeActivityFacts(members map[string]json.RawMessage) *ActivityFacts {
	var f ActivityFacts
	if json.Unmarshal(members["page"], &f.Page) != nil {
		f.Page = nil
	}
	if json.Unmarshal(members["target"], &f.Target) != nil {
		f.Target = nil
	}
	if json.Unmarshal(members["skill"], &f.Skill) != nil {
		f.Skill = nil
	}
	var files []json.RawMessage
	if json.Unmarshal(members["files"], &files) == nil {
		for _, raw := range files {
			var file ActivityFile
			if json.Unmarshal(raw, &file) == nil && file.Path != "" {
				f.Files = append(f.Files, file)
			}
		}
	}
	var steps []json.RawMessage
	if json.Unmarshal(members["steps"], &steps) == nil {
		for _, raw := range steps {
			var step string
			if json.Unmarshal(raw, &step) == nil && step != "" {
				f.Steps = append(f.Steps, step)
			}
		}
	}
	if f.Page == nil && f.Target == nil && f.Files == nil && f.Skill == nil && f.Steps == nil {
		return nil
	}
	return &f
}

// AgentFrame is a picture of the Computer's screen, taken after a step that
// may have changed it.
type AgentFrame struct {
	MediaType  string // image/webp today; skip a type you can't show
	Width      int
	Height     int
	Data       []byte         // the decoded image
	ObservedAt *time.Time     // capture time, nil on older records
	Activity   *AgentActivity // matching step snapshot, nil for independent observations
}

// Frame returns the picture when this event is a frame event, else
// (nil, false).
func (e *AgentEvent) Frame() (*AgentFrame, bool) {
	if e == nil || e.Type != "frame" {
		return nil, false
	}
	var wire struct {
		MediaType  string          `json:"media_type"`
		Width      int             `json:"width"`
		Height     int             `json:"height"`
		Data       []byte          `json:"data"` // base64 on the wire
		ObservedAt json.RawMessage `json:"observed_at"`
		Activity   json.RawMessage `json:"activity"`
	}
	if err := json.Unmarshal(e.Payload, &wire); err != nil || wire.MediaType == "" || len(wire.Data) == 0 {
		return nil, false
	}
	var observedAt string
	_ = json.Unmarshal(wire.ObservedAt, &observedAt)
	activity, _ := parseActivity(wire.Activity)
	return &AgentFrame{MediaType: wire.MediaType, Width: wire.Width, Height: wire.Height, Data: wire.Data,
		ObservedAt: parseTime(observedAt), Activity: activity}, true
}

// Result returns how the work ended when this event is a result event, else
// (nil, false) — the same shape as AgentTurn.Result.
func (e *AgentEvent) Result() (*AgentResult, bool) {
	if e == nil || e.Type != "result" {
		return nil, false
	}
	var wire agentResultWire
	if err := json.Unmarshal(e.Payload, &wire); err != nil {
		return nil, false
	}
	r := wire.toAgentResult()
	r.Raw = e.Payload
	return r, true
}

// ControlEvent is one control-plane SSE event (spec ControlEventV1), discriminated by the
// SSE event: line. Data is the per-type payload (kept raw — the four shapes are small +
// stable; read the fields you need).
type ControlEvent struct {
	ID   int64
	Type string // controller_changed | idle_changed | handoff_completed | handoff_failed
	Data json.RawMessage
}

// AgentEventsOptions binds resumed cursors to their task and controls images.
type AgentEventsOptions struct {
	SkipFrames     bool
	ExpectedTaskID string
}

// AgentEvents streams the session's typed feed with guarded cursor resumption.
func (c *Client) AgentEvents(ctx context.Context, token, name, lastEventID string, opts AgentEventsOptions) iter.Seq2[AgentEvent, error] {
	path := c.agentPath(name, "/events")
	q := url.Values{}
	if opts.SkipFrames {
		q.Set("frames", "none")
	}
	if opts.ExpectedTaskID != "" {
		q.Set("expected_task_id", opts.ExpectedTaskID)
	}
	if len(q) != 0 {
		path += "?" + q.Encode()
	}
	return streamTyped(ctx, c, path, token, lastEventID, decodeAgentEvent)
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
		TurnID        string          `json:"turn_id"`
		Ts            string          `json:"ts"`
		Type          string          `json:"type"`
		Controller    string          `json:"controller"`
		ModeEpoch     int64           `json:"mode_epoch"`
		Payload       json.RawMessage `json:"payload"`
	}
	if json.Unmarshal([]byte(f.Data), &w) != nil {
		return AgentEvent{}, false
	}
	return AgentEvent{
		SchemaVersion: w.SchemaVersion, EventID: w.EventID, TaskID: w.TaskID,
		Session: w.Session, ComputerID: w.ComputerID, TurnID: w.TurnID, Ts: parseTime(w.Ts),
		Type: w.Type, Controller: w.Controller, ModeEpoch: w.ModeEpoch,
		Payload: w.Payload, Raw: json.RawMessage(f.Data),
	}, true
}

// decodeControlEvent reads the type off the SSE event: line (the v1 control discriminator)
// and the ID off the SSE id: line (the resume cursor), keeping the data payload raw. A
// body `id` is the fallback for a frame without an id: line. A frame without a type
// (keepalive/comment) is skipped.
func decodeControlEvent(f sse.Frame) (ControlEvent, bool) {
	if f.Event == "" || f.Event == "message" {
		return ControlEvent{}, false
	}
	id, err := strconv.ParseInt(f.ID, 10, 64)
	if err != nil {
		var hdr struct {
			ID int64 `json:"id"`
		}
		_ = json.Unmarshal([]byte(f.Data), &hdr)
		id = hdr.ID
	}
	return ControlEvent{ID: id, Type: f.Event, Data: json.RawMessage(f.Data)}, true
}

// errConsumerStopped is the internal signal that the caller broke out of the range loop
// (yield returned false). It never escapes the resume engine.
var errConsumerStopped = errors.New("consumer stopped")

// errTerminalFrame is the internal signal that a finite feed delivered its terminal event.
var errTerminalFrame = errors.New("terminal frame")

// streamTyped is the resuming iterator for a continuous event feed (agent and control
// events): it never ends on its own.
func streamTyped[T any](ctx context.Context, c *Client, path, token, lastID string, decode func(sse.Frame) (T, bool)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		cursor := lastID
		resumeStream(ctx, c, path, token, &cursor, decode, nil, yield)
	}
}

// resumeStream is the shared resuming engine for the event feeds. Two failure surfaces are
// handled distinctly: a failure to OPEN is terminal unless it's a transient transport fault
// (then bounded reconnect), while any end of an ESTABLISHED stream — clean EOF or a
// mid-stream read fault — is resumable, so it reconnects from the cursor under the bounded
// budget. The budget counts CONSECUTIVE no-progress reconnects and resets whenever a
// connection delivers an event, so a healthy feed that the server periodically recycles
// streams forever; only a feed that can't be re-established surfaces ErrStreamLost. A
// finite feed names its terminal event: delivering it ends the stream. cursor holds the
// resume position (the last SSE id seen) and is updated as frames arrive.
func resumeStream[T any](ctx context.Context, c *Client, path, token string, cursor *string,
	decode func(sse.Frame) (T, bool), terminal func(T) bool, yield func(T, error) bool) {
	fails := 0
	// host + op are the PRIMARY spine (WHICH Computer, WHICH feed) — constant for this
	// stream, so a terminal ErrStreamLost always names the resource that dropped even
	// though the failing reconnects have no response to read from. requestID is the
	// SECONDARY handle: the X-Request-Id of the most recently ESTABLISHED stream.
	host := c.Host()
	op := transport.Operation("GET", path)
	requestID := ""
	for {
		sr, err := c.openSSE(ctx, "GET", path, token, nil, lastEventIDHeader(*cursor))
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
			if !streamRetry(ctx, c, &fails, err, host, op, requestID, yield) {
				return
			}
			continue
		}
		if rid := sr.Headers.Get("X-Request-Id"); rid != "" {
			requestID = rid
		}
		delivered, ferr := pumpFrames(sr.Body, cursor, decode, terminal, yield)
		if errors.Is(ferr, errConsumerStopped) || errors.Is(ferr, errTerminalFrame) || ctx.Err() != nil {
			return
		}
		if delivered {
			fails = 0
		}
		// The established stream ended (EOF or read fault) → reconnect under budget.
		if !streamRetry(ctx, c, &fails, ferr, host, op, requestID, yield) {
			return
		}
	}
}

// pumpFrames yields decoded events off one established SSE stream until it ends, advancing
// the resume cursor as it goes. It always closes body. It returns whether any event was
// delivered, plus the terminating cause: nil on clean EOF, errConsumerStopped if the caller
// broke, errTerminalFrame after a finite feed's terminal event, or the raw read fault on a
// mid-stream drop.
func pumpFrames[T any](body io.ReadCloser, cursor *string, decode func(sse.Frame) (T, bool), terminal func(T) bool, yield func(T, error) bool) (bool, error) {
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
			continue // malformed frame — one bad frame must not kill a feed
		}
		delivered = true
		if !yield(ev, nil) {
			return delivered, errConsumerStopped
		}
		if terminal != nil && terminal(ev) {
			return delivered, errTerminalFrame
		}
	}
	return delivered, nil
}

// streamRetry charges one reconnect against the budget. It returns true to reconnect (after
// sleeping the backoff), or false to stop — either because the budget is spent (it yields
// ErrStreamLost, wrapping cause + the resource-first host/op/request-id context) or because
// ctx ended during the wait.
func streamRetry[T any](ctx context.Context, c *Client, fails *int, cause error, host, op, requestID string, yield func(T, error) bool) bool {
	*fails++
	if *fails > c.streamBudget {
		var zero T
		yield(zero, streamLost(cause, host, op, requestID))
		return false
	}
	return c.streamWait(ctx, *fails)
}

// streamLost builds the terminal ErrStreamLost error, wrapping the underlying cause and
// folding the resource-first context (host = WHICH Computer, op = WHICH feed — the primary
// spine — then the last established stream's request id) into the message. The %w chain keeps
// BOTH errors.Is(err, ErrStreamLost) AND errors.As to the underlying transport fault working
// (Go 1.20+ multi-%w). It adds its OWN suffix only when the cause doesn't already carry SDK
// resource context: a reconnect *TimeoutError/*ConnectionError already renders host/op, so
// re-appending it here would print host= twice (a clean EOF / raw read fault carries none).
func streamLost(cause error, host, op, requestID string) error {
	var err error
	switch {
	case cause != nil:
		err = fmt.Errorf("%w: %w", ErrStreamLost, cause)
	default:
		err = ErrStreamLost
	}
	if !causeCarriesContext(cause) {
		if suffix := problem.ContextSuffix(host, op, requestID); suffix != "" {
			err = fmt.Errorf("%w%s", err, suffix)
		}
	}
	return err
}

// causeCarriesContext reports whether cause is a transport fault that already renders the
// resource suffix (host/op) — so streamLost doesn't double-fold it.
func causeCarriesContext(cause error) bool {
	var te *transport.TimeoutError
	var ce *transport.ConnectionError
	return errors.As(cause, &te) || errors.As(cause, &ce)
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

// streamWait sleeps the reconnect backoff on the client's clock, returning false if ctx
// ended during the wait.
func (c *Client) streamWait(ctx context.Context, attempt int) bool {
	return c.clock.Sleep(ctx, c.streamBackoff(attempt)) == nil && ctx.Err() == nil
}

// defaultStreamBackoff is the shared reconnect cadence for a dropped event feed
// (x-pine-retry.client.stream_reconnect): 500ms·2^(attempt-1) plus up to 200ms of
// jitter, capped at 5s.
func (c *Client) defaultStreamBackoff(attempt int) time.Duration {
	d := time.Duration(float64(500*time.Millisecond)*math.Pow(2, float64(attempt-1))) +
		time.Duration(c.clock.Jitter()*float64(200*time.Millisecond))
	return min(d, 5*time.Second)
}
