package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// ---- tabs ----

// ListTabs returns the session's tabs.
func (c *Client) ListTabs(ctx context.Context, token, name string) ([]Tab, error) {
	raw, err := c.envelopeField(ctx, "GET", "/sessions/"+url.PathEscape(name)+"/tabs", token, nil, "tabs")
	if err != nil {
		return nil, err
	}
	return parseTabs(raw)
}

// CreateTab opens a tab at url and returns it.
func (c *Client) CreateTab(ctx context.Context, token, name, tabURL, label string) (*Tab, error) {
	body := map[string]any{"url": tabURL}
	if label != "" {
		body["label"] = label
	}
	raw, err := c.postEnvelopeField(ctx, "/sessions/"+url.PathEscape(name)+"/tabs", token, body, "tab")
	if err != nil {
		return nil, err
	}
	return parseTab(raw)
}

// PatchTabOptions are the tab fields to change (nil = leave unchanged).
type PatchTabOptions struct {
	Active *bool
	Label  *string
}

// PatchTab updates a tab and returns it.
func (c *Client) PatchTab(ctx context.Context, token, name, targetID string, opts PatchTabOptions) (*Tab, error) {
	body := map[string]any{}
	if opts.Active != nil {
		body["active"] = *opts.Active
	}
	if opts.Label != nil {
		body["label"] = *opts.Label
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal patch-tab: %w", err)
	}
	resp, err := c.do(ctx, "PATCH", "/sessions/"+url.PathEscape(name)+"/tabs/"+url.PathEscape(targetID), token, b)
	if err != nil {
		return nil, err
	}
	raw, err := extractField(resp.Body, "tab")
	if err != nil {
		return nil, err
	}
	return parseTab(raw)
}

// CloseTab closes a tab.
func (c *Client) CloseTab(ctx context.Context, token, name, targetID string) error {
	_, err := c.do(ctx, "DELETE", "/sessions/"+url.PathEscape(name)+"/tabs/"+url.PathEscape(targetID), token, nil)
	return err
}

// ---- control state (v1) ----

// ControlState is the session's typed control document (spec ControlState) + its
// ETag (use as If-Match on the next update). It models the full schema, so there's
// no raw escape hatch — a new spec field is added here.
type ControlState struct {
	Controller       string     // who drives: "human" | "agent" | "locked"
	Epoch            int64      // monotonic computer-wide transition counter
	SessionName      string     // the session this control state belongs to
	IdlePaused       bool       // true iff the idle timer is currently pinned/paused
	IdleDeadline     *time.Time // absolute idle deadline; nil if not human / none set
	LastTransitionAt *time.Time // when control last changed; nil if absent
	ETag             string     // pass as PatchControlOptions.IfMatch on the next update
}

