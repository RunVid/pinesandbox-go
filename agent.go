package pinesandbox

import (
	"context"
	"iter"

	"go.pinesandbox.io/computer/internal/coordinator"
)

// ErrStop, returned from a Session.AuthorEvents callback, stops the stream cleanly.
var ErrStop = coordinator.ErrStop

// ErrStreamLost is the terminal error an event iterator (AgentMode.Events /
// Session.ControlEvents) yields when the feed can't be re-established within its bounded
// reconnect budget — detect it with errors.Is; the underlying transport fault is wrapped.
var ErrStreamLost = coordinator.ErrStreamLost

// AgentMode is the delegate-mode agent driver: one persistent Task per session, each Run a
// turn. Mutations (Run/Steer/Answer/Cancel/Reset) are ct_-authorized + return the updated
// AgentTask; reads use the session ps_ (Status → AgentTask, Result → AgentResult, Events →
// the typed, resuming event iterator).
type AgentMode struct {
	s *Session
}

// Typed agent-track surface (aliases of the coordinator types, mirroring the
// coordinator-api.yaml Task / TaskResult / TaskEvent schemas — each carries a Raw escape
// hatch).
type (
	RunOptions   = coordinator.AgentRunOptions
	SteerOptions = coordinator.AgentSteerOptions
	AgentTask    = coordinator.AgentTask
	AgentResult  = coordinator.AgentResult
	AgentUsage   = coordinator.AgentUsage
	FileRef      = coordinator.FileRef
	Finding      = coordinator.Finding
	AgentEvent   = coordinator.AgentEvent
	// AgentAsk is the typed needs_input payload — read it via AgentEvent.Ask().
	AgentAsk = coordinator.AgentAsk
)

// Run starts a turn toward goal; returns the session's Task (running for this turn).
// errors.Is(err, ErrSessionBusy) ⇒ a turn is already active; errors.Is(err,
// ErrActionNotImplemented) ⇒ no resident agent is configured on this pool.
func (a *AgentMode) Run(ctx context.Context, goal string, opts RunOptions) (*AgentTask, error) {
	return a.s.coord.AgentRun(ctx, a.s.computerToken(), a.s.name, goal, opts)
}

// Steer injects guidance into the running turn; returns the updated Task.
func (a *AgentMode) Steer(ctx context.Context, text string, opts SteerOptions) (*AgentTask, error) {
	return a.s.coord.AgentSteer(ctx, a.s.computerToken(), a.s.name, text, opts)
}

// Answer responds to the agent's clarifying question; returns the updated Task.
// expectedTurnID guards against answering a stale turn (""=skip the guard). Prefer
// AnswerAsk, which fills both ids from the ask.
func (a *AgentMode) Answer(ctx context.Context, requestID, answer, expectedTurnID string) (*AgentTask, error) {
	return a.s.coord.AgentAnswer(ctx, a.s.computerToken(), a.s.name, requestID, answer, expectedTurnID)
}

// AnswerAsk responds to a needs_input ask (from AgentEvent.Ask); the ask carries
// the request id + turn id, so this is the no-plumbing path:
//
//	if ask, ok := ev.Ask(); ok { ag.AnswerAsk(ctx, ask, reply(ask.Question)) }
func (a *AgentMode) AnswerAsk(ctx context.Context, ask *AgentAsk, answer string) (*AgentTask, error) {
	return a.Answer(ctx, ask.RequestID, answer, ask.TurnID)
}

// Cancel cancels the running turn; returns the updated Task.
func (a *AgentMode) Cancel(ctx context.Context) (*AgentTask, error) {
	return a.s.coord.AgentCancel(ctx, a.s.computerToken(), a.s.name)
}

// Reset clears the session's persistent agent thread (memory); returns the updated Task.
func (a *AgentMode) Reset(ctx context.Context) (*AgentTask, error) {
	return a.s.coord.AgentReset(ctx, a.s.computerToken(), a.s.name)
}

// Status returns the session's current Task (state/goal/usage/turn ids).
func (a *AgentMode) Status(ctx context.Context) (*AgentTask, error) {
	return a.s.coord.AgentTask(ctx, a.s.token, a.s.name)
}

// Result returns the latest finished turn's outcome. While a turn is still in flight the
// result isn't materialized: errors.Is(err, ErrTaskNotReady) ⇒ poll again. errors.Is(err,
// ErrNoActiveTask) ⇒ no turn has run yet.
func (a *AgentMode) Result(ctx context.Context) (*AgentResult, error) {
	return a.s.coord.AgentResult(ctx, a.s.token, a.s.name)
}

// Events streams the session's agent TaskEvent feed as a typed, resuming iterator:
//
//	for ev, err := range sess.Agent().Events(ctx, "") {
//		if err != nil { return err } // terminal: auth, or reconnect budget exhausted
//		if ev.Terminal { break }     // this turn ended
//	}
//
// The feed is continuous across turns; a dropped connection is transparently resumed from
// the last event id under a bounded reconnect budget. Pass lastEventID to resume after a
// process restart (""=from now). Break to stop; cancel ctx to end cleanly.
func (a *AgentMode) Events(ctx context.Context, lastEventID string) iter.Seq2[AgentEvent, error] {
	return a.s.coord.AgentEvents(ctx, a.s.token, a.s.name, lastEventID)
}
