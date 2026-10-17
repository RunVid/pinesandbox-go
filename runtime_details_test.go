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
	"time"
)

func TestRuntimeDetailsReadsOnlyTheSavedGeneration(t *testing.T) {
	for _, state := range []string{"Running", "Paused", "Resuming", "SaveFailed", "Unavailable", "Stopping"} {
		t.Run(state, func(t *testing.T) {
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/sandboxes/saved-runtime" {
					t.Errorf("unexpected lookup or mutation: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
					return
				}
				reads++
				fmt.Fprintf(w, `{"id":"saved-runtime","status":{"state":%q,"reason":"reason","message":"message","retryable":true,"operation_id":"op"},"createdAt":"2026-10-16T00:00:00Z","expiresAt":"2026-10-16T00:05:00Z"}`, state)
			}))
			defer server.Close()
			creds, _ := GenerateCredentials()
			client := &Client{conn: buildTestConnection(t, server.URL, "http://unused")}
			binding := Binding{creds.ID, "saved-runtime", "ct_saved", 7}
			comp, err := client.ReconnectComputer(context.Background(), creds, binding)
			if err != nil {
				t.Fatal(err)
			}
			details, err := comp.RuntimeDetails(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			legacy, err := comp.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if details.ID != binding.RuntimeID || details.State != legacy || details.Reason != "reason" ||
				details.Message != "message" || details.Retryable == nil || !*details.Retryable || details.OperationID != "op" ||
				details.CreatedAt == nil || details.ExpiresAt == nil || details.ExpiresAt.Sub(*details.CreatedAt) != 5*time.Minute || reads != 2 {
				t.Fatalf("lost lifecycle metadata: %+v (legacy=%s reads=%d)", details, legacy, reads)
			}
		})
	}
}