// parseControlState parses a control body + ETag into a typed ControlState.
func parseControlState(body []byte, etag string) (*ControlState, error) {
	var w struct {
		Controller       string  `json:"controller"`
		Epoch            int64   `json:"epoch"`
		SessionName      string  `json:"session_name"`
		IdlePaused       bool    `json:"idle_paused"`
		IdleDeadline     *string `json:"idle_deadline"`
		LastTransitionAt *string `json:"last_transition_at"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable control state: %w", err)
	}
	return &ControlState{
		Controller:       w.Controller,
		Epoch:            w.Epoch,
		SessionName:      w.SessionName,
		IdlePaused:       w.IdlePaused,
		IdleDeadline:     parseTimePtr(w.IdleDeadline),
		LastTransitionAt: parseTimePtr(w.LastTransitionAt),
		ETag:             etag,
	}, nil
}

// parseTimePtr parses an RFC-3339 timestamp pointer; a nil/empty/garbled value
// yields nil (a bad timestamp never fails the whole parse).
func parseTimePtr(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil
	}
	return &t
}

// GetControl returns the session's typed control state + ETag.
func (c *Client) GetControl(ctx context.Context, token, name string) (*ControlState, error) {
	resp, err := c.do(ctx, "GET", "/v1/sessions/"+url.PathEscape(name)+"/control", token, nil)
	if err != nil {
		return nil, err
	}
	return parseControlState(resp.Body, resp.Headers.Get("ETag"))
}

// ControlPatch is the typed body of a control update. Pointer fields are PATCH
// semantics — leave nil to keep the current value. ActorType is required by the
// coord ("user_click" for normal transitions; "admin_override" pairs with Force).
type ControlPatch struct {
	Controller   *string    // "human" | "agent"
	IdlePaused   *bool      // pin/unpin the idle timer
	IdleDeadline *time.Time // set an ABSOLUTE idle deadline (RFC3339 on the wire)
	// IdleDeadlineIn sets a RELATIVE idle deadline (spec: "+15m"); the coord always
	// commits it. Wins over IdleDeadline if both are set. Sent as "+<seconds>s", so
	// it must be a POSITIVE duration — a non-positive value is ignored (the SDK won't
	// emit a malformed "+-Ns" the coord would 400).
	IdleDeadlineIn *time.Duration
	ActorType      string // REQUIRED: "user_click" | "admin_override"
}

// toWire builds the PATCH body, omitting unset (nil) fields.
func (p ControlPatch) toWire() map[string]any {
	body := map[string]any{}
	if p.ActorType != "" {
		body["actor_type"] = p.ActorType
	}
	if p.Controller != nil {
		body["controller"] = *p.Controller
	}
	if p.IdlePaused != nil {
		body["idle_paused"] = *p.IdlePaused
	}
	switch {
	case p.IdleDeadlineIn != nil && *p.IdleDeadlineIn > 0:
		body["idle_deadline"] = fmt.Sprintf("+%ds", int64(p.IdleDeadlineIn.Seconds()))
	case p.IdleDeadline != nil:
		body["idle_deadline"] = p.IdleDeadline.UTC().Format(time.RFC3339)
	}
	return body
}

// PatchControlOptions configure UpdateControl. IfMatch is required unless Force.
type PatchControlOptions struct {
	IfMatch        string
	Force          bool
	IdempotencyKey string
}

// PatchControl applies a typed control patch and returns the new state + ETag.
func (c *Client) PatchControl(ctx context.Context, token, name string, patch ControlPatch, opts PatchControlOptions) (*ControlState, error) {
	b, err := json.Marshal(patch.toWire())
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal control patch: %w", err)
	}
	path := "/v1/sessions/" + url.PathEscape(name) + "/control"
	if opts.Force {
		path += "?force=true"
	}
	headers := map[string]string{}
	if opts.IfMatch != "" {
		headers["If-Match"] = opts.IfMatch
	}
	if opts.IdempotencyKey != "" {
		headers["Idempotency-Key"] = opts.IdempotencyKey
	}
	resp, err := c.send(ctx, coordReq{method: "PATCH", path: path, token: token, body: b, contentType: "application/json", headers: headers})
	if err != nil {
		return nil, err
	}
	return parseControlState(resp.Body, resp.Headers.Get("ETag"))
}

// ControlNotify posts a control notification (If-Match required).
func (c *Client) ControlNotify(ctx context.Context, token, name, reason, detail, ifMatch string) error {
	body := map[string]any{"reason": reason}
	if detail != "" {
		body["detail"] = detail
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("pinesandbox: marshal control notify: %w", err)
	}
	headers := map[string]string{}
	if ifMatch != "" {
		headers["If-Match"] = ifMatch
	}
	_, err = c.send(ctx, coordReq{method: "POST", path: "/v1/sessions/" + url.PathEscape(name) + "/control/notify", token: token, body: b, contentType: "application/json", headers: headers})
	return err
}

// DesktopToken is a short-TTL browser-safe VNC desktop-stream token (dt_).
type DesktopToken struct {
	Token     string
	ExpiresAt string
}

// MintDesktopToken mints a dt_ for the session (ct_-only).
func (c *Client) MintDesktopToken(ctx context.Context, token, name string) (*DesktopToken, error) {
	resp, err := c.do(ctx, "POST", "/v1/sessions/"+url.PathEscape(name)+"/desktop-token", token, emptyJSON)
	if err != nil {
		return nil, err
	}
	var w struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(resp.Body, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable desktop token: %w", err)
	}
	return &DesktopToken{Token: w.Token, ExpiresAt: w.ExpiresAt}, nil
}

// ---- handoffs ----

// Handoff is one past HUMAN turn. The stable SUMMARY fields are always typed. The
// deep forensic detail (nav / form_submit / xhr_submit / clicked_action arrays —
// loose and evolving) is in Raw ONLY for GetHandoff; ListHandoffs returns
// summaries, so a list item's Raw is the summary object, not the forensic body.
type Handoff struct {
	HandoffID         string    // "<session>:<release_epoch>"
	StartedAt         time.Time // best-effort; zero if absent/malformed
	EndedAt           time.Time // best-effort; zero if absent/malformed
	ControllerAtStart string    // "human" | "agent" | "locked"
	ControllerAtEnd   string
	Raw               json.RawMessage // summary (list) or full forensic body (get)
}

// HandoffList is a page of handoffs + its pagination cursor. Pass NextBefore as
// the next ListHandoffs `before` to walk older pages; "" means no more pages.
type HandoffList struct {
	Handoffs   []Handoff
	NextBefore string
}

func parseHandoff(raw json.RawMessage) (*Handoff, error) {
	var w struct {
		HandoffID         string `json:"handoff_id"`
		StartedAt         string `json:"started_at"`
		EndedAt           string `json:"ended_at"`
		ControllerAtStart string `json:"controller_at_start"`
		ControllerAtEnd   string `json:"controller_at_end"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable handoff: %w", err)
	}
	// Timestamps are best-effort: a bad/absent value zeroes that field but never
	// fails the whole call — one drifted handoff must not break a list fetch (the
	// raw body is always in .Raw). StartedAt.IsZero() flags a missing/bad value.
	h := &Handoff{
		HandoffID: w.HandoffID, ControllerAtStart: w.ControllerAtStart,
		ControllerAtEnd: w.ControllerAtEnd, Raw: raw,
	}
	if t := parseTimePtr(&w.StartedAt); t != nil {
		h.StartedAt = *t
	}
	if t := parseTimePtr(&w.EndedAt); t != nil {
		h.EndedAt = *t
	}
	return h, nil
}

