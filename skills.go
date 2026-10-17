package pinesandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"go.pinesandbox.io/computer/internal/coordinator"
)

// TeachOptions / AuthorSkillOptions configure the authoring calls.
type (
	TeachOptions       = coordinator.TeachOptions
	AuthorSkillOptions = coordinator.AuthorSkillOptions
)

// ---- Computer-level served skills (ct_) ----

// RequireAgentProtocol checks the current binding before building an agent UI.
// Unsupported runtimes return AgentProtocolUnsupportedError. Missing Computers,
// authorization refusals and transport failures retain their original errors.
// No result is cached across binding changes. Other agent calls qualify the runtime only
// when an unrouted 404 needs it.
func (c *Computer) RequireAgentProtocol(ctx context.Context) error {
	coord, ct, err := c.bound()
	if err != nil {
		return err
	}
	return coord.RequirePublicAgent(ctx, ct)
}

// Capabilities returns the product features this Computer can use for its
// binding and the limits it enforces on your calls (raw).
func (c *Computer) Capabilities(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.Capabilities(ctx, ct)
}

// Skills is the Computer's skill catalog: Pine's skills and the skills you authored or
// learned on this Computer. Every call uses the Computer token.
type Skills struct {
	c *Computer
}

// Skills returns the Computer's skill catalog.
func (c *Computer) Skills() *Skills { return &Skills{c: c} }

// List returns the served skills — Pine's skills plus your activated ones (raw array).
func (s *Skills) List(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := s.c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ListSkills(ctx, ct)
}

// Get returns one skill's metadata + SKILL.md body (raw): a skill authored on the
// Computer, or the computer-agent role guide. Pine's other skills are listed by name and
// description; their content is not served.
func (s *Skills) Get(ctx context.Context, name string) (json.RawMessage, error) {
	coord, ct, err := s.c.bound()
	if err != nil {
		return nil, err
	}
	return coord.GetSkill(ctx, ct, name)
}

// SetEnabled switches one of Pine's skills that List marks switchable on or off for the
// agent processes this Computer starts next (raw result: the skill as the next agent is
// offered it, and whether the switch changed). Other Pine skills cannot be switched;
// learned skills use Activate and Deactivate. A running agent keeps its catalog until its
// session's agent is reset.
func (s *Skills) SetEnabled(ctx context.Context, name string, enabled bool) (json.RawMessage, error) {
	coord, ct, err := s.c.bound()
	if err != nil {
		return nil, err
	}
	return coord.SetSkillEnabled(ctx, ct, name, enabled)
}

// Drafts returns pending drafts with their bodies (raw array).
func (s *Skills) Drafts(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := s.c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ListSkillDrafts(ctx, ct)
}

// Versions returns the visible versions of one skill, or of every skill when name is ""
// (raw array, metadata only).
func (s *Skills) Versions(ctx context.Context, name string) (json.RawMessage, error) {
	coord, ct, err := s.c.bound()
	if err != nil {
		return nil, err
	}
	if name == "" {
		return coord.ListAllSkillVersions(ctx, ct)
	}
	return coord.ListSkillVersions(ctx, ct, name)
}

// Version returns one version's metadata + SKILL.md body (raw).
func (s *Skills) Version(ctx context.Context, name string, version int) (json.RawMessage, error) {
	coord, ct, err := s.c.bound()
	if err != nil {
		return nil, err
	}
	return coord.GetSkillVersion(ctx, ct, name, strconv.Itoa(version))
}

// Activate makes a reviewed version the one the agent is offered (raw). The version is
// required: activating reactivates exactly the version you reviewed.
func (s *Skills) Activate(ctx context.Context, name string, version int) (json.RawMessage, error) {
	coord, ct, err := s.c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ActivateSkill(ctx, ct, name, version)
}

// Deactivate stops offering a skill to the agent, keeping its versions (raw).
func (s *Skills) Deactivate(ctx context.Context, name string) (json.RawMessage, error) {
	coord, ct, err := s.c.bound()
	if err != nil {
		return nil, err
	}
	return coord.DeactivateSkill(ctx, ct, name)
}

