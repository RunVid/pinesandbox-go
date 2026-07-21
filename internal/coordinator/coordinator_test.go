package coordinator

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/spec"
	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/bind"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	raw := transport.New("http", strings.TrimPrefix(srv.URL, "http://"))
	return NewClient(raw, 1)
}

func TestBindPubkey(t *testing.T) {
	pub := bytes.Repeat([]byte{0x01}, 32)
	enc := base64.RawURLEncoding.EncodeToString(pub)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/coord/bind-pubkey" || r.Method != "GET" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if _, ok := r.Header["X-Pine-Auth"]; ok {
			t.Error("bind-pubkey must be token-less")
		}
		if got := r.Header.Get("Computer-Spec-Version"); got != "1" {
			t.Errorf("spec-version header = %q", got)
		}
		fmt.Fprintf(w, `{"pod_uid":"pod-1","coord_boot_id":"boot-1","ephem_pub_x25519":%q,"fetched_at":1700000000}`, enc)
	})

	bp, err := c.BindPubkey(context.Background())
	if err != nil {
		t.Fatalf("BindPubkey: %v", err)
	}
	if bp.PodUID != "pod-1" || bp.CoordBootID != "boot-1" {
		t.Errorf("identity = %+v", bp)
	}
	if !bytes.Equal(bp.EphemPub, pub) {
		t.Errorf("EphemPub = %x, want %x", bp.EphemPub, pub)
	}
	if bp.FetchedAt == nil {
		t.Error("FetchedAt not parsed")
	}
}

func TestBindPubkey_BadKey(t *testing.T) {
	cases := map[string]string{
		"not base64url": `{"pod_uid":"p","coord_boot_id":"b","ephem_pub_x25519":"!!!not!!!"}`,
		"wrong length":  fmt.Sprintf(`{"pod_uid":"p","coord_boot_id":"b","ephem_pub_x25519":%q}`, base64.RawURLEncoding.EncodeToString([]byte{1, 2, 3})),
		"no identity":   `{"ephem_pub_x25519":""}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if _, err := c.BindPubkey(context.Background()); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestBind(t *testing.T) {
	var got map[string]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/coord/bind" || r.Method != "POST" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if _, ok := r.Header["X-Pine-Auth"]; ok {
			t.Error("bind must be token-less")
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"computer_token":"ct_xyz","epoch":7}`)
	})

	res, err := c.Bind(context.Background(), "bt-1", "pod-1", "boot-1", "cipher-b64", BindExtras{})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if res.ComputerToken != "ct_xyz" || res.Epoch != 7 {
		t.Errorf("result = %+v", res)
	}
	want := map[string]string{"pod_uid": "pod-1", "coord_boot_id": "boot-1", "ciphertext": "cipher-b64", "bind_token": "bt-1"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("body[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestBind_SurfacesPersistenceMode(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"computer_token":"ct_eph","epoch":1,"persistence_mode":"ephemeral"}`)
	})
	res, err := c.Bind(context.Background(), "bt", "pod", "boot", "ct", BindExtras{})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if res.PersistenceMode != "ephemeral" {
		t.Errorf("PersistenceMode = %q, want %q", res.PersistenceMode, "ephemeral")
	}
}

func TestCreateSession_BodyAndAuth(t *testing.T) {
	var body map[string]any
	var auth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions" || r.Method != "POST" {
			t.Errorf("got %s %s (want POST /sessions, no v1)", r.Method, r.URL.Path)
		}
		auth = r.Header.Get("X-Pine-Auth")
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"session":{"name":"s1","token":"ps_abc","browser":{"primary_tab_id":"t1","window_id":42,"ws_url":"ws://x"},"created_at":"2026-01-01T00:00:00Z"}}`)
	})

	sess, err := c.CreateSession(context.Background(), "ct_admin", CreateSessionOptions{Browser: true})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if auth != "ct_admin" {
		t.Errorf("X-Pine-Auth = %q, want ct_admin", auth)
	}
	// name + label omitted (empty); browser always present; blind omitted (false).
	if _, ok := body["name"]; ok {
		t.Errorf("name should be omitted when empty: %v", body)
	}
	if _, ok := body["label"]; ok {
		t.Errorf("label should be omitted when empty: %v", body)
	}
	if _, ok := body["blind"]; ok {
		t.Errorf("blind should be omitted when false: %v", body)
	}
	if b, ok := body["browser"].(bool); !ok || !b {
		t.Errorf("browser should always be present and true: %v", body)
	}
	if sess.Name != "s1" || sess.Token != "ps_abc" {
		t.Errorf("session = %+v", sess)
	}
	if sess.Browser == nil || sess.Browser.PrimaryTabID != "t1" || sess.Browser.OwnedTabIDs == nil {
		t.Errorf("browser = %+v", sess.Browser)
	}
}

func TestCreateSession_BlindSent(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"session":{"name":"s","blind":true}}`)
	})
	sess, err := c.CreateSession(context.Background(), "ct_", CreateSessionOptions{Name: "s", Blind: true})
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := body["blind"].(bool); !ok || !b {
		t.Errorf("blind should be sent true: %v", body)
	}
	if body["name"] != "s" {
		t.Errorf("name should be sent: %v", body)
	}
	if !sess.Blind {
		t.Errorf("parsed Blind = false, want true")
	}
}

