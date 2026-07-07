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
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{"bind_token":"bt_1","broker_grant":"bg_1"}`)
	})
	ttl := 600
	cr, err := s.Credentials(context.Background(), CredentialsRequest{
		ComputerID: "c1", PodUID: "pod-1", CoordBootID: "boot-1", SandboxID: "sb-1", Profile: "pine_cua_v2", TTLSeconds: &ttl,
	})
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if cr.BindToken != "bt_1" || cr.BrokerGrant != "bg_1" {
		t.Errorf("creds = %+v", cr)
	}
	if gotPath != "/v1/computers/c1/attach-credentials" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["pod_uid"] != "pod-1" || gotBody["sandbox_id"] != "sb-1" || gotBody["profile"] != "pine_cua_v2" || gotBody["ttl_seconds"].(float64) != 600 {
		t.Errorf("body = %v", gotBody)
	}
}

func TestCredentials_OmitsOptional(t *testing.T) {
	var gotBody map[string]any
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{"bind_token":"b","broker_grant":"g"}`)
	})
	if _, err := s.Credentials(context.Background(), CredentialsRequest{ComputerID: "c", PodUID: "p", CoordBootID: "b", SandboxID: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["profile"]; ok {
		t.Errorf("profile should be omitted: %v", gotBody)
	}
	if _, ok := gotBody["ttl_seconds"]; ok {
		t.Errorf("ttl_seconds should be omitted: %v", gotBody)
	}
}

func TestCredentials_404Unknown(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	var e *UnknownComputerError
	if _, err := s.Credentials(context.Background(), CredentialsRequest{ComputerID: "c", PodUID: "p", CoordBootID: "b", SandboxID: "s"}); !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *UnknownComputerError", err, err)
	}
}

func TestCredentials_MissingFields(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"bind_token":"b"}`) // missing broker_grant
	})
	var e *AttachCredentialsError
	if _, err := s.Credentials(context.Background(), CredentialsRequest{ComputerID: "c", PodUID: "p", CoordBootID: "b", SandboxID: "s"}); !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *AttachCredentialsError", err, err)
	}
}

// A bad key (401) stays in the AttachCredentialsError family so a caller's
// errors.As(*AttachCredentialsError) on the attach call keeps catching it; only the
// project/computer-status 403 is broken out as a distinct *ProjectAccessDenied.
func TestCredentials_403ProjectAccessDenied(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	var e *ProjectAccessDenied
	if _, err := s.Credentials(context.Background(), CredentialsRequest{ComputerID: "c", PodUID: "p", CoordBootID: "b", SandboxID: "s"}); !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *ProjectAccessDenied", err, err)
	} else if e.Status != 403 {
		t.Errorf("status = %d, want 403", e.Status)
	}
}

// 403 is NOT swallowed by the generic AttachCredentialsError type — it's the more
// specific ProjectAccessDenied (Go has no inheritance, so the two are distinct).
func TestCredentials_403IsNotGenericAttachError(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	_, err := s.Credentials(context.Background(), CredentialsRequest{ComputerID: "c", PodUID: "p", CoordBootID: "b", SandboxID: "s"})
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
		if _, err := s.Credentials(context.Background(), CredentialsRequest{ComputerID: "c", PodUID: "p", CoordBootID: "b", SandboxID: "s"}); !errors.As(err, &e) {
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
