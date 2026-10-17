package pinesandbox

import (
	"context"
	"encoding/json"

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

// ListSkills returns the served skills — curated + activated learned (raw array).
func (c *Computer) ListSkills(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ListSkills(ctx, ct)
}

// GetSkill returns one skill's metadata + SKILL.md body (raw): a skill authored
// on the Computer, or the computer-agent role guide. Pine's other skills are
// listed by name and description; their content is not served.
func (c *Computer) GetSkill(ctx context.Context, name string) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.GetSkill(ctx, ct, name)
}

// ListSkillDrafts returns pending drafts with their bodies (raw array).
func (c *Computer) ListSkillDrafts(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ListSkillDrafts(ctx, ct)
}

// ListSkillVersions returns visible versions for a name (raw array); name "" → all names.
func (c *Computer) ListSkillVersions(ctx context.Context, name string) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	if name == "" {
		return coord.ListAllSkillVersions(ctx, ct)
	}
	return coord.ListSkillVersions(ctx, ct, name)
}

// GetSkillVersion returns one version's metadata + SKILL.md body (raw).
func (c *Computer) GetSkillVersion(ctx context.Context, name, version string) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.GetSkillVersion(ctx, ct, name, version)
}

// ActivateSkill makes a reviewed draft version the one the agent is offered (raw).
func (c *Computer) ActivateSkill(ctx context.Context, name, version string) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ActivateSkill(ctx, ct, name, version)
}

// DeactivateSkill un-serves a skill, keeping the version (raw).
func (c *Computer) DeactivateSkill(ctx context.Context, name string) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.DeactivateSkill(ctx, ct, name)
}

// SetSkillEnabled switches one of Pine's skills that ListSkills marks
// switchable on or off for the agent processes this Computer starts next (raw
// result: the skill as the next agent is offered it, and whether the switch
// changed). Other Pine skills cannot be switched; learned skills use
// ActivateSkill and DeactivateSkill. A running agent keeps its catalog until
// its session's agent is reset.
func (c *Computer) SetSkillEnabled(ctx context.Context, name string, enabled bool) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.SetSkillEnabled(ctx, ct, name, enabled)
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

// DeleteSkillVersion hides a version, un-serving it if active (raw).
func (c *Computer) DeleteSkillVersion(ctx context.Context, name, version string) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.DeleteSkillVersion(ctx, ct, name, version)
}

// ---- session-scoped authoring (ct_) ----
//
// Authoring mutations require the Computer token (the agent itself can't author) and the
// author event stream accepts ct_ OR ps_, so the WHOLE lifecycle uses the Computer's ct_.
// That keeps it consistent (mirrors the agent mutations) and lets a ct_-only handle — e.g.
// one from Computer.Session(name), whose ps_ is redacted — both start authoring AND stream
// its progress.

// Learn authors a draft from the session's latest finished run; scaffold (optional) seeds
// the SKILL.md the author continues from (raw 202 body — drive AuthorEvents for progress).
func (s *Session) Learn(ctx context.Context, scaffold string) (json.RawMessage, error) {
	return s.coord.LearnSkill(ctx, s.computerToken(), s.name, scaffold)
}

// Teach authors a draft from a human demonstration toward goal (raw 202 body).
func (s *Session) Teach(ctx context.Context, goal string, opts TeachOptions) (json.RawMessage, error) {
	return s.coord.TeachSkill(ctx, s.computerToken(), s.name, goal, opts)
}

// Refine revises an existing immutable skill version with your guidance
// (raw 202 body — drive AuthorEvents for progress and the resulting draft).
func (s *Session) Refine(ctx context.Context, skill string, version int, guidance string) (json.RawMessage, error) {
	return s.coord.RefineSkill(ctx, s.computerToken(), s.name, skill, version, guidance)
}

// AuthorSkill registers a SKILL.md draft you wrote yourself (raw).
func (s *Session) AuthorSkill(ctx context.Context, skillName, skillMD, reason string, opts AuthorSkillOptions) (json.RawMessage, error) {
	return s.coord.AuthorSkillDraft(ctx, s.computerToken(), s.name, skillName, skillMD, reason, opts)
}

// AuthorEvents streams an in-flight author run; fn receives each event's raw data JSON.
// Returns the resume cursor; fn returning ErrStop stops cleanly. Uses the ct_, like the
// rest of authoring.
func (s *Session) AuthorEvents(ctx context.Context, authorID, lastEventID string, fn func(data []byte) error) (string, error) {
	return s.coord.AuthorEvents(ctx, s.computerToken(), s.name, authorID, lastEventID, fn)
}

// CancelAuthor cancels a running author task (raw 202 body).
func (s *Session) CancelAuthor(ctx context.Context, authorID string) (json.RawMessage, error) {
	return s.coord.CancelAuthor(ctx, s.computerToken(), s.name, authorID)
}