func TestGetAndListSessions(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sessions/s 1" && r.Method == "GET": // decoded path; name had a space
			if esc := r.URL.EscapedPath(); esc != "/sessions/s%201" {
				t.Errorf("escaped path = %q, want /sessions/s%%201 (name path-escaped)", esc)
			}
			fmt.Fprint(w, `{"session":{"name":"s 1","token":"ps_1"}}`)
		case r.URL.Path == "/sessions" && r.Method == "GET":
			fmt.Fprint(w, `{"sessions":[{"name":"a"},{"name":"b"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	sess, err := c.GetSession(context.Background(), "ps_1", "s 1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Name != "s 1" {
		t.Errorf("session = %+v", sess)
	}
	list, err := c.ListSessions(context.Background(), "ct_")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 2 || list[0].Name != "a" || list[1].Name != "b" {
		t.Errorf("list = %+v", list)
	}
}

func TestDestroySession_CleanFlag(t *testing.T) {
	for _, clean := range []bool{false, true} {
		var gotQuery string
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "DELETE" {
				t.Errorf("method = %s", r.Method)
			}
			gotQuery = r.URL.RawQuery
			w.WriteHeader(204)
		})
		if err := c.DestroySession(context.Background(), "ct_", "s", clean); err != nil {
			t.Fatalf("Destroy(clean=%v): %v", clean, err)
		}
		if clean && gotQuery != "clean=true" {
			t.Errorf("clean=true → query %q, want clean=true", gotQuery)
		}
		if !clean && gotQuery != "" {
			t.Errorf("clean=false → query %q, want empty", gotQuery)
		}
	}
}

func TestRecreateTerminalAndFocus(t *testing.T) {
	for _, verb := range []string{"terminal/recreate", "focus"} {
		var gotBody string
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/"+verb) || r.Method != "POST" {
				t.Errorf("got %s %s", r.Method, r.URL.Path)
			}
			b := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(b)
			gotBody = string(b)
			w.WriteHeader(200)
		})
		var err error
		if verb == "focus" {
			err = c.Focus(context.Background(), "ps_", "s")
		} else {
			err = c.RecreateTerminal(context.Background(), "ps_", "s")
		}
		if err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		if gotBody != "{}" {
			t.Errorf("%s body = %q, want {}", verb, gotBody)
		}
	}
}

func TestEpoch_RawPassthrough(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"epoch":42,"primary_tab_id":"t1"}`)
	})
	raw, err := c.Epoch(context.Background(), "ps_", "s")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("epoch not raw JSON: %v", err)
	}
	if m["epoch"].(float64) != 42 {
		t.Errorf("epoch = %v", m["epoch"])
	}
}

func TestError_RFC9457Mapping(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(409)
		fmt.Fprint(w, `{"type":"/errors/terminal-lost","status":409,"detail":"recreate the terminal","request_id":"rid-9"}`)
	})
	var ae *problem.APIError
	if _, err := c.GetSession(context.Background(), "ps_", "s"); !errors.As(err, &ae) {
		t.Fatalf("err = %T (%v), want *problem.APIError", err, err)
	} else if ae.Status != 409 || ae.ProblemType != "/errors/terminal-lost" || ae.RequestID != "rid-9" {
		t.Errorf("APIError = %+v", ae)
	}
}

