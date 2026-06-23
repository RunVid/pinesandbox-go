package pinesandbox

import (
	"context"

	"go.pinesandbox.io/computer/internal/coordinator"
)

// ExecOptions / ExecResult configure and carry a command execution.
type (
	ExecOptions = coordinator.ExecOptions
	ExecResult  = coordinator.ExecResult
)

// Exec runs a shell command in the session's bash terminal (ps_), accumulating stdout/stderr
// and the exit code from the SSE stream. fn (optional) receives each parsed event as it
// arrives. A 409 terminal_lost surfaces as *APIError — call RecreateTerminal then retry.
func (s *Session) Exec(ctx context.Context, command string, opts ExecOptions, fn func(event map[string]any) error) (*ExecResult, error) {
	return s.coord.Exec(ctx, s.token, s.name, command, opts, fn)
}
