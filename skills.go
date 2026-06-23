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

// ListSkills returns the served skills — curated + activated learned (raw array).
func (c *Computer) ListSkills(ctx context.Context) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.ListSkills(ctx, ct)
}

// GetSkill returns one skill's metadata + SKILL.md body (raw).
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

// ActivateSkill serves a reviewed draft version (operator gate; raw).
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

// DeleteSkillVersion hides a version, un-serving it if active (raw).
func (c *Computer) DeleteSkillVersion(ctx context.Context, name, version string) (json.RawMessage, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.DeleteSkillVersion(ctx, ct, name, version)
}

// ---- Session-level authoring (ps_) ----

// Learn authors a draft from the session's latest finished run; scaffold (optional) seeds
// the SKILL.md the author continues from (raw 202 body — drive AuthorEvents for progress).
func (s *Session) Learn(ctx context.Context, scaffold string) (json.RawMessage, error) {
	return s.coord.LearnSkill(ctx, s.token, s.name, scaffold)
}

// Teach authors a draft from a human demonstration toward goal (raw 202 body).
func (s *Session) Teach(ctx context.Context, goal string, opts TeachOptions) (json.RawMessage, error) {
	return s.coord.TeachSkill(ctx, s.token, s.name, goal, opts)
}

// AuthorSkill registers a BYOA-authored SKILL.md draft directly (raw).
func (s *Session) AuthorSkill(ctx context.Context, skillName, skillMD, reason string, opts AuthorSkillOptions) (json.RawMessage, error) {
	return s.coord.AuthorSkillDraft(ctx, s.token, s.name, skillName, skillMD, reason, opts)
}

// AuthorEvents streams an in-flight author run; fn receives each event's raw data JSON.
// Returns the resume cursor; fn returning ErrStop stops cleanly.
func (s *Session) AuthorEvents(ctx context.Context, authorID, lastEventID string, fn func(data []byte) error) (string, error) {
	return s.coord.AuthorEvents(ctx, s.token, s.name, authorID, lastEventID, fn)
}

// CancelAuthor cancels a running author task (raw 202 body).
func (s *Session) CancelAuthor(ctx context.Context, authorID string) (json.RawMessage, error) {
	return s.coord.CancelAuthor(ctx, s.token, s.name, authorID)
}
