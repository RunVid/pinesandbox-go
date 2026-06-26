package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"go.pinesandbox.io/computer/internal/base/sse"
)

// ---- agent track (/v1/sessions/{name}/agent[/*]) ----
// Mutations (run/steer/answer/cancel/reset) are ct_-only; reads (task/result/events)
// accept ct_ OR the session-matching ps_. The caller passes the right token.

// AgentRunOptions are the optional run knobs. Context/Constraints are arbitrary JSON
// (object or string); Skills is a name list. A nil field is omitted from the body.
type AgentRunOptions struct {
	Context     any
	Skills      []string
	Constraints any
}

// AgentRun starts a turn (delegate mode — one persistent Task per session). Returns the
// session's Task, `running` for the duration of this turn.
func (c *Client) AgentRun(ctx context.Context, token, name, goal string, opts AgentRunOptions) (*AgentTask, error) {
	body := map[string]any{"goal": goal}
	if opts.Context != nil {
		body["context"] = opts.Context
	}
	if opts.Skills != nil {
		body["skills"] = opts.Skills
	}
	if opts.Constraints != nil {
		body["constraints"] = opts.Constraints
	}
	return c.postAgentTask(ctx, c.agentPath(name, "/run"), token, body)
}

// AgentSteerOptions optionally pin the steer to a turn (concurrency guard).
type AgentSteerOptions struct {
	ExpectedTurnID string // omitted when empty
	TurnAttempt    *int   // omitted when nil
}

// AgentSteer injects guidance into the running turn. Returns the updated Task.
func (c *Client) AgentSteer(ctx context.Context, token, name, text string, opts AgentSteerOptions) (*AgentTask, error) {
	body := map[string]any{"text": text}
	if opts.ExpectedTurnID != "" {
		body["expected_turn_id"] = opts.ExpectedTurnID
	}
	if opts.TurnAttempt != nil {
		body["turn_attempt"] = *opts.TurnAttempt
	}
	return c.postAgentTask(ctx, c.agentPath(name, "/steer"), token, body)
}

// AgentAnswer responds to an agent's clarifying question. expectedTurnID is optional ("").
// Returns the updated Task.
func (c *Client) AgentAnswer(ctx context.Context, token, name, requestID, answer, expectedTurnID string) (*AgentTask, error) {
	body := map[string]any{"request_id": requestID, "answer": answer}
	if expectedTurnID != "" {
		body["expected_turn_id"] = expectedTurnID
	}
	return c.postAgentTask(ctx, c.agentPath(name, "/answer"), token, body)
}

// AgentCancel cancels the running turn. Returns the updated Task.
func (c *Client) AgentCancel(ctx context.Context, token, name string) (*AgentTask, error) {
	return c.postAgentTask(ctx, c.agentPath(name, "/cancel"), token, map[string]any{})
}

// AgentReset clears the session's persistent agent thread (memory). Returns the updated Task.
func (c *Client) AgentReset(ctx context.Context, token, name string) (*AgentTask, error) {
	return c.postAgentTask(ctx, c.agentPath(name, "/reset"), token, map[string]any{})
}

// AgentTask returns the session's current Task (state/goal/usage/turn ids).
func (c *Client) AgentTask(ctx context.Context, token, name string) (*AgentTask, error) {
	raw, err := c.getJSON(ctx, c.agentPath(name, ""), token)
	if err != nil {
		return nil, err
	}
	return parseAgentTask(raw)
}

// AgentResult returns the latest finished turn's result.
func (c *Client) AgentResult(ctx context.Context, token, name string) (*AgentResult, error) {
	raw, err := c.getJSON(ctx, c.agentPath(name, "/result"), token)
	if err != nil {
		return nil, err
	}
	return parseAgentResult(raw)
}

// postAgentTask POSTs body and decodes the Task response shared by all agent mutations.
func (c *Client) postAgentTask(ctx context.Context, path, token string, body any) (*AgentTask, error) {
	raw, err := c.postJSON(ctx, path, token, body)
	if err != nil {
		return nil, err
	}
	return parseAgentTask(raw)
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
	return "/v1/sessions/" + url.PathEscape(name) + "/agent" + suffix
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

// streamJSONEvents is the shared driver for the agent (and, later, author) event feeds:
// open the streaming GET with Last-Event-ID, parse SSE frames, skip empty-data frames,
// track the id cursor, and hand each frame's data JSON to fn.
func (c *Client) streamJSONEvents(ctx context.Context, path, token, lastEventID string, fn func(data []byte) error) (string, error) {
	var extra map[string]string
	if lastEventID != "" {
		extra = map[string]string{"Last-Event-ID": lastEventID}
	}
	sr, err := c.openSSE(ctx, "GET", path, token, nil, extra)
	if err != nil {
		return lastEventID, err
	}
	defer sr.Body.Close()

	latest := lastEventID
	for frame, ferr := range sse.Frames(sr.Body) {
		if ferr != nil {
			return latest, ferr // a read error (NOT a clean EOF — Frames just stops on EOF)
		}
		if frame.ID != "" {
			// Advance the resume cursor for ANY id-bearing frame, BEFORE the empty-data
			// skip — mirrors pumpFrames (stream.go) so a reconnect can't replay a frame
			// we've already seen and skipped (an id-bearing keepalive).
			latest = frame.ID
		}
		if frame.Data == "" {
			continue
		}
		if err := fn([]byte(frame.Data)); err != nil {
			return latest, err
		}
	}
	return latest, nil
}

// ErrStop, when returned from an AuthorEvents callback, stops the stream; streamJSONEvents
// returns it unchanged so the caller can distinguish a deliberate stop (errors.Is) from a
// real failure. (The agent/control feeds are iterators now — they stop on a range break.)
var ErrStop = errors.New("pinesandbox: stream stopped by caller")
