package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/sse"
	"go.pinesandbox.io/computer/internal/base/transport"
)

// ---- agent track (/v2/sessions/{name}/agent[/*]) ----
// Work is addressed by turn: run and answer return the turn to save, wait reads it,
// and answer/steer/cancel take its ID. Mutations (run/steer/answer/cancel/reset) are
// ct_-only; reads (task/turns/events) accept ct_ OR the session-matching ps_. The
// caller passes the right token.

// AgentRunConstraints bounds a run's token usage and elapsed time. Zero leaves
// the corresponding limit at the Computer's default.
type AgentRunConstraints struct {
	MaxTokens int   `json:"max_tokens,omitempty"`
	MaxWallMs int64 `json:"max_wall_ms,omitempty"`
}

// AgentRunOptions carries caller context, selected skills and supported budgets.
// Secrets is caller-supplied credentials/configuration delivered to the session env
// file ($SESSION_SECRETS), never model-visible text — see the spec's SessionSecrets schema
// for naming rules and lifecycle (ephemeral; re-send after re-attach).
//
// IdempotencyKey names the submission, for example your saved message ID: a run
// repeated with the same key returns the turn the key admitted instead of starting
// another. The coordinator does not compare the request body.
type AgentRunOptions struct {
	Context        string
	Skills         []string
	Constraints    *AgentRunConstraints
	Secrets        map[string]string
	IdempotencyKey string
}

// AgentRun starts a turn and returns it. opts.IdempotencyKey is required: a lost
// response is resent under it, and the resend names the same turn. When no
// response arrives within the resend budget, or ctx ends first, the error is a
// RunUnconfirmedError naming the key to send again.
func (c *Client) AgentRun(ctx context.Context, token, name, goal string, opts AgentRunOptions) (*AgentTurn, error) {
	if opts.IdempotencyKey == "" {
		return nil, fmt.Errorf("pinesandbox: idempotency key is required")
	}
	body := map[string]any{"goal": goal}
	if opts.Context != "" {
		body["context"] = opts.Context
	}
	if opts.Skills != nil {
		body["skills"] = opts.Skills
	}
	if opts.Constraints != nil {
		body["constraints"] = opts.Constraints
	}
	if len(opts.Secrets) > 0 {
		body["secrets"] = opts.Secrets
	}
	headers := map[string]string{"Idempotency-Key": opts.IdempotencyKey}
	turn, err := c.postAgentTurn(ctx, c.agentPath(name, "/run"), token, body, headers, true)
	// A lost response, or ctx ending before one arrived, leaves the run unconfirmed.
	ctxEnded := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	if err != nil && (lostResponse(err) || ctxEnded) {
		return nil, &RunUnconfirmedError{IdempotencyKey: opts.IdempotencyKey, Err: err}
	}
	return turn, err
}

// maxTurnHold bounds one server-side wait. It stays below the transport's 30s
// fallback deadline so a hold never times out a caller without a context deadline.
const maxTurnHold = 25 * time.Second

// AgentWait reads a turn, holding while it is running or canceling. It repeats the
// bounded server wait until the turn leaves those states or timeout elapses, then
// returns the turn as it is; a zero timeout reads once. A read that returns before its
// hold is followed by a pause of up to a second, so the loop never spins. A read whose response was
// lost, or whose problem is marked retryable, is read again within the timeout
// after retryDelay; any other error is returned. It never cancels the turn or
// follows NextTurnID. ctx bounds the whole call.
func (c *Client) AgentWait(ctx context.Context, token, name, turnID string, timeout time.Duration) (*AgentTurn, error) {
	if turnID == "" {
		return nil, fmt.Errorf("pinesandbox: turn ID is required")
	}
	path := c.agentPath(name, "/turns/"+url.PathEscape(turnID))
	deadline := c.clock.Now().Add(timeout)
	until := func() time.Duration { return deadline.Sub(c.clock.Now()) }
	failures := 0
	for {
		hold := max(int(min(until(), maxTurnHold)/time.Second), 0)
		readStarted := c.clock.Now()
		raw, err := c.getJSON(ctx, path+"?wait_seconds="+strconv.Itoa(hold), token)
		if err != nil {
			var apiErr *problem.APIError
			retryable := lostResponse(err) || (errors.As(err, &apiErr) && apiErr.Retryable)
			if !retryable || until() <= 0 {
				return nil, err
			}
			failures++
			if serr := c.clock.Sleep(ctx, min(retryDelay(failures, err), until())); serr != nil {
				return nil, errors.Join(err, serr)
			}
			continue
		}
		failures = 0
		turn, err := parseAgentTurn(raw)
		if err != nil {
			return nil, err
		}
		if (turn.State != "running" && turn.State != "canceling") || until() <= 0 {
			return turn, nil
		}
		// The Computer holds a read for whole seconds. A read that answered sooner than
		// its hold, or a hold of 0 inside the last second, waits up to a second before
		// the next read, so Wait never spins against a server that ignores the hold.
		if hold == 0 || c.clock.Now().Sub(readStarted) < time.Duration(hold)*time.Second {
			if err := c.clock.Sleep(ctx, min(waitPollFloor, until())); err != nil {
				return nil, err
			}
		}
	}
}

