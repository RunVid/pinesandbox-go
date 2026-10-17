package coordinator

import (
	"encoding/json"
	"fmt"
	"time"
)

// Typed agent-track results, mirroring the coordinator-api.yaml schemas (PublicTask,
// PublicTurn, PublicResult). Each carries a Raw escape hatch so a forward (additive) wire field
// the struct doesn't name is still reachable — additive growth is non-breaking
// (clients MUST tolerate unknown values), so the typed spine never blocks an
// integrator from a new field.
//
// Timestamps follow the established sessionWire pattern: the wire structs decode
// them as plain strings and convert via the tolerant parseTime (→ *time.Time, nil
// on empty/non-RFC3339). A 200 that the SDK doesn't functionally need a timestamp
// from must never hard-fail the whole parse — matching Session's behavior and the
// old raw-return contract.

// AgentUsage is a turn's structured usage (spec PublicUsage): the
// Pine-normalized token split, the turn's duration, and the customer-facing
// rated charge. Decoded directly, so it keeps json tags.
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

// AgentDuration is how long the turn ran.
type AgentDuration struct {
	TotalMs int64 `json:"total_ms"`
}

// AgentCost is the public customer-facing rated charge. It is not Pine's
// provider cost or an account invoice. Total and LLM are nil only when the
// project's customer rate card cannot price the observation; Compute is
// currently nil.
type AgentCost struct {
	Currency string   `json:"currency"`
	Total    *float64 `json:"total"`
	LLM      *float64 `json:"llm"`
	Compute  *float64 `json:"compute"`
}

// AuthorResult is present only for skill-author tasks. Draft is a LearnedSkillMeta
// object kept raw so the SDK does not couple ordinary task results to skill types.
type AuthorResult struct {
	Draft     json.RawMessage `json:"draft,omitempty"`
	Questions []string        `json:"questions,omitempty"`
	HandoffID string          `json:"handoff_id,omitempty"`
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

// AgentTask is the session's agent Task and its latest turn (spec PublicTask). The
// Task scopes event cursors; it is never what you answer, steer or cancel — those take
// the turn ID you saved. Turn is nil before the first run.
type AgentTask struct {
	TaskID     string
	Session    string
	ComputerID string
	Turn       *AgentTurn
	CreatedAt  *time.Time
	UpdatedAt  *time.Time
	Raw        json.RawMessage // the full wire object (forward-compat escape hatch)
}

// AgentTurn is one unit of agent work (spec PublicTurn): it starts when a run or an
// answer is accepted and ends when the agent finishes, fails, is canceled or asks a
// question. Every ending is the turn's Result, with its usage. Save TurnID and wait
// for the turn; a question is answered with a new turn. Running and canceling change,
// and needs_input becomes answered; every other state is final for this turn. An
// answered turn names the turn its answer opened in NextTurnID. Unknown State values
// must be tolerated.
type AgentTurn struct {
	TurnID       string
	State        string       // running | canceling | needs_input | answered | completed | failed | canceled
	NextTurnID   string       // the turn this turn's answer opened; set when answered
	Result       *AgentResult // how the turn ended, with its usage; nil while it runs
	StartedAt    *time.Time
	EndedAt      *time.Time // nil while running or canceling
	FirstEventID int64      // the turn's slice of the event history
	LastEventID  int64
	Raw          json.RawMessage
}

// Question returns the question that ended the turn — its result's summary — or ""
// when the turn did not end with one.
func (t *AgentTurn) Question() string {
	if t == nil || t.Result == nil || t.Result.TerminalReason != "needs_input" {
		return ""
	}
	return t.Result.Summary
}

// AgentResult is how a turn ended (spec PublicResult). TerminalReason says why;
// Summary states the actual goal outcome and must be read even when the turn
// completed, and for needs_input it is the question. Usage is what the turn used.
// Unknown values are tolerated.
type AgentResult struct {
	TerminalReason string
	Summary        string
	Artifacts      []FileRef
	Author         *AuthorResult
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
	TaskID     string          `json:"task_id"`
	Session    string          `json:"session"`
	ComputerID string          `json:"computer_id"`
	Turn       json.RawMessage `json:"turn"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}

type agentTurnWire struct {
	TurnID       string          `json:"turn_id"`
	State        string          `json:"state"`
	NextTurnID   string          `json:"next_turn_id"`
	Result       json.RawMessage `json:"result"`
	StartedAt    string          `json:"started_at"`
	EndedAt      string          `json:"ended_at"`
	FirstEventID int64           `json:"first_event_id"`
	LastEventID  int64           `json:"last_event_id"`
}

type agentResultWire struct {
	TerminalReason string        `json:"terminal_reason"`
	Summary        string        `json:"summary"`
	Artifacts      []fileRefWire `json:"artifacts"`
	Author         *AuthorResult `json:"author,omitempty"`
	Usage          AgentUsage    `json:"usage"`
}

func (w *agentResultWire) toAgentResult() *AgentResult {
	r := &AgentResult{
		TerminalReason: w.TerminalReason,
		Summary:        w.Summary,
		Author:         w.Author,
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
	t := &AgentTask{
		TaskID: w.TaskID, Session: w.Session, ComputerID: w.ComputerID,
		CreatedAt: parseTime(w.CreatedAt), UpdatedAt: parseTime(w.UpdatedAt), Raw: raw,
	}
	if len(w.Turn) > 0 && string(w.Turn) != "null" {
		turn, err := parseAgentTurn(w.Turn)
		if err != nil {
			return nil, err
		}
		t.Turn = turn
	}
	return t, nil
}

func parseAgentTurn(raw json.RawMessage) (*AgentTurn, error) {
	var w agentTurnWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: decode agent turn: %w", err)
	}
	t := &AgentTurn{
		TurnID: w.TurnID, State: w.State, NextTurnID: w.NextTurnID,
		StartedAt: parseTime(w.StartedAt), EndedAt: parseTime(w.EndedAt),
		FirstEventID: w.FirstEventID, LastEventID: w.LastEventID, Raw: raw,
	}
	if len(w.Result) > 0 && string(w.Result) != "null" {
		result, err := parseAgentResult(w.Result)
		if err != nil {
			return nil, err
		}
		t.Result = result
	}
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
