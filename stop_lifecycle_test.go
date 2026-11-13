package pinesandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
			handle := newSandboxHandle(conn.controlPlane, "sb-1", info.Status)
			handle.sleeper = func(context.Context, time.Duration) error { return nil }
			err = recoverRelease(context.Background(), handle, info)
			if changed {
				var conflict *ConflictError
				if !errors.As(err, &conflict) || reads.Load() != 1 {
					t.Fatalf("stale release: %v", err)
				}
			} else if err != nil || reads.Load() != 3 {
				t.Fatalf("release completed before compute stopped: %v reads=%d", err, reads.Load())
			}
			if deletes.Load() != 1 {
				t.Fatal("conditional DELETE was retried")
			}
		})
	}
}

// recoverRelease is Recover's gate on info, within a one-minute budget.
func recoverRelease(ctx context.Context, handle *SandboxHandle, info *controlplane.SandboxInfo) error {
	release := readyWait{target: targetReleased, started: handle.clock()}
	return handle.admit(ctx, admissionRecover, info, release, release.started.Add(time.Minute))
}

func TestRecoveryDoesNotDiscardAnUnsettledCapture(t *testing.T) {
	for _, reason := range []string{"CaptureTimeout", "CAPTURE_TIMEOUT"} {
		conn := buildTestConnection(t, "http://unused", "http://unused")
		handle := newSandboxHandle(conn.controlPlane, "sb-1", "unavailable")
		info := &controlplane.SandboxInfo{Status: "unavailable", Reason: reason, ETag: `"uid/7"`}
		var retained *SandboxRetainedError
		err := recoverRelease(context.Background(), handle, info)
		if !errors.As(err, &retained) || retained.Reason != reason {
			t.Fatalf("capture timeout is not a settled failure: %v", err)
		}
		want := "pinesandbox: Recover refused: the capture of Computer runtime sb-1 hasn't resolved (state unavailable, reason " + reason + "); nothing was released or started"
		if err.Error() != want {
			t.Fatalf("message = %q, want %q", err, want)
		}
	}
}