// DeleteVersion hides a version, no longer offering it if it was active (raw).
func (s *Skills) DeleteVersion(ctx context.Context, name string, version int) (json.RawMessage, error) {
	coord, ct, err := s.c.bound()
	if err != nil {
		return nil, err
	}
	return coord.DeleteSkillVersion(ctx, ct, name, strconv.Itoa(version))
}

// ---- the former flattened skill methods ----

// ListSkills returns the served skills (raw array).
//
// Deprecated: use Computer.Skills().List.
func (c *Computer) ListSkills(ctx context.Context) (json.RawMessage, error) {
	return c.Skills().List(ctx)
}

// GetSkill returns one skill's metadata + SKILL.md body (raw).
//
// Deprecated: use Computer.Skills().Get.
func (c *Computer) GetSkill(ctx context.Context, name string) (json.RawMessage, error) {
	return c.Skills().Get(ctx, name)
}

// ListSkillDrafts returns pending drafts with their bodies (raw array).
//
// Deprecated: use Computer.Skills().Drafts.
func (c *Computer) ListSkillDrafts(ctx context.Context) (json.RawMessage, error) {
	return c.Skills().Drafts(ctx)
}

// ListSkillVersions returns visible versions for a name (raw array); name "" → all names.
//
// Deprecated: use Computer.Skills().Versions.
func (c *Computer) ListSkillVersions(ctx context.Context, name string) (json.RawMessage, error) {
	return c.Skills().Versions(ctx, name)
}

// GetSkillVersion returns one version's metadata + SKILL.md body (raw).
//
// Deprecated: use Computer.Skills().Version, which takes the version as an int.
func (c *Computer) GetSkillVersion(ctx context.Context, name, version string) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.GetSkillVersion(ctx, ct, name, version)
}

// ActivateSkill makes a reviewed draft version the one the agent is offered (raw).
// version is the decimal version number; it is sent as the integer the API requires.
//
// Deprecated: use Computer.Skills().Activate, which takes the version as an int.
func (c *Computer) ActivateSkill(ctx context.Context, name, version string) (json.RawMessage, error) {
	n, err := skillVersion(version)
	if err != nil {
		return nil, err
	}
	return c.Skills().Activate(ctx, name, n)
}

// DeactivateSkill un-serves a skill, keeping the version (raw).
//
// Deprecated: use Computer.Skills().Deactivate.
func (c *Computer) DeactivateSkill(ctx context.Context, name string) (json.RawMessage, error) {
	return c.Skills().Deactivate(ctx, name)
}

// SetSkillEnabled switches one of Pine's switchable skills on or off (raw).
//
// Deprecated: use Computer.Skills().SetEnabled.
func (c *Computer) SetSkillEnabled(ctx context.Context, name string, enabled bool) (json.RawMessage, error) {
	return c.Skills().SetEnabled(ctx, name, enabled)
}

// DeleteSkillVersion hides a version, un-serving it if active (raw).
//
// Deprecated: use Computer.Skills().DeleteVersion, which takes the version as an int.
func (c *Computer) DeleteSkillVersion(ctx context.Context, name, version string) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.DeleteSkillVersion(ctx, ct, name, version)
}

// skillVersion parses a decimal skill version for the deprecated string-taking forms.
func skillVersion(version string) (int, error) {
	n, err := strconv.Atoi(version)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("pinesandbox: skill version must be a positive integer, got %q", version)
	}
	return n, nil
}

// ---- session-scoped authoring (ct_) ----
//
// Authoring mutations require the Computer token (the agent itself can't author). The
// author event stream accepts either token, so it uses the session token when the handle
// has one and the Computer token otherwise — which lets a ct_-only handle, e.g. one from
// Computer.Session(name), whose ps_ is redacted, both start authoring and stream it.

// AuthorRun is an accepted authoring run (learn, teach or refine). Follow it with
// Session.AuthorEvents(AuthorID) until its result, or cancel it with
// Session.CancelAuthor(AuthorID).
type AuthorRun struct {
	AuthorID  string // the author session (wire author_session)
	TaskID    string
	HandoffID string // the demonstration a teach run reads; empty for learn and refine
	Raw       json.RawMessage
}

