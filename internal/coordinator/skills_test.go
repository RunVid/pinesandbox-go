package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"go.pinesandbox.io/computer/internal/base/problem"
)

func TestSkills_ServedAndVersions(t *testing.T) {
	var activateBody map[string]any
	var enabledBody map[string]any
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
		case r.URL.Path == "/v1/skills/pdf/enabled" && r.Method == "PUT":
			_ = json.NewDecoder(r.Body).Decode(&enabledBody)
			_, _ = io.WriteString(w, `{"skill":{"name":"pdf","enabled":false},"changed":true}`)
		case r.URL.Path == "/v1/capabilities" && r.Method == "GET":
			_, _ = io.WriteString(w, `{"features":[{"id":"media.generate","state":"enabled"}],"limits":[]}`)
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
	if _, err := c.ActivateSkill(ctx, "ct_", "book-flight", 2); err != nil {
		t.Fatalf("ActivateSkill: %v", err)
	}
	// spec ActivateSkillRequest.version is an integer; a JSON string is a 400.
	if activateBody["version"] != float64(2) {
		t.Errorf("activate body = %#v, want an integer version", activateBody)
	}
	if _, err := c.DeactivateSkill(ctx, "ct_", "book-flight"); err != nil {
		t.Fatalf("DeactivateSkill: %v", err)
	}
	if _, err := c.DeleteSkillVersion(ctx, "ct_", "book-flight", "1"); err != nil {
		t.Fatalf("DeleteSkillVersion: %v", err)
	}
	if r, err := c.SetSkillEnabled(ctx, "ct_", "pdf", false); err != nil || !contains(string(r), `"changed":true`) {
		t.Fatalf("SetSkillEnabled = %s, %v", r, err)
	}
	if enabled, ok := enabledBody["enabled"].(bool); !ok || enabled {
		t.Errorf("enabled body = %v, want {enabled:false}", enabledBody)
	}
	if m, err := c.Capabilities(ctx, "ct_"); err != nil || !contains(string(m), "media.generate") {
		t.Fatalf("Capabilities = %s, %v", m, err)
	}
}

func TestSkills_Authoring(t *testing.T) {
	var teachBody, refineBody, authorBody map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/capabilities":
			_, _ = io.WriteString(w, `{"protocols":{"agent":[2]}}`)
		case "/v2/sessions/s/learn":
			_, _ = io.WriteString(w, `{"author_id":"au1"}`)
		case "/v2/sessions/s/teach":
			_ = json.NewDecoder(r.Body).Decode(&teachBody)
			_, _ = io.WriteString(w, `{"author_id":"au2"}`)
		case "/v2/sessions/s/refine":
			_ = json.NewDecoder(r.Body).Decode(&refineBody)
			_, _ = io.WriteString(w, `{"author_id":"au3"}`)
		case "/v1/sessions/s/skills":
			_ = json.NewDecoder(r.Body).Decode(&authorBody)
			_, _ = io.WriteString(w, `{"draft":"d1"}`)
		case "/v2/sessions/s/skills/author/au1/cancel":
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
	if _, err := c.RefineSkill(ctx, "ps_", "s", "book-flight", 2, "add the empty-results gotcha"); err != nil {
		t.Fatalf("RefineSkill: %v", err)
	}
	if refineBody["name"] != "book-flight" || refineBody["version"] != float64(2) || refineBody["guidance"] != "add the empty-results gotcha" {
		t.Errorf("refine body = %v", refineBody)
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

// TestSkills_AuthorEvents: the author feed is finite but resumable — a stream that closes
// before the result reopens from the last event id after the reconnect backoff, and the
// result event ends it with the cursor.
func TestSkills_AuthorEvents(t *testing.T) {
	var opens []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/sessions/s/skills/author/au1/events" {
			t.Errorf("path = %s", r.URL.Path)
		}
		opens = append(opens, r.Header.Get("Last-Event-ID"))
		if len(opens) == 1 {
			_, _ = io.WriteString(w, "id: 1\ndata: {\"type\":\"progress\",\"event_id\":1}\n\n")
			return // closes before the result
		}
		_, _ = io.WriteString(w, "id: 2\ndata: {\"type\":\"result\",\"event_id\":2}\n\n")
		_, _ = io.WriteString(w, "id: 3\ndata: {\"type\":\"progress\",\"event_id\":3}\n\n")
	})
	var types []string
	last, err := c.AuthorEvents(context.Background(), "ps_", "s", "au1", "", func(data []byte) error {
		var ev struct{ Type string }
		_ = json.Unmarshal(data, &ev)
		types = append(types, ev.Type)
		return nil
	})
	if err != nil {
		t.Fatalf("AuthorEvents: %v", err)
	}
	if fmt.Sprint(types) != "[progress result]" || last != "2" {
		t.Errorf("events=%v cursor=%q, want [progress result] and 2 (nothing after the result)", types, last)
	}
	if fmt.Sprint(opens) != "[ 1]" {
		t.Errorf("opens sent Last-Event-ID %q, want none then 1", opens)
	}
	if sleeps := sleepsOf(t, c); fmt.Sprint(sleeps) != "[500ms]" {
		t.Errorf("sleeps = %v, want the 500ms reconnect backoff", sleeps)
	}
}

// TestSkills_AuthorEvents_StreamLost: a feed that keeps closing without delivering an
// event ends with ErrStreamLost after the shared reconnect budget.
func TestSkills_AuthorEvents_StreamLost(t *testing.T) {
	var opens atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { opens.Add(1) })
	_, err := c.AuthorEvents(context.Background(), "ps_", "s", "au1", "7", func([]byte) error { return nil })
	if !errors.Is(err, ErrStreamLost) || opens.Load() != defaultReconnectBudget+1 {
		t.Fatalf("err = %v after %d opens, want ErrStreamLost after %d", err, opens.Load(), defaultReconnectBudget+1)
	}
}

