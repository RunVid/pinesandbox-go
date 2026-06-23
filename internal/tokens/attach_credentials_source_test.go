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

func TestCredentials_GenericByStatus(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		var e *AttachCredentialsError
		if _, err := s.Credentials(context.Background(), CredentialsRequest{ComputerID: "c", PodUID: "p", CoordBootID: "b", SandboxID: "s"}); !errors.As(err, &e) {
			t.Errorf("status %d → %T, want *AttachCredentialsError", status, err)
		} else if e.Status != status {
			t.Errorf("status = %d, want %d", e.Status, status)
		}
	}
}

func TestGrantRefresh(t *testing.T) {
	var gotPath string
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"broker_grant":"bg_2","refresh_token":"rt_2"}`)
	})
	gr, err := s.GrantRefresh(context.Background(), GrantRefreshRequest{ComputerID: "c1", PodUID: "p", CoordBootID: "b"})
	if err != nil {
		t.Fatalf("GrantRefresh: %v", err)
	}
	if gr.BrokerGrant != "bg_2" || gr.RefreshToken != "rt_2" {
		t.Errorf("gr = %+v", gr)
	}
	if gotPath != "/v1/computers/c1/grant-refresh" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestGrantRefresh_404Unknown(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	var e *UnknownComputerError
	if _, err := s.GrantRefresh(context.Background(), GrantRefreshRequest{ComputerID: "c", PodUID: "p", CoordBootID: "b"}); !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *UnknownComputerError", err, err)
	}
}

func TestGrantRefresh_MissingFields(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"broker_grant":"bg"}`) // missing refresh_token
	})
	var e *AttachCredentialsError
	if _, err := s.GrantRefresh(context.Background(), GrantRefreshRequest{ComputerID: "c", PodUID: "p", CoordBootID: "b"}); !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *AttachCredentialsError", err, err)
	}
}

func TestGrantRefresh_GenericByStatus(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		var e *AttachCredentialsError
		if _, err := s.GrantRefresh(context.Background(), GrantRefreshRequest{ComputerID: "c", PodUID: "p", CoordBootID: "b"}); !errors.As(err, &e) {
			t.Errorf("status %d → %T, want *AttachCredentialsError", status, err)
		}
	}
}

func TestGrantRefresh_MissingArgs(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) { t.Error("server should not be hit") })
	if _, err := s.GrantRefresh(context.Background(), GrantRefreshRequest{ComputerID: "c"}); err == nil {
		t.Error("expected an error for missing pod_uid/coord_boot_id")
	}
}

func TestAttachSource_StringRedacts(t *testing.T) {
	s := newAttachSource(t, func(w http.ResponseWriter, r *http.Request) {})
	if strings.Contains(s.String(), "pk_test") {
		t.Errorf("String leaked the pk_: %q", s.String())
	}
}
