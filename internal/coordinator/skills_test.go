package coordinator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestSkills_ServedAndVersions(t *testing.T) {
	var activateBody map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/skills" && r.Method == "GET":
			_, _ = io.WriteString(w, `{"skills":[{"name":"book-flight"}]}`)
		case r.URL.Path == "/v1/skills/book-flight" && r.Method == "GET":
			_, _ = io.WriteString(w, `{"name":"book-flight","body":"# SKILL"}`)
		case r.URL.Path == "/v1/skills/drafts":
			_, _ = io.WriteString(w, `{"drafts":[{"name":"d1"}]}`)
		case r.URL.Path == "/v1/skills/versions":
			_, _ = io.WriteString(w, `{"versions":[{"v":1}]}`)
		case r.URL.Path == "/v1/skills/book-flight/versions" && r.Method == "GET":
			_, _ = io.WriteString(w, `{"versions":[{"v":1},{"v":2}]}`)
		case r.URL.Path == "/v1/skills/book-flight/versions/2" && r.Method == "GET":
			_, _ = io.WriteString(w, `{"v":2,"body":"# v2"}`)
		case r.URL.Path == "/v1/skills/book-flight/activate":
			_ = json.NewDecoder(r.Body).Decode(&activateBody)
			_, _ = io.WriteString(w, `{"active":2}`)
		case r.URL.Path == "/v1/skills/book-flight/deactivate":
			_, _ = io.WriteString(w, `{"active":null}`)
		case r.URL.Path == "/v1/skills/book-flight/versions/1" && r.Method == "DELETE":
			_, _ = io.WriteString(w, `{"deleted":1}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()

	if s, err := c.ListSkills(ctx, "ct_"); err != nil || !contains(string(s), "book-flight") {
		t.Fatalf("ListSkills = %s, %v", s, err)
	}
	if s, err := c.GetSkill(ctx, "ct_", "book-flight"); err != nil || !contains(string(s), "# SKILL") {
		t.Fatalf("GetSkill = %s, %v", s, err)
	}
	if d, err := c.ListSkillDrafts(ctx, "ct_"); err != nil || !contains(string(d), "d1") {
		t.Fatalf("ListSkillDrafts = %s, %v", d, err)
	}
	if v, err := c.ListAllSkillVersions(ctx, "ct_"); err != nil || !contains(string(v), `"v":1`) {
		t.Fatalf("ListAllSkillVersions = %s, %v", v, err)
	}
	vs, err := c.ListSkillVersions(ctx, "ct_", "book-flight")
	if err != nil {
		t.Fatalf("ListSkillVersions: %v", err)
	}
	var versions []map[string]any
	if json.Unmarshal(vs, &versions); len(versions) != 2 {
		t.Errorf("versions = %s", vs)
	}
	if gv, err := c.GetSkillVersion(ctx, "ct_", "book-flight", "2"); err != nil || !contains(string(gv), "# v2") {
		t.Fatalf("GetSkillVersion = %s, %v", gv, err)
	}
	if _, err := c.ActivateSkill(ctx, "ct_", "book-flight", "2"); err != nil {
		t.Fatalf("ActivateSkill: %v", err)
	}
	if activateBody["version"] != "2" {
		t.Errorf("activate body = %v", activateBody)
	}
	if _, err := c.DeactivateSkill(ctx, "ct_", "book-flight"); err != nil {
		t.Fatalf("DeactivateSkill: %v", err)
	}
	if _, err := c.DeleteSkillVersion(ctx, "ct_", "book-flight", "1"); err != nil {
		t.Fatalf("DeleteSkillVersion: %v", err)
	}
}

func TestSkills_Authoring(t *testing.T) {
	var teachBody, authorBody map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions/s/learn":
			_, _ = io.WriteString(w, `{"author_id":"au1"}`)
		case "/v1/sessions/s/teach":
			_ = json.NewDecoder(r.Body).Decode(&teachBody)
			_, _ = io.WriteString(w, `{"author_id":"au2"}`)
		case "/v1/sessions/s/skills":
			_ = json.NewDecoder(r.Body).Decode(&authorBody)
			_, _ = io.WriteString(w, `{"draft":"d1"}`)
		case "/v1/sessions/s/skills/author/au1/cancel":
			_, _ = io.WriteString(w, `{"cancelled":true}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	ctx := context.Background()

	if _, err := c.LearnSkill(ctx, "ps_", "s", "# seed"); err != nil {
		t.Fatalf("LearnSkill: %v", err)
	}
	tainted := true
	if _, err := c.TeachSkill(ctx, "ps_", "s", "book a flight", TeachOptions{HandoffID: "h1", Scaffold: "# seed"}); err != nil {
		t.Fatalf("TeachSkill: %v", err)
	}
	if teachBody["goal"] != "book a flight" || teachBody["handoff_id"] != "h1" {
		t.Errorf("teach body = %v", teachBody)
	}
	if _, err := c.AuthorSkillDraft(ctx, "ps_", "s", "book-flight", "# SKILL", "manual", AuthorSkillOptions{Origin: "byoa", Tainted: &tainted}); err != nil {
		t.Fatalf("AuthorSkillDraft: %v", err)
	}
	if authorBody["name"] != "book-flight" || authorBody["reason"] != "manual" || authorBody["tainted"] != true {
		t.Errorf("author body = %v", authorBody)
	}
	if _, err := c.CancelAuthor(ctx, "ps_", "s", "au1"); err != nil {
		t.Fatalf("CancelAuthor: %v", err)
	}
}

func TestSkills_AuthorEvents(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/s/skills/author/au1/events" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, "id: 1\ndata: {\"type\":\"progress\"}\n\n")
		_, _ = io.WriteString(w, "id: 2\ndata: {\"terminal\":true}\n\n")
	})
	var n int
	last, err := c.AuthorEvents(context.Background(), "ps_", "s", "au1", "", func(data []byte) error {
		n++
		return nil
	})
	if err != nil {
		t.Fatalf("AuthorEvents: %v", err)
	}
	if n != 2 || last != "2" {
		t.Errorf("events=%d cursor=%q, want 2/2", n, last)
	}
}
