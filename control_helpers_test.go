package pinesandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTakeControl_FetchesETagAndRetriesOn412 pins the TakeControl helper: it fetches
// the current ETag, PATCHes with If-Match, and on a stale-ETag 412 re-fetches and
// retries ONCE with a FRESH Idempotency-Key (the retry is a distinct request — must
// not reuse the rejected key), then returns the typed new state.
func TestTakeControl_FetchesETagAndRetriesOn412(t *testing.T) {
	var patches int
	var lastIfMatch, idemFirst, idemRetry string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet: // ControlState — ETag advances after the first (rejected) PATCH
			etag := `"v1"`
			if patches > 0 {
				etag = `"v2"`
			}
			w.Header().Set("ETag", etag)
			fmt.Fprint(w, `{"controller":"agent","epoch":1}`)
		case http.MethodPatch:
			patches++
			lastIfMatch = r.Header.Get("If-Match")
			if patches == 1 {
				idemFirst = r.Header.Get("Idempotency-Key")
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(412)
				fmt.Fprint(w, `{"type":"/errors/precondition-failed","status":412}`)
				return
			}
			idemRetry = r.Header.Get("Idempotency-Key")
			w.Header().Set("ETag", `"v3"`)
			fmt.Fprint(w, `{"controller":"human","epoch":2}`)
		}
	}))
	defer srv.Close()

	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(buildTestConnection(t, "http://unused", srv.URL), "sb-1", "ct_tok", "running"); err != nil {
		t.Fatal(err)
	}
	sess, err := comp.AdoptSession("s1", "ps_x")
	if err != nil {
		t.Fatal(err)
	}

	st, err := sess.TakeControl(context.Background())
	if err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	if st.Controller != "human" {
		t.Errorf("controller = %q, want human", st.Controller)
	}
	if patches != 2 {
		t.Errorf("PATCH attempts = %d, want 2 (one 412 + one success)", patches)
	}
	if lastIfMatch != `"v2"` {
		t.Errorf("retry If-Match = %q, want the re-fetched %q", lastIfMatch, `"v2"`)
	}
	if idemFirst == "" || idemRetry == "" || idemFirst == idemRetry {
		t.Errorf("412 retry must mint a FRESH Idempotency-Key (not reuse the rejected one): first=%q retry=%q", idemFirst, idemRetry)
	}
}

// TestTakeControl_AdminOverrideSetsForce: actor_type "admin_override" must send
// force=true (the coord's force ⇔ admin_override rule) — otherwise the override
// 400s. The 412-retry is also skipped for a forced patch (no If-Match contention).
func TestTakeControl_AdminOverrideSetsForce(t *testing.T) {
	var force, actorType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("ETag", `"v1"`)
			fmt.Fprint(w, `{"controller":"agent"}`)
		case http.MethodPatch:
			force = r.URL.Query().Get("force")
			var b struct {
				ActorType string `json:"actor_type"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			actorType = b.ActorType
			w.Header().Set("ETag", `"v2"`)
			fmt.Fprint(w, `{"controller":"human"}`)
		}
	}))
	defer srv.Close()

	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(buildTestConnection(t, "http://unused", srv.URL), "sb-1", "ct_tok", "running"); err != nil {
		t.Fatal(err)
	}
	sess, err := comp.AdoptSession("s1", "ps_x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.TakeControl(context.Background(), WithForce()); err != nil {
		t.Fatalf("TakeControl admin_override: %v", err)
	}
	if force != "true" {
		t.Errorf("admin_override must set force=true; got force=%q", force)
	}
	if actorType != "admin_override" {
		t.Errorf("actor_type = %q, want admin_override", actorType)
	}
}

// TestTakeControl_OptionsStayConsistent: WithForce() then WithActor(user_click) must
// NOT leave force=true with actor_type=user_click (force is derived from actor_type,
// so the last option wins consistently). Guards the WithForce-then-WithActor drift.
func TestTakeControl_OptionsStayConsistent(t *testing.T) {
	var force, actorType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("ETag", `"v1"`)
			fmt.Fprint(w, `{"controller":"agent"}`)
			return
		}
		force = r.URL.Query().Get("force")
		var b struct {
			ActorType string `json:"actor_type"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		actorType = b.ActorType
		w.Header().Set("ETag", `"v2"`)
		fmt.Fprint(w, `{"controller":"human"}`)
	}))
	defer srv.Close()

	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(buildTestConnection(t, "http://unused", srv.URL), "sb-1", "ct_tok", "running"); err != nil {
		t.Fatal(err)
	}
	sess, err := comp.AdoptSession("s1", "ps_x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.TakeControl(context.Background(), WithForce(), WithActor(ActorUserClick)); err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	if actorType != ActorUserClick {
		t.Errorf("last option must win: actor_type = %q, want user_click", actorType)
	}
	if force == "true" {
		t.Errorf("force must follow actor_type (user_click ⇒ no force), got force=%q", force)
	}
}
