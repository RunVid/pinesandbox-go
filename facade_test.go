package pinesandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/bindhpke"
	"go.pinesandbox.io/computer/internal/controlplane"
	"go.pinesandbox.io/computer/internal/coordinator"
	"go.pinesandbox.io/computer/internal/tokens"
)

type fakeTokenSource struct{}

func (fakeTokenSource) Token(context.Context, bool) (string, error) { return "jws", nil }

// buildTestConnection wires a Connection at two httptest servers: controlSrv (control plane
// + portal attach-credentials) and coordSrv (the data plane). newCoord is overridden to
// point at coordSrv (the real zone derives an un-routable gateway host).
func buildTestConnection(t *testing.T, controlURL, coordURL string) *Connection {
	t.Helper()
	controlT := transport.New("http", strings.TrimPrefix(controlURL, "http://"))
	attach, err := tokens.NewAttachCredentialsSource(controlT, "pk_test")
	if err != nil {
		t.Fatal(err)
	}
	cp := controlplane.NewClient(controlT, fakeTokenSource{}, 1)
	coordHost := strings.TrimPrefix(coordURL, "http://")
	return &Connection{
		controlPlane:   cp,
		attachProvider: attach,
		specMajor:      1,
		newCoord: func(string) (*coordinator.Client, error) {
			return coordinator.NewClient(transport.New("http", coordHost), 1), nil
		},
	}
}

