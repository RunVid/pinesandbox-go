package pinesandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiagnosticsUseProjectKeyWithoutStartingComputer(t *testing.T) {
	const id = "diag_0123456789abcdef"
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer pk_private" || r.Header.Get("X-Pine-Auth") != "" {
			t.Error("incorrect project authentication")
		}
		if r.Method == "POST" {
			if r.Header.Get("Idempotency-Key") != "retry-key" {
				t.Error("lost retry key")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 4 || body["session"] != "main" || body["request_id"] != "req_original" || body["description"] != "stop failed" {
				t.Errorf("body=%v", body)
			}
			w.WriteHeader(202)
		}
		fmt.Fprintf(w, `{"id":%q,"computer_id":"0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000","state":"completed","created_at":"2026-10-08T00:00:00Z","expires_at":"2026-11-07T00:00:00Z","assessment":{"code":"future_code","message":"Read saved result","evidence":"partial","actions":[]}}`, id)
	}))
	defer srv.Close()
	c, err := NewClient(ClientOptions{Endpoint: "http://pine.localhost:8080", ControlHost: strings.TrimPrefix(srv.URL, "http://"), APIKey: "pk_private"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.CreateDiagnostic(context.Background(), DiagnosticOptions{ComputerID: "0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000", Session: "main", Description: "stop failed", RequestID: "req_original", IdempotencyKey: "retry-key"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != id || result.Assessment.Code != "future_code" {
		t.Fatalf("result=%+v", result)
	}
	if _, err := c.GetDiagnostic(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1] != "GET /v1/diagnostics/"+id {
		t.Fatalf("unexpected calls: %v", calls)
	}
	if _, err := c.GetDiagnostic(context.Background(), "../computers"); err == nil || len(calls) != 2 {
		t.Fatal("invalid reference sent")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, c), "pk_private") {
			t.Error("client logs credential")
		}
	}
}

func TestDiagnosticErrorsKeepSupportCorrelation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_failed")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"type":"urn:pinesandbox:problem:diagnostic-limit","detail":"Too many diagnostics","status":429}`)
	}))
	defer srv.Close()
	c, err := NewClient(ClientOptions{Endpoint: "http://pine.localhost:8080", ControlHost: strings.TrimPrefix(srv.URL, "http://"), APIKey: "pk_private"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GetDiagnostic(context.Background(), "diag_0123456789abcdef")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RequestID != "req_failed" {
		t.Fatalf("error=%v", err)
	}
}

// A response can be lost after admission. The shared transport must replay the
// same request key; otherwise one call would consume multiple diagnostic slots.
func TestCreateDiagnosticRetainsGeneratedKeyAcrossTransportRetry(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		w.WriteHeader(202)
		fmt.Fprint(w, `{"id":"diag_0123456789abcdef","state":"queued"}`)
	}))
	defer srv.Close()
	c, err := NewClient(ClientOptions{Endpoint: "http://pine.localhost:8080", ControlHost: strings.TrimPrefix(srv.URL, "http://"), APIKey: "pk_private"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateDiagnostic(context.Background(), DiagnosticOptions{ComputerID: "0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000"}); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("retry keys=%v", keys)
	}
}