// waitPollFloor is the shortest gap between two turn reads that did not hold.
const waitPollFloor = time.Second

// AgentSteerOptions deliver secrets with the steer (same SessionSecrets contract as
// AgentRunOptions.Secrets), only once the steer passes every check.
type AgentSteerOptions struct {
	Secrets map[string]string
}

// AgentSteer adds guidance to a running turn and returns it. It is never retried:
// a conflict at a completion boundary does not prove the text was not delivered.
func (c *Client) AgentSteer(ctx context.Context, token, name, turnID, text string, opts AgentSteerOptions) (*AgentTurn, error) {
	body := map[string]any{"turn_id": turnID, "text": text}
	if len(opts.Secrets) > 0 {
		body["secrets"] = opts.Secrets
	}
	return c.postAgentTurn(ctx, c.agentPath(name, "/steer"), token, body, nil, false)
}

// AgentAnswerOptions deliver secrets with the answer through the protected session
// file, once, before the answer's turn is admitted: a failed delivery leaves the
// question open.
type AgentAnswerOptions struct {
	Secrets map[string]string
}

// AgentAnswer answers the question turnID asked and returns the turn the answer
// opened. Repeating the same answer returns that same turn, so a lost response is
// resent; a different answer to an answered turn is a conflict.
func (c *Client) AgentAnswer(ctx context.Context, token, name, turnID, answer string, opts AgentAnswerOptions) (*AgentTurn, error) {
	body := map[string]any{"turn_id": turnID, "answer": answer}
	if len(opts.Secrets) > 0 {
		body["secrets"] = opts.Secrets
	}
	return c.postAgentTurn(ctx, c.agentPath(name, "/answer"), token, body, nil, true)
}

// AgentCancel cancels turnID while it runs and returns the turn. A waiting
// question is closed: its turn ends canceled and can no longer be answered. An
// answered turn is a 409 /errors/stale-turn: cancel the turn its answer opened.
// Any other ended turn is returned unchanged, so a lost response is resent.
func (c *Client) AgentCancel(ctx context.Context, token, name, turnID string) (*AgentTurn, error) {
	return c.postAgentTurn(ctx, c.agentPath(name, "/cancel"), token, map[string]any{"turn_id": turnID}, nil, true)
}