func TestSandboxNotFoundMapsToSandboxGoneError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"type":"/errors/sandbox-not-found","status":404,"detail":"gone","request_id":"rid-gone","retryable":false}`)
	})

	_, err := c.ListSessions(context.Background(), "ct_bound")
	var gone *SandboxGoneError
	if !errors.As(err, &gone) {
		t.Fatalf("sandbox-not-found → %T (%v), want *SandboxGoneError", err, err)
	}
	var ae *problem.APIError
	if !errors.As(err, &ae) || ae.ProblemType != "/errors/sandbox-not-found" || ae.RequestID != "rid-gone" {
		t.Errorf("wrapped APIError diagnostics lost: %+v", ae)
	}
	if msg := err.Error(); !strings.Contains(msg, "op=GET /sessions") || !strings.Contains(msg, "request_id=rid-gone") {
		t.Errorf("SandboxGoneError message lost operation/request context: %q", msg)
	}
}

func TestLegacyBareListSessions404MapsToSandboxGoneError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Computer not found", http.StatusNotFound)
	})

	_, err := c.ListSessions(context.Background(), "ct_bound")
	var gone *SandboxGoneError
	if !errors.As(err, &gone) {
		t.Fatalf("legacy bare gateway 404 → %T (%v), want *SandboxGoneError", err, err)
	}

	// A bare 404 on a named session is ambiguous on an old coordinator and
	// must remain opaque rather than being mislabeled as a missing sandbox.
	_, err = c.GetSession(context.Background(), "ct_bound", "missing")
	if errors.As(err, &gone) {
		t.Fatal("bare named-session 404 must remain opaque")
	}
}

func TestSessionNotFoundRemainsAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"type":"/errors/session-not-found","status":404,"detail":"missing session","retryable":false}`)
	})

	_, err := c.GetSession(context.Background(), "ct_bound", "gone")
	var gone *SandboxGoneError
	if errors.As(err, &gone) {
		t.Fatal("session-not-found must not be classified as a missing sandbox")
	}
	var ae *problem.APIError
	if !errors.As(err, &ae) || ae.ProblemType != "/errors/session-not-found" {
		t.Errorf("session-not-found → %T (%v), want *problem.APIError", err, err)
	}
}

// TestTokenRejectedOn401 verifies a token'd 401 maps to *bind.TokenRejectedError (a report
// of binding_auth_lost, never an attach instruction), with the underlying *problem.APIError
// still reachable.
func TestTokenRejectedOn401(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(401)
		fmt.Fprint(w, `{"type":"/errors/unauthorized","status":401,"detail":"token rejected"}`)
	})
	_, err := c.GetSession(context.Background(), "ps_stale", "s")
	var rr *bind.TokenRejectedError
	if !errors.As(err, &rr) {
		t.Fatalf("token'd 401 → %T (%v), want *bind.TokenRejectedError", err, err)
	}
	var ae *problem.APIError
	if !errors.As(err, &ae) || ae.Status != 401 {
		t.Errorf("underlying *problem.APIError not reachable via unwrap: %v", err)
	}
}

// TestNoTokenRejectedOnTokenless401 verifies a token-LESS 401 (bind/health) stays a plain
// APIError — the token-rejected class only applies to a bound token.
func TestNoTokenRejectedOnTokenless401(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"type":"/errors/x","status":401}`)
	})
	_, err := c.BindPubkey(context.Background()) // token-less route
	var rr *bind.TokenRejectedError
	if errors.As(err, &rr) {
		t.Error("token-less 401 must NOT be TokenRejectedError")
	}
	var ae *problem.APIError
	if !errors.As(err, &ae) {
		t.Errorf("want *problem.APIError, got %T", err)
	}
}

// TestMalformedTypedResponses: a 200 with a body that doesn't match the expected shape is an
// error, not a silently zero-valued struct.
func TestMalformedTypedResponses(t *testing.T) {
	cases := []struct {
		name string
		body string
		call func(c *Client) error
	}{
		{"create-session not json", `<<<`, func(c *Client) error {
			_, err := c.CreateSession(context.Background(), "ct_", CreateSessionOptions{})
			return err
		}},
		{"create-session missing session", `{}`, func(c *Client) error {
			_, err := c.CreateSession(context.Background(), "ct_", CreateSessionOptions{})
			return err
		}},
		{"list-sessions not json", `not json`, func(c *Client) error {
			_, err := c.ListSessions(context.Background(), "ct_")
			return err
		}},
		{"bind missing computer_token", `{"epoch":1}`, func(c *Client) error {
			_, err := c.Bind(context.Background(), "bt", "p", "b", "ct", BindExtras{})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })
			if err := tc.call(c); err == nil {
				t.Fatalf("%s: expected an error, got nil", tc.name)
			}
		})
	}
}

func TestSpecVersionMismatch(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Computer-Spec-Version", "2")
		fmt.Fprint(w, `{"sessions":[]}`)
	})
	var me *spec.MismatchError
	if _, err := c.ListSessions(context.Background(), "ct_"); !errors.As(err, &me) {
		t.Fatalf("err = %T (%v), want *spec.MismatchError", err, err)
	}
}