// ListHandoffs returns a page of the session's control handoffs (summary-typed)
// plus the pagination cursor (HandoffList.NextBefore — pass it back as `before`
// to page older). limit<=0 / before="" omit the bounds.
func (c *Client) ListHandoffs(ctx context.Context, token, name string, limit int, before string) (*HandoffList, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	if before != "" {
		q.Set("before", before)
	}
	path := "/v1/sessions/" + url.PathEscape(name) + "/handoffs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	resp, err := c.do(ctx, "GET", path, token, nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Handoffs   []json.RawMessage `json:"handoffs"`
		NextBefore string            `json:"next_before"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable handoffs response: %w", err)
	}
	out := &HandoffList{NextBefore: env.NextBefore, Handoffs: make([]Handoff, 0, len(env.Handoffs))}
	for _, it := range env.Handoffs {
		h, err := parseHandoff(it)
		if err != nil {
			return nil, err
		}
		out.Handoffs = append(out.Handoffs, *h)
	}
	return out, nil
}

// GetHandoff returns one handoff (summary-typed; full detail in Raw).
func (c *Client) GetHandoff(ctx context.Context, token, name, handoffID string) (*Handoff, error) {
	resp, err := c.do(ctx, "GET", "/v1/sessions/"+url.PathEscape(name)+"/handoffs/"+url.PathEscape(handoffID), token, nil)
	if err != nil {
		return nil, err
	}
	return parseHandoff(json.RawMessage(resp.Body))
}

// ControlEvents is a typed, resuming iterator — see stream.go.

// ---- helpers ----

func (c *Client) envelopeField(ctx context.Context, method, path, token string, body []byte, field string) (json.RawMessage, error) {
	resp, err := c.do(ctx, method, path, token, body)
	if err != nil {
		return nil, err
	}
	return extractField(resp.Body, field)
}

func (c *Client) postEnvelopeField(ctx context.Context, path, token string, body any, field string) (json.RawMessage, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal body: %w", err)
	}
	return c.envelopeField(ctx, "POST", path, token, b, field)
}

func extractField(body []byte, field string) (json.RawMessage, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable response: %w", err)
	}
	v, ok := env[field]
	if !ok {
		return nil, fmt.Errorf("pinesandbox: response missing %q field", field)
	}
	return v, nil
}
