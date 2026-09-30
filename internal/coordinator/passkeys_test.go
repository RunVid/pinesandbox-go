package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestPasskeyRoutesAndTypes(t *testing.T) {
	var bodies = map[string]map[string]string{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Pine-Auth") == "" {
			t.Errorf("%s %s missing auth", r.Method, r.URL.Path)
		}
		if r.Body != nil {
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) == nil {
				bodies[r.URL.Path] = body
			}
		}
		requestPath := r.URL.Path
		if r.URL.RawPath != "" {
			requestPath = r.URL.EscapedPath()
		}
		switch r.Method + " " + requestPath {
		case "GET /v1/passkeys":
			fmt.Fprint(w, `{"credentials":[{"id":"cred","rp_id":"example.com","user_handle":"dXNlcg","created_at":"2026-08-12T00:00:00Z"}]}`)
		case "GET /v1/passkeys/ceremonies":
			fmt.Fprint(w, `{"ceremonies":[{"id":"pwc_abc","kind":"create","rp_id":"example.com","origin":"https://example.com","created_at":"2026-08-12T00:00:00Z","expires_at":"2026-08-12T00:02:00Z","presented_at":"2026-08-12T00:00:01Z"}]}`)
		case "GET /v1/sessions/main/passkeys/ceremonies":
			fmt.Fprint(w, `{"ceremonies":[{"id":"pwc_session","kind":"create","rp_id":"example.com","origin":"https://example.com","session":"main","created_at":"2026-08-12T00:00:00Z","expires_at":"2026-08-12T00:02:00Z"}]}`)
		case "DELETE /v1/passkeys/cred%2Funsafe", "POST /v1/passkeys/ceremonies/pwc_abc", "POST /v1/sessions/main/passkeys/intents", "POST /v1/sessions/main/passkeys/ceremonies/pwc_session":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s (escaped %s)", r.Method, r.URL.Path, r.URL.EscapedPath())
		}
	})
	ctx := context.Background()

	credentials, err := c.ListPasskeys(ctx, "ct_token")
	if err != nil || len(credentials) != 1 || credentials[0].RPID != "example.com" || credentials[0].CreatedAt.IsZero() {
		t.Fatalf("ListPasskeys = %+v, %v", credentials, err)
	}
	ceremonies, err := c.ListPasskeyCeremonies(ctx, "ct_token")
	if err != nil || len(ceremonies) != 1 || ceremonies[0].ID != "pwc_abc" || ceremonies[0].ExpiresAt.IsZero() || ceremonies[0].PresentedAt == nil {
		t.Fatalf("ListPasskeyCeremonies = %+v, %v", ceremonies, err)
	}
	if err := c.DeletePasskey(ctx, "ct_token", "cred/unsafe"); err != nil {
		t.Fatal(err)
	}
	if err := c.DecidePasskeyCeremony(ctx, "ct_token", "pwc_abc", "approve_custodial"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeclarePasskeyIntent(ctx, "ps_token", "main", "example.com"); err != nil {
		t.Fatal(err)
	}
	sessionCeremonies, err := c.ListSessionPasskeyCeremonies(ctx, "ps_token", "main")
	if err != nil || len(sessionCeremonies) != 1 || sessionCeremonies[0].ID != "pwc_session" {
		t.Fatalf("ListSessionPasskeyCeremonies = %+v, %v", sessionCeremonies, err)
	}
	if err := c.DecideSessionPasskeyCeremony(ctx, "ps_token", "main", "pwc_session", "decline"); err != nil {
		t.Fatal(err)
	}
	if got := bodies["/v1/passkeys/ceremonies/pwc_abc"]["decision"]; got != "approve_custodial" {
		t.Errorf("decision = %q", got)
	}
	if got := bodies["/v1/sessions/main/passkeys/intents"]["rp_id"]; got != "example.com" {
		t.Errorf("rp_id = %q", got)
	}
	if got := bodies["/v1/sessions/main/passkeys/ceremonies/pwc_session"]["decision"]; got != "decline" {
		t.Errorf("session decision = %q", got)
	}
}

func TestPasskeyListsNormalizeNullToEmpty(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) })
	credentials, err := c.ListPasskeys(context.Background(), "ct")
	if err != nil || credentials == nil || len(credentials) != 0 {
		t.Fatalf("credentials = %#v, %v", credentials, err)
	}
	ceremonies, err := c.ListPasskeyCeremonies(context.Background(), "ct")
	if err != nil || ceremonies == nil || len(ceremonies) != 0 {
		t.Fatalf("ceremonies = %#v, %v", ceremonies, err)
	}
	sessionCeremonies, err := c.ListSessionPasskeyCeremonies(context.Background(), "ps", "main")
	if err != nil || sessionCeremonies == nil || len(sessionCeremonies) != 0 {
		t.Fatalf("session ceremonies = %#v, %v", sessionCeremonies, err)
	}
}
