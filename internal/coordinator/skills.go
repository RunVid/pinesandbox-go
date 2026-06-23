package coordinator

import (
	"context"
	"encoding/json"
	"net/url"
)

// ---- served skills (Computer-level) ----

// ListSkills returns the served skills — curated + activated learned (raw "skills" array).
func (c *Client) ListSkills(ctx context.Context, token string) (json.RawMessage, error) {
	return c.envelopeField(ctx, "GET", "/v1/skills", token, nil, "skills")
}

// GetSkill returns one skill's metadata + full SKILL.md body (raw).
func (c *Client) GetSkill(ctx context.Context, token, name string) (json.RawMessage, error) {
	return c.getJSON(ctx, "/v1/skills/"+url.PathEscape(name), token)
}

// ListSkillDrafts returns pending drafts, each with its body (raw "drafts" array).
func (c *Client) ListSkillDrafts(ctx context.Context, token string) (json.RawMessage, error) {
	return c.envelopeField(ctx, "GET", "/v1/skills/drafts", token, nil, "drafts")
}

// ListAllSkillVersions returns all visible learned versions, metadata only (raw "versions").
func (c *Client) ListAllSkillVersions(ctx context.Context, token string) (json.RawMessage, error) {
	return c.envelopeField(ctx, "GET", "/v1/skills/versions", token, nil, "versions")
}

// ListSkillVersions returns visible versions for one name, metadata only (raw "versions").
func (c *Client) ListSkillVersions(ctx context.Context, token, name string) (json.RawMessage, error) {
	return c.envelopeField(ctx, "GET", "/v1/skills/"+url.PathEscape(name)+"/versions", token, nil, "versions")
}

// GetSkillVersion returns one version's metadata + SKILL.md body (raw).
func (c *Client) GetSkillVersion(ctx context.Context, token, name, version string) (json.RawMessage, error) {
	return c.getJSON(ctx, "/v1/skills/"+url.PathEscape(name)+"/versions/"+url.PathEscape(version), token)
}

// ActivateSkill serves a reviewed draft version (operator gate; raw).
func (c *Client) ActivateSkill(ctx context.Context, token, name, version string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/v1/skills/"+url.PathEscape(name)+"/activate", token, map[string]any{"version": version})
}

// DeactivateSkill un-serves a skill, keeping the version (raw).
func (c *Client) DeactivateSkill(ctx context.Context, token, name string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/v1/skills/"+url.PathEscape(name)+"/deactivate", token, map[string]any{})
}

// DeleteSkillVersion hides a version and un-serves it if active (raw).
func (c *Client) DeleteSkillVersion(ctx context.Context, token, name, version string) (json.RawMessage, error) {
	resp, err := c.do(ctx, "DELETE", "/v1/skills/"+url.PathEscape(name)+"/versions/"+url.PathEscape(version), token, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(resp.Body), nil
}

// ---- authoring (Session-level) ----

// LearnSkill authors a draft from the session's latest finished run. scaffold (optional) is
// an operator-seeded SKILL.md the author continues from (raw 202 body).
func (c *Client) LearnSkill(ctx context.Context, token, name, scaffold string) (json.RawMessage, error) {
	body := map[string]any{}
	if scaffold != "" {
		body["scaffold"] = scaffold
	}
	return c.postJSON(ctx, "/v1/sessions/"+url.PathEscape(name)+"/learn", token, body)
}

// TeachOptions are the optional fields for TeachSkill.
type TeachOptions struct {
	HandoffID      string
	Clarifications any
	Scaffold       string
}

// TeachSkill authors a draft from a human demonstration toward goal (raw 202 body).
func (c *Client) TeachSkill(ctx context.Context, token, name, goal string, opts TeachOptions) (json.RawMessage, error) {
	body := map[string]any{"goal": goal}
	if opts.HandoffID != "" {
		body["handoff_id"] = opts.HandoffID
	}
	if opts.Clarifications != nil {
		body["clarifications"] = opts.Clarifications
	}
	if opts.Scaffold != "" {
		body["scaffold"] = opts.Scaffold
	}
	return c.postJSON(ctx, "/v1/sessions/"+url.PathEscape(name)+"/teach", token, body)
}

// AuthorSkillOptions are the optional fields for AuthorSkillDraft.
type AuthorSkillOptions struct {
	Origin  string
	Tainted *bool
}

// AuthorSkillDraft registers a BYOA-authored SKILL.md draft directly. skillName is the
// skill's kebab-case name (also in its frontmatter); name is the session (raw).
func (c *Client) AuthorSkillDraft(ctx context.Context, token, name, skillName, skillMD, reason string, opts AuthorSkillOptions) (json.RawMessage, error) {
	body := map[string]any{"name": skillName, "skill_md": skillMD, "reason": reason}
	if opts.Origin != "" {
		body["origin"] = opts.Origin
	}
	if opts.Tainted != nil {
		body["tainted"] = *opts.Tainted
	}
	return c.postJSON(ctx, "/v1/sessions/"+url.PathEscape(name)+"/skills", token, body)
}

// AuthorEvents streams an in-flight author-codex run (same SSE wire as agent events).
func (c *Client) AuthorEvents(ctx context.Context, token, name, authorID, lastEventID string, fn func(data []byte) error) (string, error) {
	return c.streamJSONEvents(ctx, "/v1/sessions/"+url.PathEscape(name)+"/skills/author/"+url.PathEscape(authorID)+"/events", token, lastEventID, fn)
}

// CancelAuthor cancels a running author-codex task (raw 202 body).
func (c *Client) CancelAuthor(ctx context.Context, token, name, authorID string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/v1/sessions/"+url.PathEscape(name)+"/skills/author/"+url.PathEscape(authorID)+"/cancel", token, map[string]any{})
}
