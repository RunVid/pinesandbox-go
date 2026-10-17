package pinesandbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestAdoptSession_DrivesWithProvidedPs pins the stateless-reuse path: a *Session
// rebuilt from a persisted {name, ps_} (no coord round-trip — the session analog
// of Client.AdoptExisting) drives with the provided ps_ and routes control through
// the parent's ct_.
func TestAdoptSession_DrivesWithProvidedPs(t *testing.T) {
	const ct, ps = "ct_tok", "ps_persisted"
	var mu sync.Mutex
	auth := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth[r.Method+" "+r.URL.Path] = r.Header.Get("X-Pine-Auth")
		mu.Unlock()
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(buildTestConnection(t, "http://unused", srv.URL), "sb-1", ct, "running"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sess, err := comp.AdoptSession("s1", ps)
	if err != nil {
		t.Fatalf("AdoptSession: %v", err)
	}
	_, _ = sess.CreateTab(ctx, "https://x", "") // drive → provided ps_
	human := "human"
	_, _ = sess.UpdateControl(ctx, ControlPatch{Controller: &human, ActorType: "user_click"},
		PatchControlOptions{IfMatch: "v1"}) // control → ct_

	mu.Lock()
	defer mu.Unlock()
	if got := auth["POST /sessions/s1/tabs"]; got != ps {
		t.Errorf("adopted-session drive used %q, want the persisted ps_ %q", got, ps)
	}
	if got := auth["PATCH /v1/sessions/s1/control"]; got != ct {
		t.Errorf("adopted-session control used %q, want ct_ %q", got, ct)
	}
}

// An adopted Computer qualifies an old runtime's unrouted agent read, made
// with the session's ps_, through its own ct_.
func TestAdoptSession_AgentReadQualifiesWithComputerToken(t *testing.T) {
	const ct, ps = "ct_tok", "ps_persisted"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capabilities" && r.Header.Get("X-Pine-Auth") == ct {
			fmt.Fprint(w, `{}`)
			return
		}
		if r.URL.Path == "/v1/capabilities" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(buildTestConnection(t, "http://unused", srv.URL), "sb-1", ct, "running"); err != nil {
		t.Fatal(err)
	}
	sess, err := comp.AdoptSession("s1", ps)
	if err != nil {
		t.Fatal(err)
	}
	var unsupported *AgentProtocolUnsupportedError
	if _, err := sess.Agent().Status(context.Background(), TaskOptions{}); !errors.As(err, &unsupported) {
		t.Fatalf("status on an old runtime = %v, want AgentProtocolUnsupportedError", err)
	}
}
