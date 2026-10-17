package pinesandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.pinesandbox.io/computer/internal/controlplane"
)

func TestStopUsesServerOwnershipFromAnySavedHandle(t *testing.T) {
	for _, initial := range []string{"Running", "Paused", "Unavailable", "Saving", "Stopping"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failed=%v", initial, fail), func(t *testing.T) {
				var reads, stops atomic.Int32
				control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == "GET" && r.URL.Path == "/sandboxes/sb-1":
						if reads.Add(1) == 1 {
							fmt.Fprintf(w, `{"id":"sb-1","status":{"state":%q}}`, initial)
						} else if fail {
							fmt.Fprint(w, `{"id":"sb-1","status":{"state":"SaveFailed","message":"restore unavailable","retryable":true}}`)
						} else {
							// The resource remains for storage cleanup; compute has stopped.
							fmt.Fprint(w, `{"id":"sb-1","status":{"state":"Terminated","reason":"COMPUTE_RELEASED"}}`)
						}
					case r.Method == "POST" && r.URL.Path == "/sandboxes/sb-1/stop":
						stops.Add(1)
						var body struct {
							ID string `json:"id"`
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !uuidV7RE.MatchString(body.ID) {
							t.Error("Stop omitted request identity")
						}
						w.WriteHeader(202)
					default:
						t.Errorf("SDK must not resume, save, or delete itself: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(500)
					}
				}))
				defer control.Close()
				coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Error("SDK contacted coord during Stop")
					w.WriteHeader(500)
				}))
				defer coord.Close()
				client := &Client{conn: buildTestConnection(t, control.URL, coord.URL)}
				creds, err := GenerateCredentials()
				if err != nil {
					t.Fatal(err)
				}
				comp, err := client.ReconnectComputer(context.Background(), creds, Binding{creds.ID, "sb-1", "ct_saved", 7})
				if err != nil {
					t.Fatal(err)
				}
				gone, err := comp.Stop(context.Background())
				if fail {
					var saveErr *StopSaveError
					if gone || !errors.As(err, &saveErr) || !IsRetryable(err) || comp.SandboxID() != "sb-1" {
						t.Fatalf("failed save lost source: gone=%v err=%v", gone, err)
					}
				} else if !gone || err != nil || comp.SandboxID() != "" {
					t.Fatalf("release=%v err=%v", gone, err)
				}
				wantStops := int32(1)
				if initial == "Saving" || initial == "Stopping" {
					wantStops = 0
				}
				if stops.Load() != wantStops || comp.BindingRevision() != 7 {
					t.Fatal("Stop lost persisted revision or repeated an active operation")
				}
			})
		}
	}
}