// TestAttachEndToEnd drives the whole stack: register → create → wait → bind handshake
// (real HPKE seal, opened by the coord) → create session (ct_) → agent.Run (ct_) →
// drive.Observe (ps_). It asserts the bind plaintext shape and the ct_/ps_ token routing.
func TestAttachEndToEnd(t *testing.T) {
	kp, err := bindhpke.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	const ct, ps = "ct_pod", "ps_sess"
	var openedKeyVersion int
	var openedBrokerGrant string
	var agentAuth, observeAuth, createAuth string

	controlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/computers":
			w.WriteHeader(201)
			fmt.Fprint(w, `{"computer_id":"c1"}`)
		case r.Method == "POST" && r.URL.Path == "/sandboxes":
			w.WriteHeader(202)
			fmt.Fprint(w, `{"id":"sb-1","status":{"state":"Running"}}`)
		case r.Method == "POST" && r.URL.Path == "/v1/computers/c1/attach-credentials":
			fmt.Fprint(w, `{"bind_token":"bt_1","broker_grant":"bg_1"}`)
		default:
			t.Errorf("controlSrv: unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer controlSrv.Close()

	coordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/coord/bind-pubkey":
			fmt.Fprintf(w, `{"pod_uid":"pod-1","coord_boot_id":"boot-1","ephem_pub_x25519":%q}`,
				base64.RawURLEncoding.EncodeToString(kp.PublicKeyRaw()))
		case r.URL.Path == "/v1/coord/bind":
			var body struct {
				Ciphertext string `json:"ciphertext"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			raw, derr := base64.RawURLEncoding.DecodeString(body.Ciphertext)
			if derr != nil {
				t.Errorf("ciphertext not base64url: %v", derr)
			}
			pt, oerr := kp.Open(raw, bindhpke.Info("pod-1", "boot-1"), nil)
			if oerr != nil {
				t.Errorf("coord could not open the bind envelope: %v", oerr)
				w.WriteHeader(400)
				return
			}
			var p struct {
				CKC struct {
					Version int `json:"version"`
				} `json:"computer_key_current"`
				BrokerGrant string `json:"broker_grant"`
			}
			_ = json.Unmarshal(pt, &p)
			openedKeyVersion, openedBrokerGrant = p.CKC.Version, p.BrokerGrant
			fmt.Fprintf(w, `{"computer_token":%q,"epoch":1}`, ct)
		case r.URL.Path == "/sessions" && r.Method == "POST":
			createAuth = r.Header.Get("X-Pine-Auth")
			fmt.Fprintf(w, `{"session":{"name":"s1","token":%q}}`, ps)
		case r.URL.Path == "/v1/sessions/s1/agent/run":
			agentAuth = r.Header.Get("X-Pine-Auth")
			fmt.Fprint(w, `{"task_id":"t1","session":"s1","state":"running","goal":"do it","created_at":"2026-01-02T03:04:05Z"}`)
		case r.URL.Path == "/v1/sessions/s1/observe":
			observeAuth = r.Header.Get("X-Pine-Auth")
			fmt.Fprint(w, `{"tree":"..."}`)
		default:
			t.Errorf("coordSrv: unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer coordSrv.Close()

	conn := buildTestConnection(t, controlSrv.URL, coordSrv.URL)
	ctx := context.Background()

	// Register (as Client.CreateComputer does) + attach.
	if err := conn.attachProvider.RegisterComputer(ctx, "c1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	comp := newComputer("c1", []byte("0123456789abcdef0123456789abcdef"))
	if err := comp.Attach(ctx, conn, AttachOptions{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if comp.ComputerToken() != ct || comp.SandboxID() != "sb-1" {
		t.Errorf("computer not bound: token=%q sandbox=%q", comp.ComputerToken(), comp.SandboxID())
	}
	if openedKeyVersion != CurrentKeyVersion || openedBrokerGrant != "bg_1" {
		t.Errorf("bind plaintext: keyVersion=%d brokerGrant=%q", openedKeyVersion, openedBrokerGrant)
	}

	// Session + agent + drive, asserting token routing.
	sess, err := comp.CreateSession(ctx, CreateSessionOptions{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Token() != ps {
		t.Errorf("session token = %q, want %q", sess.Token(), ps)
	}
	task, err := sess.Agent().Run(ctx, "do it", RunOptions{})
	if err != nil {
		t.Fatalf("agent.Run: %v", err)
	}
	// Assert the facade→coordinator typed parse end-to-end (not just err==nil): the
	// run returns the typed Task, parsed from the wire, with the Raw escape hatch + a
	// tolerantly-parsed timestamp.
	if task.TaskID != "t1" || task.State != "running" || task.Goal != "do it" {
		t.Errorf("agent.Run typed task = %+v, want task_id=t1 state=running goal=\"do it\"", task)
	}
	if task.CreatedAt == nil || task.CreatedAt.Year() != 2026 {
		t.Errorf("agent.Run task.CreatedAt = %v, want parsed 2026 timestamp", task.CreatedAt)
	}
	if len(task.Raw) == 0 {
		t.Error("agent.Run task.Raw escape hatch is empty")
	}
	if _, err := sess.Drive().Observe(ctx); err != nil {
		t.Fatalf("drive.Observe: %v", err)
	}

	if createAuth != ct {
		t.Errorf("create-session auth = %q, want ct_ (%q)", createAuth, ct)
	}
	if agentAuth != ct {
		t.Errorf("agent.Run auth = %q, want ct_ (%q) — mutations are ct_-only", agentAuth, ct)
	}
	if observeAuth != ps {
		t.Errorf("drive.Observe auth = %q, want ps_ (%q) — drive is session-scoped", observeAuth, ps)
	}
}

// TestAttach_BindFailureCleansUp: a terminal bind error tears the pod down and surfaces the
// typed error (no leaked binding).
func TestAttach_BindFailureCleansUp(t *testing.T) {
	kp, _ := bindhpke.GenerateKeypair()
	var destroyed bool
	controlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sandboxes" && r.Method == "POST":
			w.WriteHeader(202)
			fmt.Fprint(w, `{"id":"sb-1","status":{"state":"Running"}}`)
		case r.URL.Path == "/v1/computers/c1/attach-credentials":
			fmt.Fprint(w, `{"bind_token":"bt","broker_grant":"bg"}`)
		case r.Method == "DELETE" && r.URL.Path == "/sandboxes/sb-1":
			destroyed = true
			w.WriteHeader(204)
		default:
			w.WriteHeader(500)
		}
	}))
	defer controlSrv.Close()
	coordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/coord/bind-pubkey":
			fmt.Fprintf(w, `{"pod_uid":"pod-1","coord_boot_id":"boot-1","ephem_pub_x25519":%q}`,
				base64.RawURLEncoding.EncodeToString(kp.PublicKeyRaw()))
		case "/v1/coord/bind":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(403)
			fmt.Fprint(w, `{"type":"/errors/bind-rejected","status":403,"detail":"bad bind token"}`)
		default:
			w.WriteHeader(500)
		}
	}))
	defer coordSrv.Close()

	conn := buildTestConnection(t, controlSrv.URL, coordSrv.URL)
	comp := newComputer("c1", []byte("0123456789abcdef0123456789abcdef"))
	err := comp.Attach(context.Background(), conn, AttachOptions{})
	var ba *BindAuthError
	if !errors.As(err, &ba) {
		t.Fatalf("err = %T (%v), want *BindAuthError", err, err)
	}
	if !destroyed {
		t.Error("a terminal bind failure must tear the single-use pod down")
	}
	if comp.SandboxID() != "" || comp.ComputerToken() != "" {
		t.Error("bound state leaked after a failed attach")
	}
}

// TestRefreshBrokerGrant drives the 3-step §6.4 flow: re-fetch pod identity (bind-pubkey) →
// mint a fresh grant at the portal → coord swaps its in-RAM grant.
func TestRefreshBrokerGrant(t *testing.T) {
	kp, _ := bindhpke.GenerateKeypair()
	var portalMinted, coordSwapped bool
	controlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/computers/c1/grant-refresh" {
			portalMinted = true
			fmt.Fprint(w, `{"broker_grant":"bg_new","refresh_token":"rt_new"}`)
			return
		}
		w.WriteHeader(500)
	}))
	defer controlSrv.Close()
	coordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/coord/bind-pubkey":
			fmt.Fprintf(w, `{"pod_uid":"pod-1","coord_boot_id":"boot-1","ephem_pub_x25519":%q}`,
				base64.RawURLEncoding.EncodeToString(kp.PublicKeyRaw()))
		case "/v1/coord/grant":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["broker_grant"] != "bg_new" || body["refresh_token"] != "rt_new" {
				t.Errorf("coord grant body = %v", body)
			}
			if r.Header.Get("X-Pine-Auth") != "ct_x" {
				t.Errorf("grant auth = %q, want ct_x", r.Header.Get("X-Pine-Auth"))
			}
			coordSwapped = true
			fmt.Fprint(w, `{"expires_at":1700000000}`)
		default:
			w.WriteHeader(500)
		}
	}))
	defer coordSrv.Close()

	conn := buildTestConnection(t, controlSrv.URL, coordSrv.URL)
	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(conn, "sb-1", "ct_x", "running"); err != nil {
		t.Fatal(err)
	}
	out, err := comp.RefreshBrokerGrant(context.Background(), nil)
	if err != nil {
		t.Fatalf("RefreshBrokerGrant: %v", err)
	}
	if !portalMinted || !coordSwapped {
		t.Errorf("flow incomplete: portalMinted=%v coordSwapped=%v", portalMinted, coordSwapped)
	}
	if !strings.Contains(string(out), "expires_at") {
		t.Errorf("out = %s", out)
	}
}

func TestNewClient_Validation(t *testing.T) {
	if _, err := NewClient(ClientOptions{APIKey: "pk_x"}); err == nil {
		t.Error("expected error for missing Endpoint")
	}
	if _, err := NewClient(ClientOptions{Endpoint: "https://x.io"}); err == nil {
		t.Error("expected error for missing APIKey")
	}
	if _, err := NewClient(ClientOptions{Endpoint: "https://staging.pinesandbox.io", APIKey: "pk_x"}); err != nil {
		t.Errorf("valid options errored: %v", err)
	}
}

func TestValidateIdentity(t *testing.T) {
	if err := validateIdentity("", make([]byte, 32)); err == nil {
		t.Error("empty id should be rejected")
	}
	if err := validateIdentity("c1", make([]byte, 16)); err == nil {
		t.Error("16-byte key should be rejected (must be 32)")
	}
	if err := validateIdentity("c1", make([]byte, 32)); err != nil {
		t.Errorf("valid identity errored: %v", err)
	}
}

// TestComputer_NotAttached: session/computer ops on an un-attached Computer error clearly
// (no nil-deref of the coordinator).
func TestComputer_NotAttached(t *testing.T) {
	comp := newComputer("c1", make([]byte, 32))
	if _, err := comp.CreateSession(context.Background(), CreateSessionOptions{}); err == nil {
		t.Error("CreateSession on an un-attached Computer should error")
	}
	if _, err := comp.Capture(context.Background()); err == nil {
		t.Error("Capture on an un-attached Computer should error")
	}
}

// TestStop_CapturesThenTerminates verifies Stop takes a durable checkpoint BEFORE deleting
// the pod (closes the epoch race that silently loses state).
func TestStop_CapturesThenTerminates(t *testing.T) {
	var captured, destroyed bool
	controlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE" && r.URL.Path == "/sandboxes/sb-1":
			if !captured {
				t.Error("pod deleted BEFORE the pre-terminate checkpoint")
			}
			destroyed = true
			w.WriteHeader(204)
		case r.Method == "GET" && r.URL.Path == "/sandboxes/sb-1":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(404) // confirm-gone
			fmt.Fprint(w, `{"type":"/errors/not-found","status":404}`)
		default:
			w.WriteHeader(500)
		}
	}))
	defer controlSrv.Close()
	coordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/coord/capture" {
			if r.Header.Get("X-Pine-Auth") != "ct_x" {
				t.Errorf("capture auth = %q, want ct_x", r.Header.Get("X-Pine-Auth"))
			}
			captured = true
			fmt.Fprint(w, `{"snapshot_id":"snap-1"}`)
			return
		}
		w.WriteHeader(500)
	}))
	defer coordSrv.Close()

	conn := buildTestConnection(t, controlSrv.URL, coordSrv.URL)
	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(conn, "sb-1", "ct_x", "running"); err != nil {
		t.Fatal(err)
	}
	gone, err := comp.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !captured || !destroyed || !gone {
		t.Errorf("captured=%v destroyed=%v gone=%v, want all true", captured, destroyed, gone)
	}
	if comp.SandboxID() != "" {
		t.Error("binding not cleared after Stop")
	}
}

// TestSession_TokenRouting pins the ct_ vs ps_ choice across representative methods: agent
// mutations + desktop-token use the Computer's ct_; agent reads + drive use the session ps_.
func TestSession_TokenRouting(t *testing.T) {
	const ct, ps = "ct_x", "ps_sess"
	auth := map[string]string{}
	coordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth[r.Method+" "+r.URL.Path] = r.Header.Get("X-Pine-Auth")
		switch r.URL.Path {
		case "/sessions":
			fmt.Fprintf(w, `{"session":{"name":"s1","token":%q}}`, ps)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer coordSrv.Close()

	conn := buildTestConnection(t, "http://unused", coordSrv.URL)
	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(conn, "sb-1", ct, "running"); err != nil {
		t.Fatal(err)
	}
	sess, err := comp.CreateSession(context.Background(), CreateSessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, _ = sess.Agent().Steer(ctx, "go", SteerOptions{})
	_, _ = sess.Agent().Cancel(ctx)
	_, _ = sess.Agent().Status(ctx)
	_, _ = sess.Drive().Observe(ctx)
	_, _ = sess.DesktopToken(ctx)

	wantCt := []string{"POST /sessions", "POST /v1/sessions/s1/agent/steer", "POST /v1/sessions/s1/agent/cancel", "POST /v1/sessions/s1/desktop-token"}
	wantPs := []string{"GET /v1/sessions/s1/agent", "POST /v1/sessions/s1/observe"}
	for _, k := range wantCt {
		if auth[k] != ct {
			t.Errorf("%s used %q, want ct_ (%q)", k, auth[k], ct)
		}
	}
	for _, k := range wantPs {
		if auth[k] != ps {
			t.Errorf("%s used %q, want ps_ (%q)", k, auth[k], ps)
		}
	}
}

// TestRedaction ensures a struct dump of a Computer / Credentials in a log can't leak the
// state key or ct_ (across %v, %+v, %#v).
func TestRedaction(t *testing.T) {
	const keyStr = "0123456789abcdef0123456789abcdef"
	creds := Credentials{ID: "c1", Key: []byte(keyStr)}
	for _, s := range []string{fmt.Sprintf("%v", creds), fmt.Sprintf("%+v", creds), fmt.Sprintf("%#v", creds)} {
		if strings.Contains(s, keyStr) {
			t.Errorf("Credentials leaked the key: %s", s)
		}
	}
	comp := newComputer("c1", []byte(keyStr))
	comp.computerToken = "ct_secret"
	for _, s := range []string{fmt.Sprintf("%v", comp), fmt.Sprintf("%+v", comp), fmt.Sprintf("%#v", comp)} {
		if strings.Contains(s, "ct_secret") || strings.Contains(s, keyStr) {
			t.Errorf("Computer leaked a secret: %s", s)
		}
	}
}

func TestAddPriorKey_RejectsCurrentVersion(t *testing.T) {
	c := newComputer("c1", make([]byte, 32))
	if err := c.AddPriorKey(CurrentKeyVersion, make([]byte, 32)); err == nil {
		t.Error("AddPriorKey must reject the current key version")
	}
	if err := c.AddPriorKey(0, make([]byte, 32)); err != nil {
		t.Errorf("AddPriorKey(0): %v", err)
	}
}
