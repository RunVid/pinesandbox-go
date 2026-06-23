package pinesandbox

import (
	"context"
	"encoding/json"

	"go.pinesandbox.io/computer/internal/coordinator"
)

// Control / tab / desktop-token types (re-exported from the coordinator layer).
type (
	ControlState        = coordinator.ControlState
	DesktopToken        = coordinator.DesktopToken
	PatchControlOptions = coordinator.PatchControlOptions
	PatchTabOptions     = coordinator.PatchTabOptions
)

// ---- tabs (ps_) ----

// ListTabs returns the session's tabs (raw array).
func (s *Session) ListTabs(ctx context.Context) (json.RawMessage, error) {
	return s.coord.ListTabs(ctx, s.token, s.name)
}

// CreateTab opens a tab at url (raw tab).
func (s *Session) CreateTab(ctx context.Context, url, label string) (json.RawMessage, error) {
	return s.coord.CreateTab(ctx, s.token, s.name, url, label)
}

// PatchTab updates a tab (raw tab).
func (s *Session) PatchTab(ctx context.Context, targetID string, opts PatchTabOptions) (json.RawMessage, error) {
	return s.coord.PatchTab(ctx, s.token, s.name, targetID, opts)
}

// CloseTab closes a tab.
func (s *Session) CloseTab(ctx context.Context, targetID string) error {
	return s.coord.CloseTab(ctx, s.token, s.name, targetID)
}

// ---- control state ----

// ControlState returns the session's control state + ETag (use the ETag as the next
// UpdateControl If-Match).
func (s *Session) ControlState(ctx context.Context) (*ControlState, error) {
	return s.coord.GetControl(ctx, s.token, s.name)
}

// UpdateControl applies a control patch. opts.IfMatch is required unless opts.Force; an
// empty opts.IdempotencyKey is auto-filled with a fresh UUID.
func (s *Session) UpdateControl(ctx context.Context, body any, opts PatchControlOptions) (*ControlState, error) {
	if opts.IdempotencyKey == "" {
		key, err := uuidV7()
		if err != nil {
			return nil, err
		}
		opts.IdempotencyKey = key
	}
	return s.coord.PatchControl(ctx, s.token, s.name, body, opts)
}

// NotifyHuman posts a control notification (If-Match required).
func (s *Session) NotifyHuman(ctx context.Context, reason, detail, ifMatch string) error {
	return s.coord.ControlNotify(ctx, s.token, s.name, reason, detail, ifMatch)
}

// DesktopToken mints a short-TTL browser-safe VNC desktop-stream token (dt_). ct_-only.
func (s *Session) DesktopToken(ctx context.Context) (*DesktopToken, error) {
	return s.coord.MintDesktopToken(ctx, s.computerToken(), s.name)
}

// ---- handoffs + control events (ps_) ----

// ListHandoffs lists control handoffs (raw array). limit<=0 / before="" omit the bounds.
func (s *Session) ListHandoffs(ctx context.Context, limit int, before string) (json.RawMessage, error) {
	return s.coord.ListHandoffs(ctx, s.token, s.name, limit, before)
}

// GetHandoff returns one handoff (raw).
func (s *Session) GetHandoff(ctx context.Context, handoffID string) (json.RawMessage, error) {
	return s.coord.GetHandoff(ctx, s.token, s.name, handoffID)
}

// ControlEvents streams the session's control event feed; fn receives each event's raw data
// JSON. Returns the resume cursor; fn returning ErrStop stops cleanly.
func (s *Session) ControlEvents(ctx context.Context, lastEventID string, fn func(data []byte) error) (string, error) {
	return s.coord.ControlEvents(ctx, s.token, s.name, lastEventID, fn)
}
