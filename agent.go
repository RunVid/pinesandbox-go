package pinesandbox

import (
	"context"
	"encoding/json"

	"go.pinesandbox.io/computer/internal/coordinator"
)

// ErrStop, returned from an AgentMode.Events callback, stops the stream cleanly.
var ErrStop = coordinator.ErrStop

// AgentMode is the delegate-mode agent driver: one persistent Task per session, each Run a
// turn. Mutations (Run/Steer/Answer/Cancel/Reset) are ct_-authorized; reads
// (Status/Result/Events) use the session ps_. Methods return the raw agent-task JSON.
type AgentMode struct {
	s *Session
}

// RunOptions / SteerOptions are the optional knobs for Run / Steer (aliases of the
// coordinator types, consistent with the other facade option aliases).
type (
	RunOptions   = coordinator.AgentRunOptions
	SteerOptions = coordinator.AgentSteerOptions
)

// Run starts a turn toward goal.
func (a *AgentMode) Run(ctx context.Context, goal string, opts RunOptions) (json.RawMessage, error) {
	return a.s.coord.AgentRun(ctx, a.s.computerToken(), a.s.name, goal, opts)
}

// Steer injects guidance into the running turn.
func (a *AgentMode) Steer(ctx context.Context, text string, opts SteerOptions) (json.RawMessage, error) {
	return a.s.coord.AgentSteer(ctx, a.s.computerToken(), a.s.name, text, opts)
}

// Answer responds to the agent's clarifying question.
func (a *AgentMode) Answer(ctx context.Context, requestID, answer, expectedTurnID string) (json.RawMessage, error) {
	return a.s.coord.AgentAnswer(ctx, a.s.computerToken(), a.s.name, requestID, answer, expectedTurnID)
}

// Cancel cancels the running turn.
func (a *AgentMode) Cancel(ctx context.Context) (json.RawMessage, error) {
	return a.s.coord.AgentCancel(ctx, a.s.computerToken(), a.s.name)
}

// Reset clears the session's persistent agent thread (memory).
func (a *AgentMode) Reset(ctx context.Context) (json.RawMessage, error) {
	return a.s.coord.AgentReset(ctx, a.s.computerToken(), a.s.name)
}

// Status returns the current agent task/status.
func (a *AgentMode) Status(ctx context.Context) (json.RawMessage, error) {
	return a.s.coord.AgentTask(ctx, a.s.token, a.s.name)
}

// Result returns the latest finished turn's result.
func (a *AgentMode) Result(ctx context.Context) (json.RawMessage, error) {
	return a.s.coord.AgentResult(ctx, a.s.token, a.s.name)
}

// Events streams the session's agent event feed, invoking fn with each event's raw data
// JSON. Returns the latest event id (the resume cursor). fn returning ErrStop (or any
// error) stops the stream.
func (a *AgentMode) Events(ctx context.Context, lastEventID string, fn func(data []byte) error) (string, error) {
	return a.s.coord.AgentEvents(ctx, a.s.token, a.s.name, lastEventID, fn)
}