func TestRecoveryWaitRespectsCallerDeadline(t *testing.T) {
	get, _ := statusSequence("stopping")
	handle := newSandboxHandle(&fakeCP{get: get}, "sb-1", "stopping")
	ctx, cancel := context.WithCancel(context.Background())
	handle.sleeper = func(context.Context, time.Duration) error { cancel(); return nil }
	err := recoverRelease(ctx, handle, &controlplane.SandboxInfo{Status: "stopping"})
	var rt *ReadyTimeoutError
	if !errors.Is(err, context.Canceled) || errors.As(err, &rt) {
		t.Fatalf("cleanup wait escaped deadline: %v", err)
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
	var retained *SandboxRetainedError
	if !errors.As(startErr, &retained) || retained.SandboxID != "old" || retained.State != "paused" {
		t.Fatalf("Start lost typed conflict: %v", startErr)
	}
	for _, operation := range []func(context.Context, Credentials, StartOptions) (*Computer, error){client.StartComputer, client.RecoverComputer} {
		if computer, err := operation(context.Background(), creds, StartOptions{}); err == nil || computer != nil {
			t.Fatal("healthy paused Computer sandbox replaced")
		}
	}
	if references.Load() != 3 {
		t.Fatal("binding revision was reread within an invocation")
	}
}

func TestStopByIDUsesCurrentBindingWithoutSandboxCredentials(t *testing.T) {
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
					t.Error("unexpected lifecycle operation")
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

func TestStartWaitRaisesAReadErrorThatIsNotTransient(t *testing.T) {
	forbidden := &ForbiddenError{}
	handle := newSandboxHandle(&fakeCP{wait: func(context.Context, string, *controlplane.SandboxInfo) (*controlplane.SandboxInfo, error) {
		return nil, forbidden
	}}, "sb-1", "")
	release := readyWait{target: targetReleased, started: handle.clock()}
	saving := &controlplane.SandboxInfo{Status: "saving", Reason: "STOP_ACCEPTED", ETag: `"o1"`}
	if err := handle.admit(context.Background(), admissionStart, saving, release, release.started.Add(time.Minute)); !errors.Is(err, forbidden) {
		t.Fatalf("err = %T %v, want the 403", err, err)
	}
}

func TestRecoveryRefusesAReleaseWithoutTheServersVersion(t *testing.T) {
	handle := newSandboxHandle(&fakeCP{}, "sb-1", "")
	err := recoverRelease(context.Background(), handle, &controlplane.SandboxInfo{Status: "failed", Reason: "PAUSE_FAILED"})
	if want := "pinesandbox: Recover refused: the server sent no version for Computer runtime sb-1; nothing was released or started"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// startServer answers a Start's requests by method and path; the binding names rt-old.
func startServer(t *testing.T, creds Credentials, clock *virtualClock, answer func(r *http.Request) (int, http.Header, string)) *Client {
	t.Helper()
	hooks := clientHooks{httpClient: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		status, header, body := 0, http.Header{"Content-Type": {"application/json"}}, ""
		switch {
		case r.URL.Path == "/v1/computers/"+creds.ID:
			status, body = 200, fmt.Sprintf(`{"computer_id":%q,"binding_revision":3,"sandbox_id":"rt-old"}`, creds.ID)
		case r.URL.Path == "/v1/control-token":
			status, body = 200, `{"token":"eyJhbGciOiJFZERTQSJ9.eyJleHAiOjQxMDI0NDQ4MDB9.c2ln","expires_in":900}`
		default:
			var extra http.Header
			status, extra, body = answer(r)
			for k, v := range extra {
				header[k] = v
			}
		}
		if status == 0 { // no response: the request ended with its context
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	if clock != nil {
		hooks.clock = clock
	}
	client, err := newClient(ClientOptions{Endpoint: "https://start.pinesandbox.io", APIKey: "pk_test"}, hooks)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestStartChecksItsOptionsBeforeTheGate(t *testing.T) {
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	var lifecycle atomic.Int32
	client := startServer(t, creds, nil, func(r *http.Request) (int, http.Header, string) {
		lifecycle.Add(1)
		return 500, nil, `{}`
	})
	for _, opts := range []StartOptions{{Location: &ComputerLocation{Country: "usa"}}, {ReadyTimeout: -time.Second}} {
		if _, err := client.RecoverComputer(context.Background(), creds, opts); err == nil {
			t.Errorf("Recover accepted %+v", opts)
		}
	}
	if _, err := client.AttachComputer(context.Background(), creds.ID, creds.Key, AttachOptions{PodEnv: map[string]string{"A": "b"}}); err == nil {
		t.Error("AttachComputer accepted PodEnv")
	}
	if lifecycle.Load() != 0 {
		t.Fatalf("%d lifecycle requests before the options were checked", lifecycle.Load())
	}
}

func TestStartSharesOneDefaultBudgetAcrossReleaseCreateAndReady(t *testing.T) {
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	clock := &virtualClock{now: time.UnixMilli(1_760_000_000_000)}
	begin := clock.Now()
	var createdAfter time.Duration
	etag := func(v string) http.Header { return http.Header{"Etag": {v}} }
	// The old runtime is released 200 s in; the new one never runs.
	client := startServer(t, creds, clock, func(r *http.Request) (int, http.Header, string) {
		polled := r.Header.Get("If-None-Match") != ""
		switch {
		case r.Method == "GET" && r.URL.Path == "/sandboxes/rt-old" && !polled:
			return 200, etag(`"o1"`), `{"id":"rt-old","status":{"state":"Saving","reason":"STOP_ACCEPTED"}}`
		case r.Method == "GET" && r.URL.Path == "/sandboxes/rt-old" && clock.Now().Sub(begin) < 200*time.Second:
			return 304, etag(`"o1"`), ""
		case r.Method == "GET" && r.URL.Path == "/sandboxes/rt-old":
			return 404, nil, `{"type":"urn:pinesandbox:problem:not-found","status":404}`
		case r.Method == "POST" && r.URL.Path == "/computer-sandboxes":
			createdAfter = clock.Now().Sub(begin)
			return 202, nil, `{"id":"rt-1","status":{"state":"Pending"}}`
		case r.Method == "GET" && r.URL.Path == "/sandboxes/rt-1" && !polled:
			return 200, etag(`"p1"`), `{"id":"rt-1","status":{"state":"Pending"}}`
		case r.Method == "GET" && r.URL.Path == "/sandboxes/rt-1":
			return 304, etag(`"p1"`), ""
		case r.Method == "DELETE" && r.URL.Path == "/sandboxes/rt-1":
			return 204, nil, ""
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		return 500, nil, `{}`
	})
	_, err = client.StartComputer(context.Background(), creds, StartOptions{})
	var timeout *ReadyTimeoutError
	if !errors.As(err, &timeout) || timeout.SandboxID != "rt-1" || timeout.Waited != 300*time.Second {
		t.Fatalf("err = %T %v", err, err)
	}
	if createdAfter != 200*time.Second || clock.Now().Sub(begin) != 300*time.Second {
		t.Fatalf("created after %v, ended after %v; want 200 s and 300 s", createdAfter, clock.Now().Sub(begin))
	}
}

func TestReadyTimeoutKeepsTheCreateResponsesReason(t *testing.T) {
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	// The first ready read never answers, so the budget ends before any observation.
	client := startServer(t, creds, nil, func(r *http.Request) (int, http.Header, string) {
		switch {
		case r.URL.Path == "/sandboxes/rt-old":
			return 404, nil, `{"type":"urn:pinesandbox:problem:not-found","status":404}`
		case r.Method == "POST":
			return 202, nil, `{"id":"rt-1","status":{"state":"Pending","reason":"COMPUTER_CAPACITY_UNAVAILABLE"}}`
		case r.Method == "GET":
			<-r.Context().Done()
			return 504, nil, `{}`
		}
		return 204, nil, ""
	})
	_, err = client.StartComputer(context.Background(), creds, StartOptions{ReadyTimeout: 100 * time.Millisecond})
	var timeout *ReadyTimeoutError
	if !errors.As(err, &timeout) || timeout.LastState != "pending" || timeout.LastReason != "COMPUTER_CAPACITY_UNAVAILABLE" {
		t.Fatalf("err = %T %+v", err, err)
	}
}

func TestStartBudgetEndsInsideTheBindingRead(t *testing.T) {
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	hooks := clientHooks{httpClient: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/computers/"+creds.ID {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}}
	client, err := newClient(ClientOptions{Endpoint: "https://start.pinesandbox.io", APIKey: "pk_test"}, hooks)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.StartComputer(context.Background(), creds, StartOptions{ReadyTimeout: 100 * time.Millisecond})
	var timeout *ReadyTimeoutError
	if !errors.As(err, &timeout) || timeout.SandboxID != "" || timeout.LastState != "" || timeout.Waited < 100*time.Millisecond {
		t.Fatalf("err = %T %+v", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) || IsRetryable(err) {
		t.Fatal("the budget running out must be a deadline and never retryable")
	}
	if got := err.Error(); got != "pinesandbox: The Computer's current runtime wasn't read within 0 s; nothing was released or started" {
		t.Fatalf("message = %q", got)
	}
}

func TestRecoverBudgetEndsInsideTheCurrentRuntimeRead(t *testing.T) {
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	client := startServer(t, creds, nil, func(r *http.Request) (int, http.Header, string) {
		if r.Method == "GET" && r.URL.Path == "/sandboxes/rt-old" {
			<-r.Context().Done()
			return 0, nil, ""
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		return 500, nil, `{}`
	})
	_, err = client.RecoverComputer(context.Background(), creds, StartOptions{ReadyTimeout: 100 * time.Millisecond})
	var timeout *ReadyTimeoutError
	if !errors.As(err, &timeout) || timeout.SandboxID != "rt-old" || timeout.LastState != "" ||
		!strings.Contains(err.Error(), "current runtime wasn't read within") {
		t.Fatalf("err = %T %+v", err, err)
	}
}

// createCutByTheBudget answers a Start whose first create never answers; resend answers the second.
func createCutByTheBudget(t *testing.T, resend func() (int, string)) (error, []string, int32) {
	t.Helper()
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	var createIDs []string
	var deleted atomic.Int32
	client := startServer(t, creds, nil, func(r *http.Request) (int, http.Header, string) {
		switch {
		case r.URL.Path == "/sandboxes/rt-old":
			return 404, nil, `{"type":"urn:pinesandbox:problem:not-found","status":404}`
		case r.Method == "POST" && r.URL.Path == "/computer-sandboxes":
			var body struct {
				CreateID string `json:"create_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if createIDs = append(createIDs, body.CreateID); len(createIDs) == 1 {
				<-r.Context().Done()
				return 0, nil, ""
			}
			status, answer := resend()
			return status, nil, answer
		case r.Method == "DELETE" && r.URL.Path == "/sandboxes/rt-1":
			deleted.Add(1)
			return 204, nil, ""
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		return 500, nil, `{}`
	})
	_, err = client.StartComputer(context.Background(), creds, StartOptions{ReadyTimeout: 100 * time.Millisecond})
	return err, createIDs, deleted.Load()
}

func TestCreateCutByTheBudgetIsSentAgainAndDeleted(t *testing.T) {
	err, createIDs, deleted := createCutByTheBudget(t, func() (int, string) {
		return 202, `{"id":"rt-1","status":{"state":"Pending","reason":"COMPUTER_CAPACITY_UNAVAILABLE"}}`
	})
	var timeout *ReadyTimeoutError
	if !errors.As(err, &timeout) || timeout.SandboxID != "rt-1" || timeout.LastState != "pending" ||
		timeout.LastReason != "COMPUTER_CAPACITY_UNAVAILABLE" {
		t.Fatalf("err = %T %+v", err, err)
	}
	if len(createIDs) != 2 || createIDs[0] == "" || createIDs[0] != createIDs[1] || deleted != 1 {
		t.Fatalf("create ids %v, %d deletes; want the same create_id twice and one delete", createIDs, deleted)
	}
}

func TestCreateNeverSentIsNotSentWhenTheTokenTakesTheBudget(t *testing.T) {
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	var creates, mints atomic.Int32
	answer := func(r *http.Request, body string) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}
	hooks := clientHooks{httpClient: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/computers/" + creds.ID:
			return answer(r, fmt.Sprintf(`{"computer_id":%q,"binding_revision":3,"sandbox_id":null}`, creds.ID))
		case "/v1/control-token":
			if mints.Add(1) == 1 { // the first project token takes the whole budget
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			return answer(r, `{"token":"eyJhbGciOiJFZERTQSJ9.eyJleHAiOjQxMDI0NDQ4MDB9.c2ln","expires_in":900}`)
		case "/computer-sandboxes":
			creates.Add(1)
		}
		return answer(r, `{"id":"rt-1","status":{"state":"Pending"}}`)
	})}}
	client, err := newClient(ClientOptions{Endpoint: "https://start.pinesandbox.io", APIKey: "pk_test"}, hooks)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.StartComputer(context.Background(), creds, StartOptions{ReadyTimeout: 100 * time.Millisecond})
	var timeout *TimeoutError
	if !errors.As(err, &timeout) || creates.Load() != 0 {
		t.Fatalf("err = %T %v, %d creates; want the token's timeout and no create", err, err, creates.Load())
	}
}

func TestCreateCutByTheBudgetWhoseResendFailsStaysATimeout(t *testing.T) {
	err, createIDs, deleted := createCutByTheBudget(t, func() (int, string) {
		return 409, `{"code":"COMPUTER_LIFECYCLE_CONFLICT","message":"create_id is stopping"}`
	})
	var timeout *TimeoutError
	if !errors.As(err, &timeout) || len(createIDs) != 2 || deleted != 0 {
		t.Fatalf("err = %T %v, %d creates, %d deletes", err, err, len(createIDs), deleted)
	}
}
