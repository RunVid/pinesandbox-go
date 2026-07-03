package coordinator

import (
	"encoding/json"
	"fmt"
	"time"
)

// Typed agent-track results, mirroring the coordinator-api.yaml schemas (Task,
// TaskResult). Each carries a Raw escape hatch so a forward (additive) wire field
// the struct doesn't name is still reachable — additive growth is non-breaking
// (clients MUST tolerate unknown values), so the typed spine never blocks an
// integrator from a new field.
//
// Timestamps follow the established sessionWire pattern: the wire structs decode
// them as plain strings and convert via the tolerant parseTime (→ *time.Time, nil
// on empty/non-RFC3339). A 200 that the SDK doesn't functionally need a timestamp
// from must never hard-fail the whole parse — matching Session's behavior and the
// old raw-return contract.

// AgentUsage is a turn's structured usage (spec Task.usage / TaskResult.usage):
// the Pine-normalized token split, the turn duration (active excludes
// human-wait), and the priced cost. Decoded directly, so it keeps json tags.
type AgentUsage struct {
	LLM      AgentTokenUsage `json:"llm"`
	Duration AgentDuration   `json:"duration"`
	Cost     AgentCost       `json:"cost"`
}

// AgentTokenUsage is the disjoint LLM token split — input excludes cache;
// total_tokens is Pine-computed (input + cache_read + cache_write + output).
type AgentTokenUsage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// AgentDuration is the turn wall clock; ActiveMs excludes time parked awaiting a
// human (the billable-active input).
type AgentDuration struct {
	TotalMs  int64 `json:"total_ms"`
	ActiveMs int64 `json:"active_ms"`
}

// AgentCost is the priced LLM cost in USD. Total and LLM are nil when the model
// is un-carded (cost unknown, never a guessed 0); Compute is nil until a
// per-second compute rate exists (Total == LLM meanwhile).
type AgentCost struct {
	Currency string   `json:"currency"`
	Total    *float64 `json:"total"`
	LLM      *float64 `json:"llm"`
	Compute  *float64 `json:"compute"`
}

// Finding is a structured key/value a Task surfaced (spec TaskResult.findings item).
// Value + Provenance are arbitrary JSON (kept raw). (Decoded directly, keeps json tags.)
type Finding struct {
	Key        string          `json:"key"`
	Value      json.RawMessage `json:"value"`
	Provenance json.RawMessage `json:"provenance,omitempty"`
}

// FileRef is a validated, path-jailed reference to a file a Task produced (spec
// FileRef — TaskResult.artifacts items). Retrieve via the {Root, RelativePath} file route.
// Built from fileRefWire (tolerant timestamp), so no json tags here.
type FileRef struct {
	Root         string // workdir | artifact | download_quarantine
	RelativePath string
	ContentType  string
	Size         int64
	SHA256       string
	ModifiedAt   *time.Time
}

// AgentTask is the session's persistent Task — its lifecycle state + the latest
// turn's metadata (spec Task). State is idle|running|paused; Reason is set only
// while paused. Returned by run/steer/answer/cancel/reset (the updated Task) and
// status. Unknown State/Reason values must be tolerated (additive enum growth).
type AgentTask struct {
	TaskID        string
	Session       string
	ComputerID    string
	State         string // idle | running | paused
	Reason        string // present when paused
	Goal          string
	Context       string
	Constraints   json.RawMessage
	ThreadID      string
	CurrentTurnID string
	TurnAttempt   int
	Usage         AgentUsage
	CreatedAt     *time.Time
	UpdatedAt     *time.Time
	Raw           json.RawMessage // the full wire object (forward-compat escape hatch)
}

// AgentResult is a turn's terminal outcome (spec TaskResult). Status is
// ok|partial|failed; TerminalReason is the fine-grained WHY (completed, error,
// budget, canceled, …) — tolerate unknown values. Returned by result.
type AgentResult struct {
	Status         string
	TerminalReason string
	Summary        string
	Artifacts      []FileRef
	Findings       []Finding
	Usage          AgentUsage
	Raw            json.RawMessage
}

// ---- wire structs: string timestamps → tolerant parseTime (mirrors sessionWire) ----

type fileRefWire struct {
	Root         string `json:"root"`
	RelativePath string `json:"relative_path"`
	ContentType  string `json:"content_type"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	ModifiedAt   string `json:"modified_at"`
}

func (w fileRefWire) toFileRef() FileRef {
	return FileRef{
		Root:         w.Root,
		RelativePath: w.RelativePath,
		ContentType:  w.ContentType,
		Size:         w.Size,
		SHA256:       w.SHA256,
		ModifiedAt:   parseTime(w.ModifiedAt),
	}
}

type agentTaskWire struct {
	TaskID        string          `json:"task_id"`
	Session       string          `json:"session"`
	ComputerID    string          `json:"computer_id"`
	State         string          `json:"state"`
	Reason        string          `json:"reason"`
	Goal          string          `json:"goal"`
	Context       string          `json:"context"`
	Constraints   json.RawMessage `json:"constraints"`
	ThreadID      string          `json:"thread_id"`
	CurrentTurnID string          `json:"current_turn_id"`
	TurnAttempt   int             `json:"turn_attempt"`
	Usage         AgentUsage      `json:"usage"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

func (w *agentTaskWire) toAgentTask() *AgentTask {
	return &AgentTask{
		TaskID:        w.TaskID,
		Session:       w.Session,
		ComputerID:    w.ComputerID,
		State:         w.State,
		Reason:        w.Reason,
		Goal:          w.Goal,
		Context:       w.Context,
		Constraints:   w.Constraints,
		ThreadID:      w.ThreadID,
		CurrentTurnID: w.CurrentTurnID,
		TurnAttempt:   w.TurnAttempt,
		Usage:         w.Usage,
		CreatedAt:     parseTime(w.CreatedAt),
		UpdatedAt:     parseTime(w.UpdatedAt),
	}
}

type agentResultWire struct {
	Status         string        `json:"status"`
	TerminalReason string        `json:"terminal_reason"`
	Summary        string        `json:"summary"`
	Artifacts      []fileRefWire `json:"artifacts"`
	Findings       []Finding     `json:"findings"`
	Usage          AgentUsage    `json:"usage"`
}

func (w *agentResultWire) toAgentResult() *AgentResult {
	r := &AgentResult{
		Status:         w.Status,
		TerminalReason: w.TerminalReason,
		Summary:        w.Summary,
		Findings:       w.Findings,
		Usage:          w.Usage,
		Artifacts:      make([]FileRef, 0, len(w.Artifacts)),
	}
	for _, a := range w.Artifacts {
		r.Artifacts = append(r.Artifacts, a.toFileRef())
	}
	return r
}

func parseAgentTask(raw json.RawMessage) (*AgentTask, error) {
	var w agentTaskWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: decode agent task: %w", err)
	}
	t := w.toAgentTask()
	t.Raw = raw
	return t, nil
}

func parseAgentResult(raw json.RawMessage) (*AgentResult, error) {
	var w agentResultWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: decode agent result: %w", err)
	}
	r := w.toAgentResult()
	r.Raw = raw
	return r, nil
}
