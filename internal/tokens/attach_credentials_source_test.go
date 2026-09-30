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
	var gotBody map[string]any
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(201)
		fmt.Fprint(w, `{"computer_id":"c1"}`)
	})
	if err := s.RegisterComputer(context.Background(), "c1", &ComputerLocation{Country: "US"}); err != nil {
		t.Fatalf("RegisterComputer: %v", err)
	}
	if gotAuth != "Bearer pk_test" || gotPath != "/v1/computers" {
		t.Errorf("auth=%q path=%q", gotAuth, gotPath)
	}
	if gotBody["computer_id"] != "c1" {
		t.Errorf("body = %v", gotBody)
	}
	if gotBody["location"].(map[string]any)["country"] != "US" {
		t.Errorf("location = %v", gotBody["location"])
	}
}

func TestDirectLocationWire(t *testing.T) {
	calls := 0
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if got := string(body["location"]); got != `{"mode":"direct"}` {
			t.Errorf("location = %s", got)
		}
		fmt.Fprint(w, `{"bind_token":"b","broker_grant":"g","key_assertion":"k","binding_revision":1,"location":{"mode":"direct"}}`)
	})
	if err := s.RegisterComputer(context.Background(), "c1", &ComputerLocation{Mode: "direct"}); err != nil {
		t.Fatal(err)
	}
	req := validCredentialsRequest()
	req.Location = &ComputerLocation{Mode: "direct"}
	creds, err := s.Credentials(context.Background(), req)
	if err != nil || creds.Location == nil || *creds.Location != *req.Location {
		t.Fatalf("credentials = %+v, error = %v", creds, err)
	}
	for _, location := range []ComputerLocation{{Mode: "direct", Country: "US"}, {Mode: "automatic"}, {}} {
		req.Location = &location
		if _, err := s.Credentials(context.Background(), req); err == nil {
			t.Errorf("accepted invalid location %+v", location)
		}
	}
	if calls != 2 {
		t.Errorf("network calls = %d, want 2", calls)
	}
}

func TestRegisterComputer_Conflict(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		fmt.Fprint(w, `{"message":"owned by another project"}`)
	})
	var e *ComputerRegistrationError
	if err := s.RegisterComputer(context.Background(), "c1", nil); !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *ComputerRegistrationError", err, err)
	} else if e.Status != 409 {
		t.Errorf("status = %d", e.Status)
	}
}

func TestAvailableLocations(t *testing.T) {
	var gotAuth string
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/locations" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"locations":[{"country":"SG"},{"country":"US"}],"default_location":{"country":"US"}}`)
	})

	result, err := s.AvailableLocations(context.Background())
	if err != nil {
		t.Fatalf("AvailableLocations: %v", err)
	}
	if gotAuth != "Bearer pk_test" || len(result.Locations) != 2 ||
		result.Locations[0].Country != "SG" || result.DefaultLocation.Country != "US" {
		t.Fatalf("auth=%q result=%+v", gotAuth, result)
	}
}