// AgentReset clears the session's persistent agent thread (memory). Returns the Task.
func (c *Client) AgentReset(ctx context.Context, token, name string, opts AgentTaskOptions) (*AgentTask, error) {
	if opts.ExpectedTaskID == "" {
		return nil, fmt.Errorf("pinesandbox: expected task ID is required to reset memory")
	}
	raw, err := c.postJSON(ctx, c.guardedAgentPath(name, "/reset", opts.ExpectedTaskID), token, map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseAgentTask(raw)
}

// AgentTaskOptions binds a read or memory reset to the Task the caller observed.
type AgentTaskOptions struct {
	ExpectedTaskID string
}

// AgentTask returns the session's Task and its latest turn.
func (c *Client) AgentTask(ctx context.Context, token, name string, opts AgentTaskOptions) (*AgentTask, error) {
	raw, err := c.getJSON(ctx, c.guardedAgentPath(name, "", opts.ExpectedTaskID), token)
	if err != nil {
		return nil, err
	}
	return parseAgentTask(raw)
}

// AgentTurns lists the retained turns, oldest first.
func (c *Client) AgentTurns(ctx context.Context, token, name string, opts AgentTaskOptions) ([]AgentTurn, error) {
	raw, err := c.getJSON(ctx, c.guardedAgentPath(name, "/turns", opts.ExpectedTaskID), token)
	if err != nil {
		return nil, err
	}
	var w struct {
		Turns []json.RawMessage `json:"turns"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: decode agent turns: %w", err)
	}
	turns := make([]AgentTurn, 0, len(w.Turns))
	for _, item := range w.Turns {
		turn, err := parseAgentTurn(item)
		if err != nil {
			return nil, err
		}
		turns = append(turns, *turn)
	}
	return turns, nil
}

// AgentEventsPageOptions select a page of retained public activity.
type AgentEventsPageOptions struct {
	// After returns events with an event ID greater than this (0 = from the start).
	After int64
	// Limit bounds the history records examined (0 = the Computer's default, 200;
	// at most 500), not the events returned.
	Limit int
	// ExpectedTaskID binds the cursor to the Task it was read from.
	ExpectedTaskID string
	// SkipFrames leaves frame events out; their IDs still advance NextAfter.
	SkipFrames bool
}

// AgentEventsPage is one page of retained public activity. Advance with NextAfter until
// it reaches LatestEventID: an empty or short page does not mean history is exhausted.
type AgentEventsPage struct {
	Events        []AgentEvent
	NextAfter     int64
	LatestEventID int64
	Raw           json.RawMessage
}

// AgentEventsPage reads one page of the session's retained public activity by cursor.
func (c *Client) AgentEventsPage(ctx context.Context, token, name string, opts AgentEventsPageOptions) (*AgentEventsPage, error) {
	q := url.Values{}
	if opts.After > 0 {
		q.Set("after", strconv.FormatInt(opts.After, 10))
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.ExpectedTaskID != "" {
		q.Set("expected_task_id", opts.ExpectedTaskID)
	}
	if opts.SkipFrames {
		q.Set("frames", "none")
	}
	path := c.agentPath(name, "/events/page")
	if len(q) != 0 {
		path += "?" + q.Encode()
	}
	raw, err := c.getJSON(ctx, path, token)
	if err != nil {
		return nil, err
	}
	var w struct {
		Events        []json.RawMessage `json:"events"`
		NextAfter     int64             `json:"next_after"`
		LatestEventID int64             `json:"latest_event_id"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: decode agent events page: %w", err)
	}
	page := &AgentEventsPage{NextAfter: w.NextAfter, LatestEventID: w.LatestEventID, Raw: raw,
		Events: make([]AgentEvent, 0, len(w.Events))}
	for _, item := range w.Events {
		// One undecodable event costs only itself, as on the stream.
		if ev, ok := decodeAgentEvent(sse.Frame{Data: string(item)}); ok {
			page.Events = append(page.Events, ev)
		}
	}
	return page, nil
}

func (c *Client) guardedAgentPath(name, suffix, taskID string) string {
	path := c.agentPath(name, suffix)
	if taskID != "" {
		path += "?" + url.Values{"expected_task_id": {taskID}}.Encode()
	}
	return path
}

// resendBudget bounds how long agent work whose response was lost keeps being
// sent again, measured from the first send: long enough to outlast a coordinator
// restart. It counts every wait, the transport's retries inside a send included
// (x-pine-retry.client.lost_response). ctx can end it sooner.
const resendBudget = 30 * time.Second

// retryDelay is the wait before retry attempt after err: growing from a second
// to at most five, and never shorter than the response's Retry-After.
func retryDelay(attempt int, err error) time.Duration {
	var retryAfter time.Duration
	if apiErr := (*problem.APIError)(nil); errors.As(err, &apiErr) {
		retryAfter = apiErr.RetryAfter
	}
	return max(min(time.Second<<min(attempt-1, 3), 5*time.Second), retryAfter)
}

// postAgentTurn POSTs body and decodes the turn every agent work mutation returns.
// Set resend only when sending the request again names the same turn: a run with
// its key, an answer, a cancel — the operations the contract marks replay-safe. A
// transient fault is then retried at once by the transport, and a lost response is
// sent again under the resend budget.
func (c *Client) postAgentTurn(ctx context.Context, path, token string, body any, headers map[string]string, resend bool) (*AgentTurn, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal request body: %w", err)
	}
	started := c.clock.Now()
	for attempt := 1; ; attempt++ {
		resp, err := c.send(ctx, coordReq{method: "POST", path: path, token: token, body: b,
			contentType: "application/json", headers: headers, retryTransient: resend})
		if err == nil {
			return parseAgentTurn(resp.Body)
		}
		if !resend || !lostResponse(err) {
			return nil, err
		}
		wait := retryDelay(attempt, err)
		if c.clock.Now().Sub(started)+wait > resendBudget {
			return nil, err
		}
		if serr := c.clock.Sleep(ctx, wait); serr != nil {
			// The response is still lost; keep it beside ctx's error.
			return nil, errors.Join(err, serr)
		}
	}
}