// TestSkills_AuthorEvents_ErrStop: returning ErrStop from the callback halts the author
// feed after the current event and surfaces ErrStop (the documented early-stop signal).
func TestSkills_AuthorEvents_ErrStop(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "id: 1\ndata: {\"n\":1}\n\n")
		_, _ = io.WriteString(w, "id: 2\ndata: {\"n\":2}\n\n")
	})
	n := 0
	last, err := c.AuthorEvents(context.Background(), "ps_", "s", "au1", "", func([]byte) error {
		n++
		return ErrStop
	})
	if !errors.Is(err, ErrStop) {
		t.Fatalf("err = %v, want ErrStop", err)
	}
	if n != 1 {
		t.Errorf("callback ran %d times, want 1 (stopped after first)", n)
	}
	if last != "1" {
		t.Errorf("cursor = %q, want 1", last)
	}
}

func TestExplicitAgentProtocolQualification(t *testing.T) {
	for _, manifest := range []string{`{}`, `null`, `{"protocols":null}`, `{"protocols":{"agent":[1]}}`, `{"protocols":{"agent":[2,2]}}`, `{"protocols":{"agent":[2,0]}}`, `{"protocols":{"agent":["2"]}}`, `{"protocols":{"agent":2}}`} {
		t.Run(manifest, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, manifest)
			})
			err := c.RequirePublicAgent(context.Background(), "ct_")
			var apiErr *problem.APIError
			var unsupported *AgentProtocolUnsupportedError
			if !errors.As(err, &unsupported) || errors.As(err, &apiErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	for _, status := range []int{401, 403, 404, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{}`)
			})
			err := c.RequirePublicAgent(context.Background(), "ct_")
			if status == 404 {
				var unsupported *AgentProtocolUnsupportedError
				if !errors.As(err, &unsupported) {
					t.Fatalf("got %v; want protocol refusal", err)
				}
				return
			}
			var apiErr *problem.APIError
			if !errors.As(err, &apiErr) || apiErr.Status != status {
				t.Fatalf("got %v; want status %d", err, status)
			}
		})
	}
}

// A routed gateway 404 is a gone Computer, not missing protocol discovery.
func TestAuthorQualificationPreservesGoneComputer(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"type":"/errors/sandbox-not-found","status":404,"detail":"runtime gone"}`)
	})
	err := c.RequirePublicAgent(context.Background(), "ct_")
	var gone *RuntimeGoneError
	if !errors.As(err, &gone) {
		t.Fatalf("got %T (%v), want RuntimeGoneError", err, err)
	}
}

func TestV2RequestsQualifyOnlyAmbiguousNotFound(t *testing.T) {
	for _, tc := range []struct {
		name, body, manifest string
		status, discoveries  int
		unsupported, gone    bool
	}{
		{"success", `{}`, `{}`, 202, 0, false, false},
		{"old runtime", "404 page not found\n", `{}`, 404, 1, true, false},
		{"supported runtime", "404 page not found\n", `{"protocols":{"agent":[2]}}`, 404, 1, false, false},
		{"missing resource", `{"type":"/errors/session-not-found","status":404}`, `{"protocols":{"agent":[2]}}`, 404, 0, false, false},
		{"gone computer", `{"type":"/errors/sandbox-not-found","status":404}`, `{}`, 404, 0, false, true},
		{"forbidden", `{"type":"/errors/feature-disabled","status":403}`, `{}`, 403, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, discovery := 0, 0
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/capabilities" {
					discovery++
					_, _ = io.WriteString(w, tc.manifest)
					return
				}
				calls++
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			ctx := context.Background()
			_, learn := c.LearnSkill(ctx, "ct_", "s", "")
			_, teach := c.TeachSkill(ctx, "ct_", "s", "g", TeachOptions{})
			_, refine := c.RefineSkill(ctx, "ct_", "s", "sk", 1, "g")
			_, cancel := c.CancelAuthor(ctx, "ct_", "s", "author")
			_, run := c.AgentRun(ctx, "ct_", "s", "g", AgentRunOptions{IdempotencyKey: "message-41"})
			for _, err := range []error{learn, teach, refine, cancel, run} {
				var unsupported *AgentProtocolUnsupportedError
				var gone *RuntimeGoneError
				if errors.As(err, &unsupported) != tc.unsupported || errors.As(err, &gone) != tc.gone {
					t.Fatalf("unexpected error %T: %v", err, err)
				}
				if tc.status == 202 && err != nil {
					t.Fatal(err)
				}
				if tc.status != 202 && !tc.unsupported {
					var apiErr *problem.APIError
					if !errors.As(err, &apiErr) || apiErr.Status != tc.status {
						t.Fatalf("lost original error: %v", err)
					}
				}
			}
			if calls != 5 || discovery != 5*tc.discoveries {
				t.Fatalf("calls=%d discovery=%d", calls, discovery)
			}
		})
	}
}

// A session-token read cannot read the capability manifest: an old runtime's
// unrouted V2 read is qualified with the Computer token instead.
func TestAgentReadQualifiesWithTheComputerToken(t *testing.T) {
	var discoveredWith []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capabilities" {
			discoveredWith = append(discoveredWith, bearer(r))
			if bearer(r) != "ct_" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "404 page not found\n")
	})
	c.QualifyWith("ct_")
	_, err := c.AgentTask(context.Background(), "ps_", "s", AgentTaskOptions{})
	var unsupported *AgentProtocolUnsupportedError
	if !errors.As(err, &unsupported) || len(discoveredWith) != 1 || discoveredWith[0] != "ct_" {
		t.Fatalf("err=%v discovered with %v", err, discoveredWith)
	}
}