func TestAvailableLocationsRejectsMalformedResponse(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"locations":[{"country":"sg"}],"default_location":{"country":"US"}}`)
	})
	var discovery *LocationDiscoveryError
	if _, err := s.AvailableLocations(context.Background()); !errors.As(err, &discovery) {
		t.Fatalf("err = %T (%v), want *LocationDiscoveryError", err, err)
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
		fmt.Fprint(w, `{"bind_token":"bt_1","broker_grant":"bg_1","key_assertion":"ka_1","binding_revision":1,"usage_reporter_grant":"urg_1","usage_reporter_grant_expires_at":"2026-08-05T12:00:00Z","usage_reporter_id":"ure_0123456789abcdef","location":{"country":"US"}}`)
	})
	req := validCredentialsRequest()
	req.ComputerID, req.PodUID, req.CoordBootID, req.SandboxID = "c1", "pod-1", "boot-1", "sb-1"
	req.Location = &ComputerLocation{Country: "US"}
	cr, err := s.Credentials(context.Background(), req)
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if cr.BindToken != "bt_1" || cr.BrokerGrant != "bg_1" {
		t.Errorf("creds = %+v", cr)
	}
	if cr.Location == nil || cr.Location.Country != "US" {
		t.Errorf("creds location = %+v", cr.Location)
	}
	if gotPath != "/v1/computers/c1/attach-credentials" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["pod_uid"] != "pod-1" || gotBody["sandbox_id"] != "sb-1" || gotBody["expected_binding_revision"].(float64) != 0 {
		t.Errorf("body = %v", gotBody)
	}
	if gotBody["location"].(map[string]any)["country"] != "US" {
		t.Errorf("location = %v", gotBody["location"])
	}
	if _, ok := gotBody["profile"]; ok {
		t.Errorf("profile leaked into attach body: %v", gotBody)
	}
}

func TestCredentialsRejectsNonCanonicalCountryBeforeNetwork(t *testing.T) {
	called := false
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	})
	req := validCredentialsRequest()
	req.Location = &ComputerLocation{Country: "us"}
	if _, err := s.Credentials(context.Background(), req); err == nil {
		t.Fatal("Credentials accepted a non-canonical lowercase country")
	}
	if called {
		t.Fatal("Credentials contacted Portal before validating location")
	}
}

func TestCredentialsPreservesNonCanonicalCountryForCommittedReceiptValidation(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"bind_token":"b","broker_grant":"g","key_assertion":"k","binding_revision":1,"location":{"country":"us"}}`)
	})
	creds, err := s.Credentials(context.Background(), validCredentialsRequest())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if creds.BindingRevision != 1 || creds.Location == nil || creds.Location.Country != "us" {
		t.Fatalf("committed receipt = %+v", creds)
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

func TestDeleteComputer_204IsIdempotentSuccess(t *testing.T) {
	var calls int
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/computers/c1" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer pk_test" {
			t.Errorf("auth = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	for i := 0; i < 2; i++ {
		if err := s.DeleteComputer(context.Background(), "c1"); err != nil {
			t.Fatalf("DeleteComputer #%d: %v", i+1, err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestDeleteComputer_RequiresID(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	})
	if err := s.DeleteComputer(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty computer_id")
	}
}

func TestDeleteComputer_ErrorsByStatus(t *testing.T) {
	problemBody := func(status int, typ string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"type":%q,"title":"t","status":%d}`, typ, status)
		}
	}
	t.Run("422 malformed id", func(t *testing.T) {
		s := newAttachSource(t, problemBody(422, "INVALID_COMPUTER_ID"))
		err := s.DeleteComputer(context.Background(), "not-a-uuid")
		var e *ComputerDeletionError
		if !errors.As(err, &e) {
			t.Fatalf("err = %T (%v), want *ComputerDeletionError", err, err)
		}
		if e.Status != 422 || e.Code != "INVALID_COMPUTER_ID" || e.Op != "DELETE /v1/computers/not-a-uuid" {
			t.Errorf("err = %+v", e.tokenBase)
		}
	})
	t.Run("403 insufficient scope", func(t *testing.T) {
		s := newAttachSource(t, problemBody(403, "INSUFFICIENT_SCOPE"))
		err := s.DeleteComputer(context.Background(), "c1")
		var e *ProjectAccessDenied
		if !errors.As(err, &e) || e.Status != 403 || !strings.Contains(err.Error(), "may not delete computers") {
			t.Fatalf("err = %T (%v), want *ProjectAccessDenied", err, err)
		}
	})
	for _, tc := range []struct {
		status int
		typ    string
	}{{401, "INVALID_API_KEY"}, {429, "RATE_LIMITED"}, {500, "INTERNAL"}, {503, "UNAVAILABLE"}} {
		t.Run(fmt.Sprintf("%d generic", tc.status), func(t *testing.T) {
			s := newAttachSource(t, problemBody(tc.status, tc.typ))
			err := s.DeleteComputer(context.Background(), "c1")
			var e *AttachCredentialsError
			if !errors.As(err, &e) || e.Status != tc.status {
				t.Fatalf("err = %T (%v), want *AttachCredentialsError status %d", err, err, tc.status)
			}
			if strings.Contains(err.Error(), "pk_test") || strings.Contains(fmt.Sprintf("%+v", err), "pk_test") {
				t.Errorf("error leaked the pk_: %v", err)
			}
		})
	}
}
