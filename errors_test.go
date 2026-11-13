package pinesandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestControlSentinels_ErrorsIsBySlug: the agent-lane sentinels match the live wire error by
// problem-type slug, while errors.As still reaches the full detail.
func TestControlSentinels_ErrorsIsBySlug(t *testing.T) {
	inFlight := &APIError{Status: 409, ProblemType: "/errors/task-not-ready", Detail: "result not ready", RequestID: "req-1"}

	if !errors.Is(inFlight, ErrTaskNotReady) {
		t.Error("errors.Is(task-not-ready, ErrTaskNotReady) = false, want true")
	}
	if errors.Is(inFlight, ErrSessionBusy) {
		t.Error("a task-not-ready error must NOT match ErrSessionBusy")
	}

	// The session cap (CreateSession at the concurrent limit) matches ErrSessionLimit,
	// not the unrelated ErrSessionBusy (both are 409).
	atCap := &APIError{Status: 409, ProblemType: "/errors/session-limit"}
	if !errors.Is(atCap, ErrSessionLimit) {
		t.Error("errors.Is(session-limit, ErrSessionLimit) = false, want true")
	}
	if errors.Is(atCap, ErrSessionBusy) {
		t.Error("a session-limit error must NOT match ErrSessionBusy")
	}

	// errors.As still reaches the full wire detail (the sentinel is not a lossy wrapper).
	var ae *APIError
	if !errors.As(inFlight, &ae) || ae.Status != 409 || ae.RequestID != "req-1" {
		t.Errorf("errors.As lost the wire detail: %+v", ae)
	}

	// A slug-less error (a non-problem+json failure) matches no sentinel.
	if errors.Is(&APIError{Status: 500}, ErrActionNotImplemented) {
		t.Error("a slug-less error must not match a sentinel")
	}
}

func TestResourceSentinelsRemainDistinct(t *testing.T) {
	missingSession := &APIError{Status: 404, ProblemType: "/errors/session-not-found"}
	if !errors.Is(missingSession, ErrSessionNotFound) {
		t.Error("session-not-found must match ErrSessionNotFound")
	}
	if errors.Is(&APIError{Status: 404, ProblemType: "/errors/sandbox-not-found"}, ErrSessionNotFound) {
		t.Error("sandbox-not-found must not match ErrSessionNotFound")
	}
	if !errors.Is(&APIError{Status: 409, ProblemType: "/errors/control-not-held"}, ErrControlNotHeld) {
		t.Error("control-not-held must match ErrControlNotHeld")
	}
}

func TestIsRetryableUsesOnlyServerAPIJudgment(t *testing.T) {
	if !IsRetryable(&APIError{Status: 503, ProblemType: "/errors/sandbox-not-ready", Retryable: true}) {
		t.Error("retryable APIError must be retryable")
	}
	if IsRetryable(&APIError{Status: 503, ProblemType: "/errors/bind-not-configured", Retryable: false}) {
		t.Error("explicitly terminal 503 must not be retryable")
	}
	if IsRetryable(errors.New("connection reset")) {
		t.Error("opaque/transport errors must not be declared replay-safe")
	}
}

// TestNoTaskSentinels_MatchTheWirePairsTheCoordinatorEmits: the two idle-session cases the
// coordinator actually produces map to two DISTINCT sentinels. The historical single
// sentinel carried {404, /errors/no-active-task} — a pair emitted by neither path — so
// read-path checks silently never matched and consumers mislabeled valid idle sessions as
// stale (duplicate CreateSession → 409 → 500, the 2026-07-14 incident branch).
func TestNoTaskSentinels_MatchTheWirePairsTheCoordinatorEmits(t *testing.T) {
	// Task READ on a never-ran session: 404 /errors/task-not-found.
	read := &APIError{Status: 404, ProblemType: "/errors/task-not-found", Detail: "no task"}
	if !errors.Is(read, ErrTaskNotFound) {
		t.Error("errors.Is(read 404 task-not-found, ErrTaskNotFound) = false, want true")
	}
	if errors.Is(read, ErrNoActiveTurn) {
		t.Error("a task-not-found read must NOT match ErrNoActiveTurn")
	}

	// Task MUTATION with no turn in flight: 409 /errors/no-active-task.
	mutation := &APIError{Status: 409, ProblemType: "/errors/no-active-task", Detail: "nothing to steer"}
	if !errors.Is(mutation, ErrNoActiveTurn) {
		t.Error("errors.Is(mutation 409 no-active-task, ErrNoActiveTurn) = false, want true")
	}
	if errors.Is(mutation, ErrTaskNotFound) {
		t.Error("a no-active-task mutation must NOT match ErrTaskNotFound")
	}

	// Task MUTATION that RACES a turn ending mid-request: the coordinator's
	// writeTaskControlResult emits 409 /errors/no-active-turn (taskbroker.ErrNoActiveTurn).
	// The same sentinel must match it — same actionable outcome (start a turn).
	race := &APIError{Status: 409, ProblemType: "/errors/no-active-turn", Detail: "turn ended"}
	if !errors.Is(race, ErrNoActiveTurn) {
		t.Error("errors.Is(mutation 409 no-active-turn race, ErrNoActiveTurn) = false, want true")
	}

	// The deprecated sentinel keeps matching exactly what it always matched in practice
	// (the mutation slug) — no silent behavior change for existing consumers.
	if !errors.Is(mutation, ErrNoActiveTask) {
		t.Error("deprecated ErrNoActiveTask must still match the mutation pair")
	}
	if errors.Is(read, ErrNoActiveTask) {
		t.Error("deprecated ErrNoActiveTask must not silently start matching reads — consumers must adopt ErrTaskNotFound deliberately")
	}
}

