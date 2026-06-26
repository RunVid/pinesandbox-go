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
// spec ObserveResult. ComputerUseResult is the typed computer-use outcome.
type (
	Observation       = coordinator.Observation
	Size              = coordinator.Size
	Scroll            = coordinator.Scroll
	ComputerUseResult = coordinator.ComputerUseResult
)

// Observe captures the session's current perception (the ObservationSnapshot: screenshot +
// the coordinate metadata to map bbox/pixel actions).
func (d *DriveMode) Observe(ctx context.Context) (*Observation, error) {
	return d.s.coord.Observe(ctx, d.s.token, d.s.name)
}

// ComputerUse issues one low-level action; params are merged into the request body.
// Returns the typed result: ComputerUseResult.Screenshot (base64 PNG) for
// action=="screenshot", else .OK.
func (d *DriveMode) ComputerUse(ctx context.Context, action string, params map[string]any) (*ComputerUseResult, error) {
	return d.s.coord.ComputerUse(ctx, d.s.token, d.s.name, action, params)
}

// ---- typed computer-use helpers ----
//
// Convenience wrappers over ComputerUse for the common actions of the spec's
// ComputerUseAction union, so callers don't hand-build params. The long tail
// (drag, hold_key, mouse down/up, wait, triple/middle click) stays reachable via
// raw ComputerUse(action, params). Coordinates are viewport CSS pixels.

// Screenshot captures the active tab; the PNG is in ComputerUseResult.Screenshot.
func (d *DriveMode) Screenshot(ctx context.Context) (*ComputerUseResult, error) {
	return d.ComputerUse(ctx, "screenshot", nil)
}

// Click left-clicks at (x, y).
func (d *DriveMode) Click(ctx context.Context, x, y int) (*ComputerUseResult, error) {
	return d.ComputerUse(ctx, "left_click", coordParams(x, y))
}

// RightClick right-clicks at (x, y).
func (d *DriveMode) RightClick(ctx context.Context, x, y int) (*ComputerUseResult, error) {
	return d.ComputerUse(ctx, "right_click", coordParams(x, y))
}

// DoubleClick double-clicks at (x, y).
func (d *DriveMode) DoubleClick(ctx context.Context, x, y int) (*ComputerUseResult, error) {
	return d.ComputerUse(ctx, "double_click", coordParams(x, y))
}

// MouseMove moves the cursor to (x, y).
func (d *DriveMode) MouseMove(ctx context.Context, x, y int) (*ComputerUseResult, error) {
	return d.ComputerUse(ctx, "mouse_move", coordParams(x, y))
}

// TypeText types literal text at the focused element.
func (d *DriveMode) TypeText(ctx context.Context, text string) (*ComputerUseResult, error) {
	return d.ComputerUse(ctx, "type", map[string]any{"text": text})
}

// Key presses a key or chord, e.g. "Enter" or "ctrl+a".
func (d *DriveMode) Key(ctx context.Context, chord string) (*ComputerUseResult, error) {
	return d.ComputerUse(ctx, "key", map[string]any{"text": chord})
}

// Scroll scrolls at (x, y) in direction ("up"|"down"|"left"|"right") by amount.
func (d *DriveMode) Scroll(ctx context.Context, x, y int, direction string, amount int) (*ComputerUseResult, error) {
	p := coordParams(x, y)
	p["scroll_direction"] = direction
	p["scroll_amount"] = amount
	return d.ComputerUse(ctx, "scroll", p)
}

func coordParams(x, y int) map[string]any { return map[string]any{"coordinate": []int{x, y}} }

// UploadFile stages a file into a selector's picker.
func (d *DriveMode) UploadFile(ctx context.Context, selector, file string) (json.RawMessage, error) {
	return d.s.coord.UploadFile(ctx, d.s.token, d.s.name, selector, file)
}