func parseAuthorRun(raw json.RawMessage) (*AuthorRun, error) {
	var w struct {
		AuthorSession string `json:"author_session"`
		TaskID        string `json:"task_id"`
		HandoffID     string `json:"handoff_id"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable authoring run: %w", err)
	}
	return &AuthorRun{AuthorID: w.AuthorSession, TaskID: w.TaskID, HandoffID: w.HandoffID, Raw: raw}, nil
}

// StartLearn starts authoring a draft from the session's latest finished run and returns
// the accepted run at once; scaffold (optional) seeds the SKILL.md the author continues
// from. A run takes minutes: follow it with AuthorEvents.
func (s *Session) StartLearn(ctx context.Context, scaffold string) (*AuthorRun, error) {
	raw, err := s.coord.LearnSkill(ctx, s.computerToken(), s.name, scaffold)
	if err != nil {
		return nil, err
	}
	return parseAuthorRun(raw)
}

// StartTeach starts authoring a draft from a human demonstration toward goal and returns
// the accepted run at once.
func (s *Session) StartTeach(ctx context.Context, goal string, opts TeachOptions) (*AuthorRun, error) {
	raw, err := s.coord.TeachSkill(ctx, s.computerToken(), s.name, goal, opts)
	if err != nil {
		return nil, err
	}
	return parseAuthorRun(raw)
}

// StartRefine starts revising an existing immutable skill version with your guidance and
// returns the accepted run at once; the new draft arrives as the run's result.
func (s *Session) StartRefine(ctx context.Context, skill string, version int, guidance string) (*AuthorRun, error) {
	raw, err := s.coord.RefineSkill(ctx, s.computerToken(), s.name, skill, version, guidance)
	if err != nil {
		return nil, err
	}
	return parseAuthorRun(raw)
}

// Learn authors a draft from the session's latest finished run; scaffold (optional) seeds
// the SKILL.md the author continues from (raw 202 body — drive AuthorEvents for progress).
//
// Deprecated: Learn returns as soon as the run is accepted. At the next pool minor (0.4)
// it blocks until the authoring result, as every other server SDK's learn does. Use
// StartLearn for the asynchronous form.
func (s *Session) Learn(ctx context.Context, scaffold string) (json.RawMessage, error) {
	return s.coord.LearnSkill(ctx, s.computerToken(), s.name, scaffold)
}

// Teach authors a draft from a human demonstration toward goal (raw 202 body).
//
// Deprecated: Teach returns as soon as the run is accepted. At the next pool minor (0.4)
// it blocks until the authoring result, as every other server SDK's teach does. Use
// StartTeach for the asynchronous form.
func (s *Session) Teach(ctx context.Context, goal string, opts TeachOptions) (json.RawMessage, error) {
	return s.coord.TeachSkill(ctx, s.computerToken(), s.name, goal, opts)
}

// Refine revises an existing immutable skill version with your guidance
// (raw 202 body — drive AuthorEvents for progress and the resulting draft).
//
// Deprecated: Refine returns as soon as the run is accepted. At the next pool minor (0.4)
// it blocks until the new draft, as every other server SDK's refine does. Use StartRefine
// for the asynchronous form.
func (s *Session) Refine(ctx context.Context, skill string, version int, guidance string) (json.RawMessage, error) {
	return s.coord.RefineSkill(ctx, s.computerToken(), s.name, skill, version, guidance)
}

// AuthorSkill registers a SKILL.md draft you wrote yourself (raw).
func (s *Session) AuthorSkill(ctx context.Context, skillName, skillMD, reason string, opts AuthorSkillOptions) (json.RawMessage, error) {
	return s.coord.AuthorSkillDraft(ctx, s.computerToken(), s.name, skillName, skillMD, reason, opts)
}

// AuthorEvents streams an author run until its result event; fn receives each event's
// data JSON. A stream that closes before the result is reopened from the last event id
// (ErrStreamLost after the reconnect budget). Returns the resume cursor; fn returning
// ErrStop stops cleanly, and the context's error ends it early.
func (s *Session) AuthorEvents(ctx context.Context, authorID, lastEventID string, fn func(data []byte) error) (string, error) {
	return s.coord.AuthorEvents(ctx, s.readToken(), s.name, authorID, lastEventID, fn)
}

// CancelAuthor cancels a running author task (raw 202 body).
func (s *Session) CancelAuthor(ctx context.Context, authorID string) (json.RawMessage, error) {
	return s.coord.CancelAuthor(ctx, s.computerToken(), s.name, authorID)
}