func TestStopRetriesSaveFailureOnlyWithAnExplicitVerdict(t *testing.T) {
	for _, verdict := range []string{"", `,"retryable":null`, `,"retryable":false`, `,"retryable":true`} {
		t.Run(verdict, func(t *testing.T) {
			var stops atomic.Int32
			control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/sandboxes/saved-runtime" && r.Method == "GET" {
					if stops.Load() > 0 {
						fmt.Fprint(w, `{"id":"saved-runtime","status":{"state":"Terminated","reason":"COMPUTE_RELEASED"}}`)
					} else {
						fmt.Fprintf(w, `{"id":"saved-runtime","status":{"state":"SaveFailed"%s}}`, verdict)
					}
				} else if r.URL.Path == "/sandboxes/saved-runtime/stop" && r.Method == "POST" {
					stops.Add(1)
					w.WriteHeader(202)
				} else {
					t.Errorf("unexpected lifecycle request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer control.Close()
			client := &Client{conn: buildTestConnection(t, control.URL, "http://unused")}
			creds, err := GenerateCredentials()
			if err != nil {
				t.Fatal(err)
			}
			comp, err := client.ReconnectComputer(context.Background(), creds, Binding{creds.ID, "saved-runtime", "ct_saved", 7})
			if err != nil {
				t.Fatal(err)
			}
			released, err := comp.Stop(context.Background())
			if verdict == `,"retryable":true` {
				if !released || err != nil || stops.Load() != 1 {
					t.Fatalf("authorized retry failed: %v %v", released, err)
				}
			} else {
				var failed *StopSaveError
				if released || !errors.As(err, &failed) || IsRetryable(err) || stops.Load() != 0 || comp.SandboxID() != "saved-runtime" {
					t.Fatalf("missing or negative retry verdict changed Stop behavior: %v %v", released, err)
				}
			}
		})
	}
}

func TestStopContinuesAfterCallerCancellationOnAnotherBackend(t *testing.T) {
	firstCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stops atomic.Int32
	var released atomic.Bool
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			stops.Add(1)
			cancel() // The server accepted intent before this backend disappeared.
			w.WriteHeader(202)
		case "GET":
			if released.Load() {
				w.WriteHeader(404)
				return
			}
			state := "Running"
			if stops.Load() > 0 {
				state = "Saving"
			}
			fmt.Fprintf(w, `{"id":"sb-1","status":{"state":%q}}`, state)
		default:
			t.Error("unexpected destructive request")
			w.WriteHeader(500)
		}
	}))
	defer control.Close()
	conn := buildTestConnection(t, control.URL, "http://unused")
	first := newComputer("c1", make([]byte, 32))
	if err := first.adopt(conn, "sb-1", "ct_saved", "running"); err != nil {
		t.Fatal(err)
	}
	if gone, err := first.Stop(firstCtx); gone || err == nil || first.SandboxID() != "sb-1" {
		t.Fatalf("lost caller: %v %v", gone, err)
	}
	second := newComputer("c1", make([]byte, 32))
	if err := second.adopt(conn, "sb-1", "ct_saved", "running"); err != nil {
		t.Fatal(err)
	}
	second.sandbox.sleeper = func(context.Context, time.Duration) error { released.Store(true); return nil }
	if gone, err := second.Stop(context.Background()); !gone || err != nil {
		t.Fatalf("reconstructed handle: %v %v", gone, err)
	}
	if stops.Load() != 1 {
		t.Fatal("another backend started another save")
	}
}

func TestRecoveryUsesObservedVersionAndWaitsForCompute(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%v", changed), func(t *testing.T) {
			var reads, deletes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case "GET":
					switch reads.Add(1) {
					case 1:
						w.Header().Set("ETag", `"uid/7"`)
						fmt.Fprint(w, `{"id":"sb-1","status":{"state":"Unavailable","reason":"RestoreFailed"}}`)
					case 2:
						fmt.Fprint(w, `{"id":"sb-1","status":{"state":"Stopping"}}`)
					default:
						fmt.Fprint(w, `{"id":"sb-1","status":{"state":"Terminated","reason":"COMPUTE_RELEASED"}}`)
					}
				case "DELETE":
					deletes.Add(1)
					if r.Header.Get("If-Match") != `"uid/7"` {
						t.Error("conditional release lost its observation fence")
					}
					if changed {
						w.WriteHeader(412)
					} else {
						w.WriteHeader(204)
					}
				default:
					t.Errorf("unexpected replacement request %s", r.Method)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			conn := buildTestConnection(t, server.URL, "http://unused")
			info, err := conn.controlPlane.Get(context.Background(), "sb-1")
			if err != nil {
				t.Fatal(err)
			}
			handle := newRuntimeHandle(conn.controlPlane, "sb-1", info.Status)
			handle.sleeper = func(context.Context, time.Duration) error { return nil }
			released, err := handle.releaseFailedRuntime(context.Background(), info)
			if changed {
				var conflict *ConflictError
				if released || !errors.As(err, &conflict) || reads.Load() != 1 {
					t.Fatalf("stale release: %v %v", released, err)
				}
			} else if !released || err != nil || reads.Load() != 3 {
				t.Fatalf("release completed before compute stopped: %v %v reads=%d", released, err, reads.Load())
			}
			if deletes.Load() != 1 {
				t.Fatal("conditional DELETE was retried")
			}
		})
	}
}

func TestRecoveryDoesNotDiscardAnUnsettledCapture(t *testing.T) {
	conn := buildTestConnection(t, "http://unused", "http://unused")
	handle := newRuntimeHandle(conn.controlPlane, "sb-1", "unavailable")
	info := &controlplane.SandboxInfo{Status: "unavailable", Reason: "CaptureTimeout", ETag: `"uid/7"`}
	if released, err := handle.releaseFailedRuntime(context.Background(), info); released || err == nil {
		t.Fatal("capture timeout is not a settled failure")
	}
}

