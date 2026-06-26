package pinesandbox

import (
	"context"
	"errors"
	"iter"

	"go.pinesandbox.io/computer/internal/coordinator"
)

// Control / tab / desktop-token types (re-exported from the coordinator layer).
type (
	ControlState        = coordinator.ControlState
	ControlPatch        = coordinator.ControlPatch
	Handoff             = coordinator.Handoff
	HandoffList         = coordinator.HandoffList
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

// UpdateControl applies a typed control patch (take/release control, pin idle).
// opts.IfMatch is required unless opts.Force; an empty opts.IdempotencyKey is
// auto-filled with a fresh UUID. For the common take/release cases prefer
// TakeControl / ReleaseControl, which handle the ETag fetch + If-Match retry.
func (s *Session) UpdateControl(ctx context.Context, patch ControlPatch, opts PatchControlOptions) (*ControlState, error) {
	if opts.IdempotencyKey == "" {
		key, err := uuidV7()
		if err != nil {
			return nil, err
		}
		opts.IdempotencyKey = key
	}
	return s.coord.PatchControl(ctx, s.computerToken(), s.name, patch, opts)
}

// ControlOption configures TakeControl / ReleaseControl.
type ControlOption func(*controlOpts)

type controlOpts struct {
	actorType string
}

// WithForce takes/releases control BY FORCE — records actor_type "admin_override",
// which the coord pairs with force=true to bypass If-Match. Use it to wrest control
// regardless of who currently holds it. force is DERIVED from actor_type (force ⇔
// admin_override), so a later WithActor() can't leave the two inconsistent.
func WithForce() ControlOption {
	return func(o *controlOpts) { o.actorType = ActorAdminOverride }
}

// WithActor overrides the actor_type recorded for the transition (default
// ActorUserClick).
func WithActor(actorType string) ControlOption {
	return func(o *controlOpts) { o.actorType = actorType }
}

// TakeControl transitions the session to human control. The common case takes no
// options (actor_type user_click); pass WithForce() to override an existing holder.
// It reads the ETag, PATCHes with If-Match, and retries once on a concurrent-
// transition 412 (a fresh Idempotency-Key per attempt).
func (s *Session) TakeControl(ctx context.Context, opts ...ControlOption) (*ControlState, error) {
	return s.setController(ctx, ControllerHuman, opts)
}

// ReleaseControl returns the session to agent control (mirror of TakeControl).
func (s *Session) ReleaseControl(ctx context.Context, opts ...ControlOption) (*ControlState, error) {
	return s.setController(ctx, ControllerAgent, opts)
}

// setController is the ETag-fetch → If-Match patch → 412-retry helper behind
// TakeControl/ReleaseControl. The 412 retry mints a FRESH Idempotency-Key (each
// UpdateControl call auto-fills one) — the retry is a distinct request, so it must
// NOT reuse the rejected key; that keeps the SDK correct without depending on
// coord-side cleanup (mirrors the Ruby SDK's hardened behavior).
func (s *Session) setController(ctx context.Context, controller string, opts []ControlOption) (*ControlState, error) {
	o := controlOpts{actorType: ActorUserClick}
	for _, fn := range opts {
		fn(&o)
	}
	force := o.actorType == ActorAdminOverride // force ⇔ admin_override — always consistent
	ctrl := controller
	patch := ControlPatch{Controller: &ctrl, ActorType: o.actorType}
	for attempt := 0; attempt < 2; attempt++ {
		cur, err := s.ControlState(ctx)
		if err != nil {
			return nil, err
		}
		st, err := s.UpdateControl(ctx, patch, PatchControlOptions{IfMatch: cur.ETag, Force: force})
		if err == nil {
			return st, nil
		}
		var apiErr *APIError
		if attempt == 0 && !force && errors.As(err, &apiErr) && apiErr.Status == 412 {
			continue // stale ETag — refetch and retry once with a fresh key
		}
		return nil, err
	}
	return nil, nil // unreachable
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

// ListHandoffs returns a page of the session's control handoffs (summary-typed)
// plus the pagination cursor in HandoffList.NextBefore — pass it back as `before`
// to walk older pages ("" = no more). List items are summaries; use GetHandoff for
// one handoff's full forensic detail. limit<=0 / before="" omit the bounds.
func (s *Session) ListHandoffs(ctx context.Context, limit int, before string) (*HandoffList, error) {
	return s.coord.ListHandoffs(ctx, s.computerToken(), s.name, limit, before)
}

// GetHandoff returns one handoff with full forensic detail in Handoff.Raw.
func (s *Session) GetHandoff(ctx context.Context, handoffID string) (*Handoff, error) {
	return s.coord.GetHandoff(ctx, s.computerToken(), s.name, handoffID)
}

// ControlEvents streams the session's control-plane event feed (controller_changed /
// idle_changed / handoff_*) as a typed, resuming iterator — same shape as AgentMode.Events.
// Each ControlEvent's Type is the discriminator; Data holds the raw per-type payload. Pass
// lastEventID to resume (""=from now); break to stop; cancel ctx to end cleanly.
func (s *Session) ControlEvents(ctx context.Context, lastEventID string) iter.Seq2[ControlEvent, error] {
	return s.coord.ControlEvents(ctx, s.computerToken(), s.name, lastEventID)
}