// lostResponse reports whether a request may have reached the Computer without
// its response reaching the caller: a network fault, a request timeout, or a
// gateway 502, 503 or 504, which cannot say whether the Computer acted.
func lostResponse(err error) bool {
	var conn *transport.ConnectionError
	var timeout *transport.TimeoutError
	var apiErr *problem.APIError
	switch {
	case errors.As(err, &conn), errors.As(err, &timeout):
		return true
	case errors.As(err, &apiErr):
		return apiErr.Status == http.StatusRequestTimeout || apiErr.Status == http.StatusBadGateway ||
			apiErr.Status == http.StatusServiceUnavailable || apiErr.Status == http.StatusGatewayTimeout
	default:
		return false
	}
}

// AgentEvents is a typed, resuming iterator — see stream.go.

// ---- drive track (BYOA primitives) ----

// Observe captures the session's current perception (the ObservationSnapshot).
func (c *Client) Observe(ctx context.Context, token, name string) (*Observation, error) {
	raw, err := c.postJSON(ctx, "/v1/sessions/"+url.PathEscape(name)+"/observe", token, map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseObservation(raw)
}

// ComputerUseResult is the typed outcome of one computer-use action. For
// action=="screenshot" Screenshot holds the base64 PNG; other actions return
// OK==true. It models the full result shape, so there's no raw escape hatch.
type ComputerUseResult struct {
	Screenshot string // base64 PNG, set for action=="screenshot"
	OK         bool   // true when a non-screenshot action completed
}

// ComputerUse issues one low-level action (the body is {action, ...params}). The action
// verb always wins: a params key named "action" can't clobber it (params carry the action's
// arguments — x/y/text/etc. — not the verb).
func (c *Client) ComputerUse(ctx context.Context, token, name, action string, params map[string]any) (*ComputerUseResult, error) {
	body := make(map[string]any, len(params)+1)
	for k, v := range params {
		body[k] = v
	}
	body["action"] = action
	raw, err := c.postJSON(ctx, "/v1/sessions/"+url.PathEscape(name)+"/computer-use", token, body)
	if err != nil {
		return nil, err
	}
	var w struct {
		Screenshot string `json:"screenshot"`
		OK         bool   `json:"ok"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable computer-use result: %w", err)
	}
	return &ComputerUseResult{Screenshot: w.Screenshot, OK: w.OK}, nil
}

// UploadFile stages a file into a selector's picker.
func (c *Client) UploadFile(ctx context.Context, token, name, selector, file string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/v1/sessions/"+url.PathEscape(name)+"/upload_file", token, map[string]any{"selector": selector, "file": file})
}

// ---- helpers ----

func (c *Client) agentPath(name, suffix string) string {
	return "/v2/sessions/" + url.PathEscape(name) + "/agent" + suffix
}

func (c *Client) postJSON(ctx context.Context, path, token string, body any) (json.RawMessage, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal request body: %w", err)
	}
	resp, err := c.do(ctx, "POST", path, token, b)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(resp.Body), nil
}

func (c *Client) getJSON(ctx context.Context, path, token string) (json.RawMessage, error) {
	resp, err := c.do(ctx, "GET", path, token, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(resp.Body), nil
}

// ErrStop, when returned from an AuthorEvents callback, stops the stream; AuthorEvents
// returns it unchanged so the caller can distinguish a deliberate stop (errors.Is) from a
// real failure. (The agent/control feeds are iterators — they stop on a range break.)
var ErrStop = errors.New("pinesandbox: stream stopped by caller")
