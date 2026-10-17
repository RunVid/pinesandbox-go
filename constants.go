package pinesandbox

// Canonical string values an integrator matches against or supplies — so you write
// `turn.State == pine.TurnNeedsInput` instead of guessing `"needs_input"` (autocomplete
// + one spelling). Plain string constants, net/http-style. The agent/terminal sets
// are ADDITIVE — the API may send values not listed here, so switch with a default
// rather than assuming these are exhaustive.

// Agent event kinds — AgentEvent.Type (spec PublicActivityEvent.type).
const (
	EventStatus            = "status"
	EventInput             = "input"              // caller-authored run, answer, or steer — see AgentEvent.Input()
	EventProgress          = "progress"           // the agent's narration — see AgentEvent.Progress()
	EventActivity          = "activity"           // an operation started or ended — see AgentEvent.Activity()
	EventFrame             = "frame"              // a picture of the screen — see AgentEvent.Frame()
	EventControllerChanged = "controller_changed" // see AgentEvent.ControlChange()
	EventResult            = "result"             // the turn ended, including with a question — see AgentEvent.Result()
)

// Turn states — AgentTurn.State. Running and canceling change, and needs_input
// becomes answered; every other state is final for that turn. Additive: treat an
// unknown state as final.
const (
	TurnRunning    = "running"     // accepted and working, including before the agent starts
	TurnNeedsInput = "needs_input" // the turn ended with a question (Question) — Answer it with the turn's ID
	TurnCanceling  = "canceling"   // cancellation was accepted; the agent is stopping
	TurnAnswered   = "answered"    // its question was answered — continue with NextTurnID
	TurnCompleted  = "completed"   // the agent finished — read Result
	TurnFailed     = "failed"      // the work ended early — read Result
	TurnCanceled   = "canceled"
)

// Activity states — AgentActivity.State. Every step ends with exactly one
// outcome, after ActivityRunning when the step takes time.
const (
	ActivityRunning     = "running"
	ActivityCompleted   = "completed" // the step finished, not proof the goal succeeded
	ActivityFailed      = "failed"
	ActivityDeclined    = "declined"    // the runtime refused to run it
	ActivityInterrupted = "interrupted" // its turn stopped (an ask, a cancel, an error) first
)

// Activity kinds — AgentActivity.Kind: what a step was for a person. The set
// grows; treat a kind you don't know as a generic step.
const (
	ActivityKindBrowser = "browser"
	ActivityKindCommand = "command"
	ActivityKindFile    = "file"
	ActivityKindSkill   = "skill"
	ActivityKindTool    = "tool"
)

// How a step touched a file — ActivityFile.Change.
const (
	ActivityFileCreated = "created"
	ActivityFileEdited  = "edited"
	ActivityFileDeleted = "deleted"
	ActivityFileMoved   = "moved"
	ActivityFileRead    = "read"
)

// Controllers — who drives a session (ControlState.Controller, Handoff.ControllerAt*).
const (
	ControllerHuman  = "human"
	ControllerAgent  = "agent"
	ControllerLocked = "locked"
)

// Actor types for a control transition. TakeControl/ReleaseControl default to
// ActorUserClick; pass WithForce() (which uses ActorAdminOverride) to bypass If-Match.
const (
	ActorUserClick     = "user_click"
	ActorAdminOverride = "admin_override"
)

// Terminal reasons — AgentResult.TerminalReason (how a turn ended). Additive.
const (
	TerminalCompleted              = "completed"
	TerminalNeedsInput             = "needs_input" // the agent asked a question; Summary is the question
	TerminalBudget                 = "budget"
	TerminalCanceled               = "canceled"
	TerminalError                  = "error"
	TerminalExecutorPanic          = "executor_panic"
	TerminalCoordinatorRestarted   = "coordinator_restarted"
	TerminalOutputInvalid          = "output_invalid"
	TerminalDraftRegistered        = "draft_registered"        // learn/teach/refine authoring
	TerminalClarificationRequested = "clarification_requested" // learn/teach authoring
	TerminalNotReusable            = "not_reusable"
	TerminalRegistrationFailed     = "registration_failed"
	TerminalFailed                 = "failed"
)

// Control-plane event types — ControlEvent.Type.
const (
	ControlControllerChanged = "controller_changed"
	ControlIdleChanged       = "idle_changed"
	ControlHandoffCompleted  = "handoff_completed"
	ControlHandoffFailed     = "handoff_failed"
)
