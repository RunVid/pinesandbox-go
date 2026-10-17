package pinesandbox

import (
	"context"
	"iter"
	"time"

	"go.pinesandbox.io/computer/internal/coordinator"
)

// ErrStop, returned from a Session.AuthorEvents callback, stops the stream cleanly.
var ErrStop = coordinator.ErrStop

// ErrStreamLost is the terminal error an event iterator (AgentMode.Events /
// Session.ControlEvents) yields when the feed can't be re-established within its bounded
// reconnect budget — detect it with errors.Is; the underlying transport fault is wrapped.
var ErrStreamLost = coordinator.ErrStreamLost

// AgentMode drives Pine's agent in a session. Work is addressed by turn: a turn starts
// when a run or an answer is accepted and ends when the agent finishes, fails, is
// canceled or asks a question. Every ending is the turn's Result, with its usage, and
// an answer is the next turn. Save the turn ID Run and Answer return and Wait for it;
// nothing else needs to be kept between calls or processes:
//
//	turn, err := ag.Run(ctx, goal, pine.RunOptions{IdempotencyKey: savedMessageID})
//	// persist turn.TurnID with your record
//	turn, err = ag.Wait(ctx, turnID, 30*time.Second)
//	switch turn.State {
//	case pine.TurnNeedsInput: // present turn.Question() with turn.TurnID
//	case pine.TurnAnswered: // another worker answered: continue with turn.NextTurnID
//	case pine.TurnCompleted, pine.TurnFailed, pine.TurnCanceled: // read turn.Result
//	}
//	next, err := ag.Answer(ctx, turnID, reply, pine.AnswerOptions{}) // persist next.TurnID
//
// Mutations (Run/Steer/Answer/Cancel/Reset) use the Computer token (ct_); reads
// (Wait/Status/Turns/Events) use the session token (ps_).
type AgentMode struct {
	s *Session
}

// Agent types, mirroring the Computer API's PublicTask, PublicTurn, PublicResult and
// PublicActivityEvent schemas. AgentTask, AgentTurn, AgentResult and AgentEvent also
// keep the full JSON object in Raw.
type (
	// RunOptions are AgentMode.Run's optional context, skills, constraints, secrets
	// and idempotency key.
	RunOptions = coordinator.AgentRunOptions
	// TaskOptions binds a read or memory reset to an observed Task.
	TaskOptions = coordinator.AgentTaskOptions
	// RunConstraints are a run's token and wall-clock limits.
	RunConstraints = coordinator.AgentRunConstraints
	// SteerOptions can deliver secrets with a steer.
	SteerOptions = coordinator.AgentSteerOptions
	// AnswerOptions can deliver secrets with an answer.
	AnswerOptions = coordinator.AgentAnswerOptions
	// AgentTask is the session's agent Task and its latest turn.
	AgentTask = coordinator.AgentTask
	// AgentTurn is one unit of agent work: its state and, once it ends, its result.
	AgentTurn = coordinator.AgentTurn
	// AgentResult is how a turn ended: its report or question, files and usage.
	AgentResult = coordinator.AgentResult
	// AgentUsage is a turn's token counts, duration and cost.
	AgentUsage = coordinator.AgentUsage
	// AgentTokenUsage is a turn's model token counts.
	AgentTokenUsage = coordinator.AgentTokenUsage
	// AgentDuration is how long a turn ran.
	AgentDuration = coordinator.AgentDuration
	// AgentCost is a turn's cost at your project's rates.
	AgentCost = coordinator.AgentCost
	// FileRef points to a file a turn produced.
	FileRef = coordinator.FileRef
	// AuthorResult is the outcome of a skill-authoring run.
	AuthorResult = coordinator.AuthorResult
	// AgentEvent is one event from AgentMode.Events.
	AgentEvent = coordinator.AgentEvent
	// AgentControlChange is the typed controller_changed payload returned by
	// AgentEvent.ControlChange().
	AgentControlChange = coordinator.AgentControlChange
	// AgentProgress is the agent's narration — read it via AgentEvent.Progress().
	AgentProgress = coordinator.AgentProgress
	// AgentInput is accepted caller input — read it via AgentEvent.Input().
	AgentInput = coordinator.AgentInput
	// AgentActivity is one step of the agent's work — read it via AgentEvent.Activity().
	AgentActivity = coordinator.AgentActivity
	// ActivityFacts are what a step acted on (AgentActivity.Facts).
	ActivityFacts = coordinator.ActivityFacts
	// ActivityPage is a page a browser step opened.
	ActivityPage = coordinator.ActivityPage
	// ActivityTarget is an element a browser step acted on.
	ActivityTarget = coordinator.ActivityTarget
	// ActivityFile is a work-directory file a step touched.
	ActivityFile = coordinator.ActivityFile
	// ActivitySkill is one of your learned skills a step used.
	ActivitySkill = coordinator.ActivitySkill
	// AgentFrame is a picture of the screen — read it via AgentEvent.Frame().
	AgentFrame = coordinator.AgentFrame
)

// EventsOptions tunes AgentMode.Events.
type EventsOptions = coordinator.AgentEventsOptions