// TestControlSentinels_SlugsInTaxonomy: every sentinel's slug is a REAL coordinator problem
// type (pinned in error-taxonomy.json) — a typo or a renamed slug fails here, keeping the
// SDK's named errors in lockstep with the server's taxonomy.
// Skips on the mirror (the contract artifact isn't published with the SDK).
func TestControlSentinels_SlugsInTaxonomy(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "contract", "error-taxonomy.json"))
	if err != nil {
		t.Skipf("error-taxonomy artifact not present (mirror build): %v", err)
	}
	var f struct {
		Entries []struct {
			Type string `json:"type"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse taxonomy: %v", err)
	}
	known := make(map[string]bool, len(f.Entries))
	for _, e := range f.Entries {
		known[e.Type] = true
	}
	for _, s := range []*APIError{ErrControlNotHeld, ErrSessionNotFound, ErrComputerStopped, ErrTaskNotReady, ErrSessionBusy, ErrSessionLimit, ErrTaskNotFound, ErrStaleTask, ErrHistoryExpired, ErrEventsUnavailable, ErrNoActiveTurn, ErrNoActiveTask, ErrActionNotImplemented, ErrFeatureDisabled, ErrSessionNameTaken, ErrStaleTurn, ErrNoPendingRequest, ErrTurnNotFound, ErrAuthorInFlight, ErrSecretInSkill, ErrSkillsUnavailable} {
		for _, slug := range append([]string{s.ProblemType}, s.AltProblemTypes...) {
			if !known[slug] {
				t.Errorf("sentinel slug %q is not in error-taxonomy.json (typo or renamed server slug)", slug)
			}
		}
	}
}

// TestErrFeatureDisabled_ArtifactMethodsWithoutTheFeature: a project without the artifacts
// feature gets 403 /errors/feature-disabled from every artifact route; each artifact method
// surfaces it as ErrFeatureDisabled (not a token rejection), while the workdir file reads a
// turn's result names keep working.
func TestErrFeatureDisabled_ArtifactMethodsWithoutTheFeature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/sessions/s1/artifacts") {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"type":"/errors/feature-disabled","status":403,"detail":"artifacts","retryable":false}`)
			return
		}
		fmt.Fprint(w, "file-bytes")
	}))
	defer srv.Close()
	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(buildTestConnection(t, "http://unused", srv.URL), "sb-1", "ct_tok", "running"); err != nil {
		t.Fatal(err)
	}
	sess, err := comp.AdoptSession("s1", "ps_tok")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	calls := map[string]func() error{
		"ListArtifacts":  func() error { _, e := sess.ListArtifacts(ctx, ""); return e },
		"ReadArtifact":   func() error { _, e := sess.ReadArtifact(ctx, "a1"); return e },
		"OpenArtifact":   func() error { _, e := sess.OpenArtifact(ctx, "a1"); return e },
		"ZipArtifacts":   func() error { _, e := sess.ZipArtifacts(ctx, ""); return e },
		"UploadArtifact": func() error { _, e := sess.UploadArtifact(ctx, "o.txt", []byte("b")); return e },
	}
	for name, call := range calls {
		err := call()
		if !errors.Is(err, ErrFeatureDisabled) || IsRetryable(err) {
			t.Errorf("%s err = %T (%v), want ErrFeatureDisabled, not retryable", name, err, err)
		}
	}
	rc, err := sess.OpenFile(ctx, "out/report.pdf")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer rc.Close()
	if b, err := io.ReadAll(rc); err != nil || string(b) != "file-bytes" {
		t.Errorf("OpenFile = %q, %v", b, err)
	}
}

