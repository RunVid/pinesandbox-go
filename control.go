package pinesandbox

import (
	"context"
	"encoding/json"
	"iter"

	"go.pinesandbox.io/computer/internal/coordinator"
)

// Control / tab / desktop-token types (re-exported from the coordinator layer).
type (
	ControlState        = coordinator.ControlState
	DesktopToken        = coordinator.DesktopToken
	PatchControlOptions = coordinator.PatchControlOptions
	PatchTabOptions     = coordinator.PatchTabOptions
	Tab                 = coordinator.Tab
	ControlEvent        = coordinator.ControlEvent
)

// ---- tabs (ps_) ----

// ListTabs returns the session's tabs.
func (s *Session) ListTabs(ctx context.Context) ([]Tab, error) {
	return s.coord.ListTabs(ctx, s.token, s.name)
}

// CreateTab opens a tab at url and returns it.
func (s *Session) CreateTab(ctx context.Context, url, label string) (*Tab, error) {
	return s.coord.CreateTab(ctx, s.token, s.name, url, label)
}

// PatchTab updates a tab and returns it.
func (s *Session) PatchTab(ctx context.Context, targetID string, opts PatchTabOptions) (*Tab, error) {
	return s.coord.PatchTab(ctx, s.token, s.name, targetID, opts)
}

// CloseTab closes a tab.
func (s *Session) CloseTab(ctx context.Context, targetID string) error {
	return s.coord.CloseTab(ctx, s.token, s.name, targetID)
}

// ---- control lease (ct_) ----
//
// Control is operator lease management: the coord makes PATCH /control ct_-only
// (ps_ → 403) and the reads accept ct_ OR ps_, so the whole surface routes through
// the Computer's ct_ — keeping it uniform and usable on any handle.

// ControlState returns the session's control state + ETag (use the ETag as the next
// UpdateControl If-Match).
func (s *Session) ControlState(ctx context.Context) (*ControlState, error) {
	return s.coord.GetControl(ctx, s.computerToken(), s.name)
}

// UpdateControl applies a control patch (take/release control). opts.IfMatch is
// required unless opts.Force; an empty opts.IdempotencyKey is auto-filled with a fresh UUID.
func (s *Session) UpdateControl(ctx context.Context, body any, opts PatchControlOptions) (*ControlState, error) {
	if opts.IdempotencyKey == "" {
		key, err := uuidV7()
		if err != nil {
			return nil, err
		}
		opts.IdempotencyKey = key
	}
	return s.coord.PatchControl(ctx, s.computerToken(), s.name, body, opts)
}

// NotifyHuman posts a control notification (If-Match required).
func (s *Session) NotifyHuman(ctx context.Context, reason, detail, ifMatch string) error {
	return s.coord.ControlNotify(ctx, s.token, s.name, reason, detail, ifMatch)
}

// DesktopToken mints a short-TTL browser-safe VNC desktop-stream token (dt_). ct_-only.
func (s *Session) DesktopToken(ctx context.Context) (*DesktopToken, error) {
	return s.coord.MintDesktopToken(ctx, s.computerToken(), s.name)
}

// ---- handoffs + control events (ct_) ----

// ListHandoffs lists control handoffs (raw array). limit<=0 / before="" omit the bounds.
func (s *Session) ListHandoffs(ctx context.Context, limit int, before string) (json.RawMessage, error) {
	return s.coord.ListHandoffs(ctx, s.computerToken(), s.name, limit, before)
}

// GetHandoff returns one handoff (raw).
func (s *Session) GetHandoff(ctx context.Context, handoffID string) (json.RawMessage, error) {
	return s.coord.GetHandoff(ctx, s.computerToken(), s.name, handoffID)
}

// ControlEvents streams the session's control-plane event feed (controller_changed /
// idle_changed / handoff_*) as a typed, resuming iterator — same shape as AgentMode.Events.
// Each ControlEvent's Type is the discriminator; Data holds the raw per-type payload. Pass
// lastEventID to resume (""=from now); break to stop; cancel ctx to end cleanly.
func (s *Session) ControlEvents(ctx context.Context, lastEventID string) iter.Seq2[ControlEvent, error] {
	return s.coord.ControlEvents(ctx, s.computerToken(), s.name, lastEventID)
}