func TestRuntimeDetailsPreservesOptionalRetryVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, status           string
		wantPresent, wantRetry bool
	}{
		{"running", `{"state":"Running"}`, false, false},
		{"paused", `{"state":"Paused"}`, false, false},
		{"legacy_failure", `{"state":"SaveFailed"}`, false, false},
		{"null", `{"state":"SaveFailed","retryable":null}`, false, false},
		{"permanent_failure", `{"state":"SaveFailed","retryable":false}`, true, false},
		{"transient_failure", `{"state":"SaveFailed","retryable":true}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/sandboxes/saved-runtime" {
					t.Errorf("unexpected lookup or mutation: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
					return
				}
				fmt.Fprintf(w, `{"id":"saved-runtime","status":%s}`, tc.status)
			}))
			defer server.Close()
			creds, err := GenerateCredentials()
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{conn: buildTestConnection(t, server.URL, "http://unused")}
			comp, err := client.ReconnectComputer(context.Background(), creds, Binding{creds.ID, "saved-runtime", "ct_saved", 7})
			if err != nil {
				t.Fatal(err)
			}
			details, err := comp.RuntimeDetails(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if (details.Retryable != nil) != tc.wantPresent || (details.Retryable != nil && *details.Retryable != tc.wantRetry) {
				t.Fatalf("retry verdict = %v; want present=%v value=%v", details.Retryable, tc.wantPresent, tc.wantRetry)
			}
		})
	}
}

func TestRuntimeDetailsOldServerAndErrorsDoNotReplaceBinding(t *testing.T) {
	for _, code := range []int{200, 401, 403, 404, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/sandboxes/old" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(code)
				if code == 200 {
					fmt.Fprint(w, `{"id":"old","status":{"state":"Running"}}`)
				}
			}))
			defer server.Close()
			creds, _ := GenerateCredentials()
			client := &Client{conn: buildTestConnection(t, server.URL, "http://unused")}
			binding := Binding{creds.ID, "old", "ct_old", 7}
			comp, err := client.ReconnectComputer(context.Background(), creds, binding)
			if err != nil {
				t.Fatal(err)
			}
			details, err := comp.RuntimeDetails(context.Background())
			if code == 200 {
				if err != nil || details.CreatedAt != nil || details.ExpiresAt != nil || details.OperationID != "" || details.Retryable != nil {
					t.Fatalf("old server: %+v %v", details, err)
				}
			} else if err == nil || details != nil {
				t.Fatal("lookup failure was hidden")
			}
			if code == 404 {
				var missing *NotFoundError
				if !errors.As(err, &missing) {
					t.Fatalf("lost absence error: %v", err)
				}
				// A stale worker can stop only its old runtime, even after another
				// backend has Started a successor. No current-binding lookup occurs.
				if stopped, err := comp.Stop(context.Background()); err != nil || !stopped {
					t.Fatalf("stale Stop: %v %v", stopped, err)
				}
				if _, err := comp.Binding(); err == nil {
					t.Fatal("Stop retained binding")
				}
			} else if got, err := comp.Binding(); err != nil || got != binding {
				t.Fatal("observation changed binding")
			}
		})
	}
}

func TestIdleTimeoutEditUsesSavedRuntimeAndDoesNotReplayConflicts(t *testing.T) {
	for _, code := range []int{200, 409, 412, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			writes := 0
			const payload = `{"id":"saved-runtime","status":{"state":"Running"},"runtime_revision":"actual-revision","lifecycle":{"idle_timeout_seconds":60,"min_idle_timeout_seconds":60,"max_idle_timeout_seconds":86400,"max_runtime_age_seconds":604800,"stop_at":"2026-10-23T00:00:00Z","deadline":"2026-10-23T00:12:00Z"}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /sandboxes/saved-runtime":
					w.Header().Set("ETag", `"resource/7"`)
					fmt.Fprint(w, payload)
				case "PUT /sandboxes/saved-runtime/idle-timeout":
					writes++
					if r.Header.Get("If-Match") != `"resource/7"` {
						t.Error("missing observed version")
					}
					var body struct {
						Seconds int `json:"seconds"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Seconds != 60 {
						t.Error("incorrect idle timeout")
					}
					w.WriteHeader(code)
					if code == 200 {
						fmt.Fprint(w, payload)
					}
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			creds, _ := GenerateCredentials()
			client := &Client{conn: buildTestConnection(t, server.URL, "http://unused")}
			binding := Binding{creds.ID, "saved-runtime", "ct_saved", 7}
			comp, err := client.ReconnectComputer(context.Background(), creds, binding)
			if err != nil {
				t.Fatal(err)
			}
			details, err := comp.SetIdleTimeout(context.Background(), time.Minute)
			if code == 200 {
				if err != nil || details.Lifecycle == nil || details.Lifecycle.IdleTimeoutSeconds != 60 || details.RuntimeRevision != "actual-revision" {
					t.Fatalf("%+v %v", details, err)
				}
				if details.Lifecycle.Deadline.Sub(details.Lifecycle.StopAt) != 12*time.Minute {
					t.Fatal("deadline lost")
				}
			} else if err == nil {
				t.Fatal("mutation error hidden")
			}
			if writes != 1 {
				t.Fatalf("mutation replayed %d times", writes)
			}
			if got, err := comp.Binding(); err != nil || got != binding {
				t.Fatal("edit changed binding")
			}
		})
	}
}

func TestIdleTimeoutEditRejectsLegacyRuntimeWithoutWriting(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/sandboxes/saved-runtime" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		reads++
		w.Header().Set("ETag", `"resource/7"`)
		fmt.Fprint(w, `{"id":"saved-runtime","status":{"state":"Running"}}`)
	}))
	defer server.Close()
	creds, _ := GenerateCredentials()
	client := &Client{conn: buildTestConnection(t, server.URL, "http://unused")}
	binding := Binding{creds.ID, "saved-runtime", "ct_saved", 7}
	comp, err := client.ReconnectComputer(context.Background(), creds, binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, timeout := range []time.Duration{0, time.Second, time.Minute + time.Nanosecond} {
		if _, err := comp.SetIdleTimeout(context.Background(), timeout); err == nil {
			t.Fatalf("accepted invalid timeout %v", timeout)
		}
	}
	if reads != 0 {
		t.Fatal("invalid input sent a request")
	}
	if _, err := comp.SetIdleTimeout(context.Background(), time.Minute); err == nil || !strings.Contains(err.Error(), "no admitted lifetime policy") {
		t.Fatalf("unexpected legacy result: %v", err)
	}
	if reads != 1 {
		t.Fatalf("expected one observation, got %d", reads)
	}
	if got, err := comp.Binding(); err != nil || got != binding {
		t.Fatal("edit changed binding")
	}
}
