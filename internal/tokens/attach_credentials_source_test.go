package tokens

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.pinesandbox.io/computer/internal/base/transport"
)

func newAttachSource(t *testing.T, handler http.HandlerFunc) *AttachCredentialsSource {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := transport.New("http", strings.TrimPrefix(srv.URL, "http://"))
	s, err := NewAttachCredentialsSource(client, "pk_test")
	if err != nil {
		t.Fatalf("NewAttachCredentialsSource: %v", err)
	}
	return s
}

func TestAttach_NewRejectsEmpty(t *testing.T) {
	if _, err := NewAttachCredentialsSource(transport.New("http", "x"), ""); err == nil {
		t.Fatal("expected error for empty api_key")
	}
}

func validCredentialsRequest() CredentialsRequest {
	return CredentialsRequest{
		ComputerID: "c", PodUID: "p", CoordBootID: "b", SandboxID: "s",
		PKComputer: "cGs", KeyGeneration: 1,
		ExpectedBindingRevision: 0, IdempotencyKey: "attach-test-key",
	}
}

func TestRegisterComputer(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]string
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(201)
		fmt.Fprint(w, `{"computer_id":"c1"}`)
	})
	if err := s.RegisterComputer(context.Background(), "c1"); err != nil {
		t.Fatalf("RegisterComputer: %v", err)
	}
	if gotAuth != "Bearer pk_test" || gotPath != "/v1/computers" {
		t.Errorf("auth=%q path=%q", gotAuth, gotPath)
	}
	if gotBody["computer_id"] != "c1" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestRegisterComputer_Conflict(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		fmt.Fprint(w, `{"message":"owned by another project"}`)
	})
	var e *ComputerRegistrationError
	if err := s.RegisterComputer(context.Background(), "c1"); !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *ComputerRegistrationError", err, err)
	} else if e.Status != 409 {
		t.Errorf("status = %d", e.Status)
	}
}

func TestCredentials(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Idempotency-Key") != "attach-test-key" {
			t.Errorf("Idempotency-Key = %q", r.Header.Get("Idempotency-Key"))
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{"bind_token":"bt_1","broker_grant":"bg_1","key_assertion":"ka_1","binding_revision":1}`)
	})
	req := validCredentialsRequest()
	req.ComputerID, req.PodUID, req.CoordBootID, req.SandboxID = "c1", "pod-1", "boot-1", "sb-1"
	cr, err := s.Credentials(context.Background(), req)
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if cr.BindToken != "bt_1" || cr.BrokerGrant != "bg_1" {
		t.Errorf("creds = %+v", cr)
	}
	if gotPath != "/v1/computers/c1/attach-credentials" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["pod_uid"] != "pod-1" || gotBody["sandbox_id"] != "sb-1" || gotBody["expected_binding_revision"].(float64) != 0 {
		t.Errorf("body = %v", gotBody)
	}
	if _, ok := gotBody["profile"]; ok {
		t.Errorf("profile leaked into attach body: %v", gotBody)
	}
}

func TestCredentials_OmitsOptional(t *testing.T) {
	var gotBody map[string]any
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{"bind_token":"b","broker_grant":"g","key_assertion":"k","binding_revision":1}`)
	})
	if _, err := s.Credentials(context.Background(), validCredentialsRequest()); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["profile"]; ok {
		t.Errorf("profile should be omitted: %v", gotBody)
	}
	if _, ok := gotBody["ttl_seconds"]; ok {
		t.Errorf("ttl_seconds should be omitted: %v", gotBody)
	}
}

