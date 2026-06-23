package pinesandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDelegateDesktop_CreatesAndDelegates: an absent session is created (create:true), then
// a dt_ is minted and folded into a browser-safe envelope carrying NO ct_/ps_.
func TestDelegateDesktop_CreatesAndDelegates(t *testing.T) {
	var created bool
	coordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sessions/main" && r.Method == "GET":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(404)
			fmt.Fprint(w, `{"type":"/errors/session-not-found","status":404}`)
		case r.URL.Path == "/sessions" && r.Method == "POST":
			created = true
			fmt.Fprint(w, `{"session":{"name":"main","token":"ps_secret","browser":{"primary_tab_id":"t1"}}}`)
		case r.URL.Path == "/v1/sessions/main/desktop-token" && r.Method == "POST":
			if r.Header.Get("X-Pine-Auth") != "ct_x" {
				t.Errorf("desktop-token auth = %q, want ct_x", r.Header.Get("X-Pine-Auth"))
			}
			fmt.Fprint(w, `{"token":"dt_short","expires_at":"2026-01-01T00:00:30Z"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer coordSrv.Close()

	conn := buildTestConnection(t, "http://unused", coordSrv.URL)
	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(conn, "sb-1", "ct_x", "running"); err != nil {
		t.Fatal(err)
	}

	dc, err := comp.DelegateDesktop(context.Background(), "main", true, true)
	if err != nil {
		t.Fatalf("DelegateDesktop: %v", err)
	}
	if !created {
		t.Error("absent session should have been created")
	}
	if dc.SessionName != "main" || dc.DesktopToken != "dt_short" || dc.SpecVersion != SpecVersion {
		t.Errorf("envelope = %+v", dc)
	}
	if !strings.HasPrefix(dc.ComputerHost, "127.0.0.1") {
		t.Errorf("ComputerHost = %q, want the data host", dc.ComputerHost)
	}

	// The wire shape must NOT leak ct_/ps_ — only the dt_ + host + session name.
	wire, err := json.Marshal(dc)
	if err != nil {
		t.Fatal(err)
	}
	s := string(wire)
	if strings.Contains(s, "ct_x") || strings.Contains(s, "ps_secret") {
		t.Errorf("delegated wire leaked a server token: %s", s)
	}
	if !strings.Contains(s, "dt_short") || !strings.Contains(s, `"name":"main"`) {
		t.Errorf("wire missing dt_/session: %s", s)
	}
}