// Run starts a turn toward goal and returns it; save its TurnID. errors.Is(err,
// ErrSessionBusy) ⇒ a turn is already running; errors.Is(err, ErrActionNotImplemented) ⇒
// Pine's agent isn't available in this environment. Starting new work leaves an
// unanswered question unanswered for good.
//
// opts.IdempotencyKey names the submission, for example your saved message ID: a run
// repeated with the same key returns the turn that key admitted. Without one, Run uses a
// key of its own so its retry after a lost response is safe; a fresh process recovering
// from a crash must pass the key it saved to get the same turn back. When no response
// arrives, or ctx ends first, Run returns a *RunUnconfirmedError naming the key: send the
// run again with it.
func (a *AgentMode) Run(ctx context.Context, goal string, opts RunOptions) (*AgentTurn, error) {
	if opts.IdempotencyKey == "" {
		key, err := uuidV7()
		if err != nil {
			return nil, err
		}
		opts.IdempotencyKey = key
	}
	return a.s.coord.AgentRun(ctx, a.s.computerToken(), a.s.name, goal, opts)
}

// Wait returns the turn once it ends, with its result or its question, or after
// timeout with the turn still running or canceling; a zero timeout reads it once. A read
// whose response was lost (a network fault, a 408, or a gateway 502, 503 or 504), or
// whose problem is marked retryable, is read again until the timeout; any other error
// is returned. Wait never cancels the turn and never follows NextTurnID. An answered
// turn means someone already answered its question: continue with NextTurnID instead of
// presenting it again.
func (a *AgentMode) Wait(ctx context.Context, turnID string, timeout time.Duration) (*AgentTurn, error) {
	return a.s.coord.AgentWait(ctx, a.s.token, a.s.name, turnID, timeout)
}

// Answer answers the question that ended turnID and returns the turn the answer opened,
// which continues the same goal; save its TurnID. Only the session's latest turn can be
// answered. Sending the same answer again returns that same turn. Put only $NAME
// references in answer; opts.Secrets carries credentials outside model-visible text.
func (a *AgentMode) Answer(ctx context.Context, turnID, answer string, opts AnswerOptions) (*AgentTurn, error) {
	return a.s.coord.AgentAnswer(ctx, a.s.computerToken(), a.s.name, turnID, answer, opts)
}

// Steer adds guidance to turnID while it is running and returns the turn. It is not
// retried: a conflict at a completion boundary does not prove the text was not delivered.
func (a *AgentMode) Steer(ctx context.Context, turnID, text string, opts SteerOptions) (*AgentTurn, error) {
	return a.s.coord.AgentSteer(ctx, a.s.computerToken(), a.s.name, turnID, text, opts)
}

// Cancel cancels turnID while it runs and returns it, canceling until the agent stops.
// A turn whose question waits for an answer is closed: it ends canceled and can no
// longer be answered. An answered turn is a 409 /errors/stale-turn: cancel the turn its
// answer opened instead. Any other ended turn is returned unchanged, so a lost response
// is resent.
func (a *AgentMode) Cancel(ctx context.Context, turnID string) (*AgentTurn, error) {
	return a.s.coord.AgentCancel(ctx, a.s.computerToken(), a.s.name, turnID)
}

// Reset clears the session's persistent agent thread (memory); returns the Task.
func (a *AgentMode) Reset(ctx context.Context, opts TaskOptions) (*AgentTask, error) {
	return a.s.coord.AgentReset(ctx, a.s.computerToken(), a.s.name, opts)
}

// Status returns the session's Task and its latest turn, for discovering work. Answer,
// steer and cancel the turn you saved, not whichever turn is latest.
func (a *AgentMode) Status(ctx context.Context, opts TaskOptions) (*AgentTask, error) {
	return a.s.coord.AgentTask(ctx, a.s.token, a.s.name, opts)
}

// Turns lists the session's retained turns, oldest first, with the IDs events carry.
func (a *AgentMode) Turns(ctx context.Context, opts TaskOptions) ([]AgentTurn, error) {
	return a.s.coord.AgentTurns(ctx, a.s.token, a.s.name, opts)
}

// Events streams the session's public activity as a typed, resuming iterator, for
// showing progress; Wait is how you learn a turn ended:
//
//	for ev, err := range sess.Agent().Events(ctx, cursor, EventsOptions{ExpectedTaskID: task.TaskID}) {
//		if err != nil { return err }
//		if a, ok := ev.Activity(); ok { ui.UpsertStep(ev.TurnID, a.ID, a.State, a.Title) }
//	}
//
// The feed is continuous across turns; a dropped connection is transparently resumed from
// the last event id under a bounded reconnect budget. Pass lastEventID to resume after a
// process restart ("" replays retained history). Save both TaskID and cursor.
// Break to stop; cancel ctx to end cleanly.
func (a *AgentMode) Events(ctx context.Context, lastEventID string, opts EventsOptions) iter.Seq2[AgentEvent, error] {
	return a.s.coord.AgentEvents(ctx, a.s.token, a.s.name, lastEventID, opts)
}
