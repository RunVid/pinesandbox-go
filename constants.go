package pinesandbox

// Canonical string values an integrator matches against or supplies — so you write
// `ev.Type == pine.EventNeedsInput` instead of guessing `"needs_input"` (autocomplete
// + one spelling). Plain string constants, net/http-style. The agent/terminal sets
// are ADDITIVE — the coord may emit values not listed here, so switch with a default
// rather than assuming these are exhaustive.

// Agent event kinds — AgentEvent.Type (spec TaskEvent.type).
const (
	EventStatus         = "status"
	EventReasoning      = "reasoning"
	EventCommand        = "command"
	EventStep           = "step"
	EventNeedsInput     = "needs_input" // the agent paused on a question — see AgentEvent.Ask()
	EventUsage          = "usage"
	EventScreenshot     = "screenshot"
	EventResult         = "result" // the turn ended — fetch AgentMode.Result
	EventUsageFinalized = "usage.finalized"
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