// TestAgentReadSentinels_EachAnswerItsOwn: an agent read's answers about its task and
// history each surface as their own sentinel, never another's, and only the
// unreadable history is retryable.
func TestAgentReadSentinels_EachAnswerItsOwn(t *testing.T) {
	answers := []struct {
		sentinel  *APIError
		status    int
		retryable bool
	}{
		{ErrTaskNotFound, 404, false},
		{ErrStaleTask, 409, false},
		{ErrHistoryExpired, 410, false},
		{ErrEventsUnavailable, 503, true},
	}
	for _, a := range answers {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(a.status)
			fmt.Fprintf(w, `{"type":%q,"status":%d,"retryable":%t}`, a.sentinel.ProblemType, a.status, a.retryable)
		}))
		comp := newComputer("c1", make([]byte, 32))
		if err := comp.adopt(buildTestConnection(t, "http://unused", srv.URL), "sb-1", "ct_tok", "running"); err != nil {
			t.Fatal(err)
		}
		sess, err := comp.AdoptSession("s1", "ps_tok")
		if err != nil {
			t.Fatal(err)
		}
		_, err = sess.Agent().EventsPage(context.Background(), EventsPageOptions{})
		srv.Close()
		if !errors.Is(err, a.sentinel) || IsRetryable(err) != a.retryable {
			t.Errorf("%s: err = %T (%v), want its sentinel, retryable %t", a.sentinel.ProblemType, err, err, a.retryable)
		}
		for _, other := range answers {
			if other.sentinel != a.sentinel && errors.Is(err, other.sentinel) {
				t.Errorf("%s also matches %s", a.sentinel.ProblemType, other.sentinel.ProblemType)
			}
		}
	}
}

// TestErrComputerStopped_EveryCallOnceAStopBegins: once a Stop has begun the Computer
// answers every call 409 /errors/computer-stopped; each call and stream surfaces it as
// ErrComputerStopped, not retryable, with the wire detail still reachable.
func TestErrComputerStopped_EveryCallOnceAStopBegins(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"type":"/errors/computer-stopped","status":409,"detail":"A Stop has begun on this Computer; it accepts no calls until it is started again","request_id":"req-stopped","retryable":false}`)
	}))
	defer srv.Close()
	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(buildTestConnection(t, "http://unused", srv.URL), "sb-1", "ct_tok", "running"); err != nil {
		t.Fatal(err)
	}
	sess, err := comp.AdoptSession("s1", "ps_tok")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	calls := map[string]func() error{
		"Sessions":  func() error { _, e := comp.Sessions(ctx); return e },
		"ListFiles": func() error { _, e := sess.ListFiles(ctx, ListFilesOptions{}); return e },
		"Exec": func() error {
			_, e := sess.Exec(ctx, "ls", ExecOptions{}, func(map[string]any) error { return nil })
			return e
		},
	}
	for name, call := range calls {
		err := call()
		var ae *APIError
		if !errors.Is(err, ErrComputerStopped) || IsRetryable(err) || !errors.As(err, &ae) || ae.RequestID != "req-stopped" {
			t.Errorf("%s err = %T (%v), want ErrComputerStopped, not retryable, with its request id", name, err, err)
		}
		if errors.Is(err, ErrSessionBusy) || errors.Is(err, ErrControlNotHeld) {
			t.Errorf("%s: a computer-stopped answer must not match another 409 sentinel", name)
		}
	}
}

// TestErrCapacityExceeded_PublicSurface: the facade sentinel and error type are the
// control-plane ones, so callers branch with errors.Is and reach detail with errors.As.
func TestErrCapacityExceeded_PublicSurface(t *testing.T) {
	var err error = &CapacityExceededError{}
	if !errors.Is(err, ErrCapacityExceeded) {
		t.Error("errors.Is(CapacityExceededError, ErrCapacityExceeded) = false, want true")
	}
	if errors.Is(&ControlPlaneError{}, ErrCapacityExceeded) {
		t.Error("a generic ControlPlaneError must NOT match ErrCapacityExceeded")
	}
}

// TestCreateComputer_CreateRateLimitedReachesCaller: a create refused for the project's
// per-minute rate reaches the caller as ErrCreateRateLimited with the server's Retry-After.
// The SDK sends the create once and nothing after it: no retry, no readiness wait, no cleanup
// of a Computer sandbox that was never started.
func TestCreateComputer_CreateRateLimitedReachesCaller(t *testing.T) {
	var requests atomic.Int32
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/computer-sandboxes" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"code":"COMPUTER_CREATE_RATE_LIMITED","message":"Nothing was created; try again in 42 seconds."}`)
	}))
	defer control.Close()
	client := &Client{conn: buildTestConnection(t, control.URL, control.URL)}

	_, err := client.CreateComputer(context.Background(), AttachOptions{})

	if !errors.Is(err, ErrCreateRateLimited) || errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("err = %T (%v), want ErrCreateRateLimited only", err, err)
	}
	var rl *CreateRateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter != 42*time.Second {
		t.Fatalf("err = %#v, want *CreateRateLimitedError with RetryAfter 42s", err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("control plane saw %d requests, want the single create", got)
	}
}

