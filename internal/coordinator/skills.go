package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"go.pinesandbox.io/computer/internal/base/problem"
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

// ActivateSkill serves a reviewed draft version (operator gate; raw). The version is a
// JSON integer (spec ActivateSkillRequest.version).
func (c *Client) ActivateSkill(ctx context.Context, token, name string, version int) (json.RawMessage, error) {
	return c.postJSON(ctx, "/v1/skills/"+url.PathEscape(name)+"/activate", token, map[string]any{"version": version})
}

// DeactivateSkill un-serves a skill, keeping the version (raw).
func (c *Client) DeactivateSkill(ctx context.Context, token, name string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/v1/skills/"+url.PathEscape(name)+"/deactivate", token, map[string]any{})
}

// SetSkillEnabled switches one of Pine's standard or feature skills on or off
// for the agent processes the Computer starts next (raw result).
func (c *Client) SetSkillEnabled(ctx context.Context, token, name string, enabled bool) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]bool{"enabled": enabled})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, "PUT", "/v1/skills/"+url.PathEscape(name)+"/enabled", token, body)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(resp.Body), nil
}

// Capabilities returns the Computer's capability manifest (raw).
func (c *Client) Capabilities(ctx context.Context, token string) (json.RawMessage, error) {
	return c.getJSON(ctx, "/v1/capabilities", token)
}

// RequirePublicAgent explicitly qualifies a binding for UI feature discovery.
// No cache, fallback or mutation retry.
func (c *Client) RequirePublicAgent(ctx context.Context, token string) error {
	raw, err := c.Capabilities(ctx, token)
	var apiErr *problem.APIError
	var gone *RuntimeGoneError
	if errors.As(err, &gone) {
		return err
	}
	if err != nil && !(errors.As(err, &apiErr) && apiErr.Status == 404) {
		return err
	}
	var manifest struct {
		Protocols struct {
			Agent []int `json:"agent"`
		} `json:"protocols"`
	}
	valid := err == nil && json.Unmarshal(raw, &manifest) == nil
	seen := map[int]bool{}
	for _, version := range manifest.Protocols.Agent {
		if version <= 0 || seen[version] {
			valid = false
		}
		seen[version] = true
	}
	if valid && seen[2] {
		return nil
	}
	return &AgentProtocolUnsupportedError{}
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
	return c.postJSON(ctx, "/v2/sessions/"+url.PathEscape(name)+"/learn", token, body)
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
	return c.postJSON(ctx, "/v2/sessions/"+url.PathEscape(name)+"/teach", token, body)
}

// RefineSkill revises an existing immutable skill version with operator guidance
// (raw 202 body; progress and the terminal outcome arrive through AuthorEvents).
func (c *Client) RefineSkill(ctx context.Context, token, name, skill string, version int, guidance string) (json.RawMessage, error) {
	body := map[string]any{"name": skill, "version": version, "guidance": guidance}
	return c.postJSON(ctx, "/v2/sessions/"+url.PathEscape(name)+"/refine", token, body)
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

// AuthorEvents streams public activity from a skill author run until its result event,
// handing fn each event's data JSON. The feed is finite but resumable: a stream that closes
// or drops before the result reopens from the last event id under the shared reconnect
// budget (ErrStreamLost when it is spent). It returns the resume cursor; fn returning an
// error (ErrStop to stop cleanly) ends the stream with that error, and so does ctx.
func (c *Client) AuthorEvents(ctx context.Context, token, name, authorID, lastEventID string, fn func(data []byte) error) (string, error) {
	path := "/v2/sessions/" + url.PathEscape(name) + "/skills/author/" + url.PathEscape(authorID) + "/events"
	cursor := lastEventID
	var failure error
	ended := false
	resumeStream(ctx, c, path, token, &cursor, decodeAgentEvent,
		func(ev AgentEvent) bool { ended = ev.Type == "result"; return ended },
		func(ev AgentEvent, err error) bool {
			if err == nil {
				err = fn(ev.Raw)
			}
			failure = err
			return err == nil
		})
	if failure == nil && !ended {
		failure = ctx.Err()
	}
	return cursor, failure
}

// CancelAuthor cancels a running author-codex task (raw 202 body).
func (c *Client) CancelAuthor(ctx context.Context, token, name, authorID string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/v2/sessions/"+url.PathEscape(name)+"/skills/author/"+url.PathEscape(authorID)+"/cancel", token, map[string]any{})
}