func TestRecoveryWaitRespectsCallerDeadline(t *testing.T) {
	get, _ := statusSequence("stopping")
	handle := newRuntimeHandle(&fakeCP{get: get}, "sb-1", "stopping")
	ctx, cancel := context.WithCancel(context.Background())
	handle.sleeper = func(context.Context, time.Duration) error { cancel(); return nil }
	released, err := handle.releaseFailedRuntime(ctx, &controlplane.SandboxInfo{Status: "stopping"})
	if released || !errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup wait escaped deadline: %v %v", released, err)
	}
}

func TestStartAndRecoveryDoNotReplaceHealthyMemory(t *testing.T) {
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	var references atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/v1/computers/"+creds.ID {
			references.Add(1)
			fmt.Fprintf(w, `{"computer_id":%q,"binding_revision":7,"sandbox_id":"old"}`, creds.ID)
		} else if r.Method == "GET" && r.URL.Path == "/sandboxes/old" {
			fmt.Fprint(w, `{"id":"old","status":{"state":"Paused"}}`)
		} else {
			t.Errorf("healthy memory replacement request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	client := &Client{conn: buildTestConnection(t, server.URL, "http://unused")}
	_, startErr := client.StartComputer(context.Background(), creds, StartOptions{})
	var retained *RuntimeRetainedError
	if !errors.As(startErr, &retained) || retained.RuntimeID != "old" || retained.State != "paused" {
		t.Fatalf("Start lost typed conflict: %v", startErr)
	}
	for _, operation := range []func(context.Context, Credentials, StartOptions) (*Computer, error){client.StartComputer, client.RecoverComputer} {
		if computer, err := operation(context.Background(), creds, StartOptions{}); err == nil || computer != nil {
			t.Fatal("healthy paused runtime replaced")
		}
	}
	if references.Load() != 3 {
		t.Fatal("binding revision was reread within an invocation")
	}
}

func TestStopByIDUsesCurrentBindingWithoutRuntimeCredentials(t *testing.T) {
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	var references, stops atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/computers/"+creds.ID:
			references.Add(1)
			fmt.Fprintf(w, `{"computer_id":%q,"binding_revision":7,"sandbox_id":"unrecorded"}`, creds.ID)
		case r.Method == "GET" && r.URL.Path == "/sandboxes/unrecorded":
			if stops.Load() == 0 {
				fmt.Fprint(w, `{"id":"unrecorded","status":{"state":"Running"}}`)
			} else {
				fmt.Fprint(w, `{"id":"unrecorded","status":{"state":"Terminated","reason":"COMPUTE_RELEASED"}}`)
			}
		case r.Method == "POST" && r.URL.Path == "/sandboxes/unrecorded/stop":
			stops.Add(1)
			w.WriteHeader(202)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	client := &Client{conn: buildTestConnection(t, server.URL, "http://unused")}
	if gone, err := client.StopComputer(context.Background(), creds.ID); !gone || err != nil {
		t.Fatalf("Stop by ID: gone=%v err=%v", gone, err)
	}
	if references.Load() != 1 || stops.Load() != 1 {
		t.Fatal("Stop must resolve one binding and admit one operation")
	}
}

func TestStopByIDDistinguishesAbsenceFromLookupFailure(t *testing.T) {
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{http.StatusNotFound, http.StatusForbidden} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/computers/"+creds.ID {
					t.Error("unexpected runtime operation")
				}
				w.WriteHeader(code)
				fmt.Fprint(w, `{"message":"not accessible"}`)
			}))
			defer server.Close()
			client := &Client{conn: buildTestConnection(t, server.URL, "http://unused")}
			gone, err := client.StopComputer(context.Background(), creds.ID)
			if code == http.StatusNotFound {
				if !gone || err != nil {
					t.Fatalf("absence: gone=%v err=%v", gone, err)
				}
			} else if gone || err == nil {
				t.Fatalf("lookup failure: gone=%v err=%v", gone, err)
			}
		})
	}
}
