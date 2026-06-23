package pinesandbox

import (
	"context"
	"encoding/json"

	"go.pinesandbox.io/computer/internal/coordinator"
)

// DriveMode is the BYOA drive-mode primitives — observe / computer-use / upload_file — all
// session-scoped (ps_). For callers driving the Computer with their own agent loop.
type DriveMode struct {
	s *Session
}

// Observation / Size / Scroll are the typed observe results (re-exported), mirroring the
// spec ObserveResult. ComputerUse still returns raw JSON — its result shape is per-action.
type (
	Observation = coordinator.Observation
	Size        = coordinator.Size
	Scroll      = coordinator.Scroll
)

// Observe captures the session's current perception (the ObservationSnapshot: screenshot +
// the coordinate metadata to map bbox/pixel actions).
func (d *DriveMode) Observe(ctx context.Context) (*Observation, error) {
	return d.s.coord.Observe(ctx, d.s.token, d.s.name)
}

// ComputerUse issues one low-level action; params are merged into the request body.
func (d *DriveMode) ComputerUse(ctx context.Context, action string, params map[string]any) (json.RawMessage, error) {
	return d.s.coord.ComputerUse(ctx, d.s.token, d.s.name, action, params)
}

// UploadFile stages a file into a selector's picker.
func (d *DriveMode) UploadFile(ctx context.Context, selector, file string) (json.RawMessage, error) {
	return d.s.coord.UploadFile(ctx, d.s.token, d.s.name, selector, file)
}