// TestCredentials_EphemeralOmitsCaptureIdentity proves an access-lease-only mint
// sends NO pk_computer/key_generation and does not require them up front.
func TestCredentials_EphemeralOmitsCaptureIdentity(t *testing.T) {
	var gotBody map[string]any
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{"bind_token":"b","broker_grant":"g","binding_revision":1}`)
	})
	req := CredentialsRequest{
		ComputerID: "c", PodUID: "p", CoordBootID: "b", SandboxID: "s",
		ExpectedBindingRevision: 0, IdempotencyKey: "attach-eph-key",
		Ephemeral: true, // no PKComputer / KeyGeneration
	}
	creds, err := s.Credentials(context.Background(), req)
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if creds.BindToken != "b" || creds.BrokerGrant != "g" || creds.KeyAssertion != "" {
		t.Errorf("ephemeral creds = %+v", creds)
	}
	if _, ok := gotBody["pk_computer"]; ok {
		t.Errorf("ephemeral body leaked pk_computer: %v", gotBody)
	}
	if _, ok := gotBody["key_generation"]; ok {
		t.Errorf("ephemeral body leaked key_generation: %v", gotBody)
	}
	if gotBody["persistence_mode"] != "ephemeral" {
		t.Errorf("ephemeral body must signal persistence_mode=ephemeral for lazy-create: %v", gotBody)
	}
}

func TestCredentials_404Unknown(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	var e *UnknownComputerError
	if _, err := s.Credentials(context.Background(), validCredentialsRequest()); !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *UnknownComputerError", err, err)
	}
}

func TestCredentials_BindingRevisionConflictIsTerminalAndCarriesWinner(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(412)
		fmt.Fprint(w, `{"type":"urn:pinesandbox:problem:binding-revision-conflict","status":412,"detail":"reload","reason":"binding_revision_changed","retryable":false,"current_binding_revision":7,"current_sandbox_id":"sb-winner"}`)
	})
	var e *BindingRevisionConflictError
	if _, err := s.Credentials(context.Background(), validCredentialsRequest()); !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *BindingRevisionConflictError", err, err)
	}
	if e.CurrentRevision == nil || *e.CurrentRevision != 7 || e.CurrentSandboxID != "sb-winner" {
		t.Fatalf("winner = revision %v sandbox %q", e.CurrentRevision, e.CurrentSandboxID)
	}
	if e.Code != "urn:pinesandbox:problem:binding-revision-conflict" || e.Reason != "binding_revision_changed" {
		t.Fatalf("code/reason = %q/%q", e.Code, e.Reason)
	}
}

func TestCredentials_MissingFields(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"bind_token":"b"}`) // missing broker_grant
	})
	var e *AttachCredentialsError
	if _, err := s.Credentials(context.Background(), validCredentialsRequest()); !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *AttachCredentialsError", err, err)
	}
}

func TestCredentials_PreservesPartialCommittedReceipt(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"bind_token":"b","binding_revision":1}`)
	})
	creds, err := s.Credentials(context.Background(), validCredentialsRequest())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if creds.BindingRevision != 1 || creds.BindToken != "b" || creds.BrokerGrant != "" {
		t.Fatalf("partial committed receipt = %+v", creds)
	}
}

// A bad key (401) stays in the AttachCredentialsError family so a caller's
// errors.As(*AttachCredentialsError) on the attach call keeps catching it; only the
// project/computer-status 403 is broken out as a distinct *ProjectAccessDenied.
func TestCredentials_403ProjectAccessDenied(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	var e *ProjectAccessDenied
	if _, err := s.Credentials(context.Background(), validCredentialsRequest()); !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *ProjectAccessDenied", err, err)
	} else if e.Status != 403 {
		t.Errorf("status = %d, want 403", e.Status)
	}
}

// 403 is NOT swallowed by the generic AttachCredentialsError type — it's the more
// specific ProjectAccessDenied (Go has no inheritance, so the two are distinct).
func TestCredentials_403IsNotGenericAttachError(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	_, err := s.Credentials(context.Background(), validCredentialsRequest())
	var generic *AttachCredentialsError
	if errors.As(err, &generic) {
		t.Errorf("403 → %T, want a distinct *ProjectAccessDenied, not *AttachCredentialsError", err)
	}
}

func TestCredentials_GenericByStatus(t *testing.T) {
	// 401 (bad key), 429, and 5xx all land in the generic AttachCredentialsError family
	// (only 403 → ProjectAccessDenied, 404 → UnknownComputerError are broken out).
	for _, status := range []int{401, 429, 500} {
		s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		var e *AttachCredentialsError
		if _, err := s.Credentials(context.Background(), validCredentialsRequest()); !errors.As(err, &e) {
			t.Errorf("status %d → %T, want *AttachCredentialsError", status, err)
		} else if e.Status != status {
			t.Errorf("status = %d, want %d", e.Status, status)
		}
	}
}

func TestAttachSource_StringRedacts(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {})
	if strings.Contains(s.String(), "pk_test") {
		t.Errorf("String leaked the pk_: %q", s.String())
	}
}
