package pinesandbox

import (
	"context"
	"encoding/json"
)

// DriveMode is the BYOA drive-mode primitives — observe / computer-use / upload_file — all
// session-scoped (ps_). For callers driving the Computer with their own agent loop.
type DriveMode struct {
	s *Session
}

// Observe captures the session's current perception (raw).
func (d *DriveMode) Observe(ctx context.Context) (json.RawMessage, error) {
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