func TestStartErrorsCarryTheRuntimeStateAndReason(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	release := &ReadyTimeoutError{SandboxID: "rt-old", LastState: "saving", LastReason: "STOP_ACCEPTED", Waited: 300 * time.Second,
		wait: readyWait{target: targetReleased, started: started}}
	if got, want := release.Error(), "pinesandbox: Computer runtime rt-old wasn't released after 300 s (last state saving, reason STOP_ACCEPTED); nothing was started"; got != want {
		t.Errorf("release timeout = %q, want %q", got, want)
	}
	ready := &ReadyTimeoutError{SandboxID: "rt-1", LastState: "pending", Waited: 2500 * time.Millisecond,
		wait: readyWait{target: statusRunning, newRuntime: true}}
	if got, want := ready.Error(), "pinesandbox: New Computer runtime rt-1 wasn't running after 3 s (last state pending)"; got != want {
		t.Errorf("ready timeout = %q, want %q", got, want)
	}
	var err error = release
	if !errors.Is(err, context.DeadlineExceeded) || IsRetryable(err) {
		t.Error("ReadyTimeoutError must be a deadline and never retryable")
	}
	failed := &SandboxFailedError{SandboxID: "rt-1", State: "failed", Reason: "STOPPED_UNEXPECTEDLY", wait: readyWait{target: statusRunning, newRuntime: true}}
	if got, want := failed.Error(), "pinesandbox: New Computer runtime rt-1 reported state failed, reason STOPPED_UNEXPECTEDLY before it was running"; got != want {
		t.Errorf("failed = %q, want %q", got, want)
	}
	retained := &SandboxRetainedError{SandboxID: "rt-old", State: "failed", Reason: "PAUSE_FAILED"}
	if got, want := retained.Error(), "pinesandbox: Start refused: Computer runtime rt-old hasn't been released (state failed, reason PAUSE_FAILED); nothing was started; recover it with RecoverComputer, or Stop it and then Start"; got != want {
		t.Errorf("retained = %q, want %q", got, want)
	}
}

func TestComputerAlreadyStartedError(t *testing.T) {
	var err error = &ComputerAlreadyStartedError{SandboxID: "rt-1", State: "running"}
	var started *ComputerAlreadyStartedError
	if !errors.As(err, &started) || started.SandboxID != "rt-1" || started.State != "running" || started.Reason != "" {
		t.Fatalf("errors.As = %+v", started)
	}
	var retained *SandboxRetainedError
	if errors.As(err, &retained) || IsRetryable(err) {
		t.Error("ComputerAlreadyStartedError must not be a SandboxRetainedError, nor retryable")
	}
	if got, want := err.Error(), "pinesandbox: Start refused: Computer runtime rt-1 is already started (state running); nothing was started"; got != want {
		t.Errorf("Start = %q, want %q", got, want)
	}
	refused := &ComputerAlreadyStartedError{SandboxID: "rt-1", State: "pending", Reason: "STARTING", column: admissionRecover}
	if got, want := refused.Error(), "pinesandbox: Recover refused: Computer runtime rt-1 has no recorded failure (state pending, reason STARTING); nothing was released or started"; got != want {
		t.Errorf("Recover = %q, want %q", got, want)
	}
}

func TestPortalRequestsNameTheSDK(t *testing.T) {
	var agent string
	client, err := newClient(ClientOptions{Endpoint: "https://ua.pinesandbox.io", APIKey: "pk_test"}, clientHooks{httpClient: &http.Client{
		Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			agent = r.Header.Get("User-Agent")
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Request: r,
				Body: io.NopCloser(strings.NewReader(`{"locations":[],"default_location":{"country":"US"}}`))}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AvailableLocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if agent != "pinesandbox-go/"+Version {
		t.Errorf("User-Agent = %q, want pinesandbox-go/%s", agent, Version)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReadinessBudgetErrorPutsTheCallersDeadlineFirst(t *testing.T) {
	handle := newSandboxHandle(&fakeCP{}, "rt-1", "pending")
	w := readyWait{target: statusRunning, newRuntime: true, started: handle.clock()}
	expired, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	var rt *ReadyTimeoutError
	if err := readinessBudgetError(expired, expired, handle.readyTimeout(w), handle, w); errors.As(err, &rt) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline = %T %v, want its context error", err, err)
	}
	if err := readinessBudgetError(context.Background(), expired, context.DeadlineExceeded, handle, w); !errors.As(err, &rt) || rt.SandboxID != "rt-1" {
		t.Fatalf("SDK budget = %T %v, want *ReadyTimeoutError", err, err)
	}
}
