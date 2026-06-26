//go:build e2e

// End-to-end integration suite — see doc.go + contract/E2E_JOURNEYS.md. Mirrors the Ruby
// integration suite's proven journeys against the same loop.
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	pine "go.pinesandbox.io/computer"
)

// attachTimeout bounds a single provision+bind; e2e is slow (real cold pod).
const attachTimeout = 5 * time.Minute

func newClient(t *testing.T) *pine.Client {
	t.Helper()
	endpoint, apiKey := os.Getenv("PINE_SANDBOX_ENDPOINT"), os.Getenv("PINE_SANDBOX_API_KEY")
	if endpoint == "" || apiKey == "" {
		t.Skip("e2e needs PINE_SANDBOX_ENDPOINT + PINE_SANDBOX_API_KEY (unset → skip; see contract/E2E_JOURNEYS.md)")
	}
	c, err := pine.NewClient(pine.ClientOptions{
		Endpoint:    endpoint,
		APIKey:      apiKey,
		ControlHost: os.Getenv("PINE_SANDBOX_CONTROL_HOST"), // "" → derive; local loop sets api.lvh.me:18080
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func newCtx(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), attachTimeout)
}

// teardown is the safety-net cleanup — best-effort Kill, even if the test already Stopped
// (Stop nils the binding, so a follow-up Kill is a no-op). Runs on every exit path so a
// failing test never leaks a real Computer.
func teardown(comp *pine.Computer) {
	comp.Kill(context.Background())
}

// driveALittle opens a browser session and navigates a tab — minimal real browser state so a
// capture has something to persist. Returns the session.
func driveALittle(t *testing.T, ctx context.Context, comp *pine.Computer, tag string) *pine.Session {
	t.Helper()
	sess, err := comp.CreateSession(ctx, pine.CreateSessionOptions{Name: "e2e-" + tag, Browser: true})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := sess.CreateTab(ctx, "https://example.com", ""); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	return sess
}

// J1 — provision → drive → teardown. The core "the whole stack wires together" smoke.
func TestE2E_J1_Lifecycle(t *testing.T) {
	c := newClient(t)
	ctx, cancel := newCtx(t)
	defer cancel()

	comp, err := c.CreateComputer(ctx, pine.AttachOptions{})
	if err != nil {
		t.Fatalf("CreateComputer: %v", err)
	}
	defer teardown(comp)

	if !strings.HasPrefix(comp.ComputerToken(), "ct_") {
		t.Errorf("computer token = %q, want a ct_", comp.ComputerToken())
	}

	sess := driveALittle(t, ctx, comp, "cold")
	tabs, err := sess.ListTabs(ctx)
	if err != nil {
		t.Fatalf("ListTabs: %v", err)
	}
	if !tabsInclude(tabs, "https://example.com") {
		t.Errorf("tabs %+v do not include example.com", tabs)
	}

	res, err := sess.Exec(ctx, "echo pine-e2e-ok", pine.ExecOptions{}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Errorf("exit = %v, want 0 (stderr: %q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pine-e2e-ok") {
		t.Errorf("stdout %q missing the marker", res.Stdout)
	}

	// Persistence endpoint reachable (nil = nothing captured yet is fine; a 503 = not
	// configured would error).
	if _, err := comp.LatestSnapshot(ctx); err != nil {
		t.Errorf("LatestSnapshot errored (persistence not configured?): %v", err)
	}

	// Browser-safe delegation: computer_host must be a FULL URI (scheme included) per
	// computer-api.yaml — the web SDK derives the desktop ws/wss scheme from it. The scheme
	// is env-dependent (https on staging/prod, http on the local lvh.me loop), so assert a
	// scheme is present + the Computer host shape, not a hardcoded https.
	if dc, derr := sess.Delegate(ctx); derr != nil {
		t.Errorf("Delegate: %v", derr)
	} else if (!strings.HasPrefix(dc.ComputerHost, "https://") && !strings.HasPrefix(dc.ComputerHost, "http://")) || !strings.Contains(dc.ComputerHost, ".computer.") {
		t.Errorf("delegation computer_host = %q, want a full http(s)://<id>.computer.<zone> URI", dc.ComputerHost)
	}

	if gone, err := comp.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	} else if !gone {
		t.Error("Stop did not confirm the pod gone")
	}
}

// J2 — capture → restore across pods. The highest-value journey: a successful re-attach
// PROVES restore (coord acquired the epoch, pulled this Computer's snapshot, decrypted with
// the split key, restored the profile — a restore failure surfaces as a bind error, never a
// silent cold start).
func TestE2E_J2_Persistence(t *testing.T) {
	c := newClient(t)

	// Phase 1 — cold attach, drive real state, force a durable checkpoint.
	ctx1, cancel1 := newCtx(t)
	defer cancel1()
	comp, err := c.CreateComputer(ctx1, pine.AttachOptions{})
	if err != nil {
		t.Fatalf("CreateComputer: %v", err)
	}
	id, key := comp.ID(), comp.Key()

	driveALittle(t, ctx1, comp, "persist")
	if _, err := comp.Capture(ctx1); err != nil {
		teardown(comp)
		t.Fatalf("Capture: %v", err)
	}
	snap := mustSnapshot(t, ctx1, comp)
	if v, _ := snap["envelope_version"].(float64); v != 1 {
		t.Errorf("envelope_version = %v, want 1 (KMS-wrapped envelope_v1)", snap["envelope_version"])
	}
	if snap["computer_id"] != id {
		t.Errorf("snapshot computer_id = %v, want %s", snap["computer_id"], id)
	}
	if size, _ := snap["size_bytes"].(float64); size <= 0 {
		t.Errorf("size_bytes = %v, want > 0", snap["size_bytes"])
	}
	if _, leaked := snap["ciphertext"]; leaked {
		t.Error("GET /state leaked the ciphertext — the encrypted payload must never leave coord")
	}
	if _, err := comp.Stop(ctx1); err != nil {
		teardown(comp)
		t.Fatalf("Stop: %v", err)
	}

	// Phase 2 — re-attach the SAME id+key onto a fresh pod. A successful bind == restore.
	ctx2, cancel2 := newCtx(t)
	defer cancel2()
	re, err := c.AttachComputer(ctx2, id, key, pine.AttachOptions{})
	if err != nil {
		t.Fatalf("re-attach (restore) failed: %v", err)
	}
	defer teardown(re)
	if !strings.HasPrefix(re.ComputerToken(), "ct_") {
		t.Errorf("re-attach token = %q, want a ct_", re.ComputerToken())
	}
	latest := mustSnapshot(t, ctx2, re)
	if latest["computer_id"] != id {
		t.Errorf("post-restore snapshot computer_id = %v, want %s", latest["computer_id"], id)
	}
	if _, err := re.Stop(ctx2); err != nil {
		t.Fatalf("Stop (re-attached): %v", err)
	}
}

// J3 — files + artifacts round-trip through the real execd.
func TestE2E_J3_Files(t *testing.T) {
	c := newClient(t)
	ctx, cancel := newCtx(t)
	defer cancel()

	comp, err := c.CreateComputer(ctx, pine.AttachOptions{})
	if err != nil {
		t.Fatalf("CreateComputer: %v", err)
	}
	defer teardown(comp)
	sess, err := comp.CreateSession(ctx, pine.CreateSessionOptions{Name: "e2e-files"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// exec writes a file into the workdir (where read_file/list_files serve from).
	const marker, fname = "pine-e2e-marker", "e2e_read.txt"
	if res, err := sess.Exec(ctx, "printf '%s' "+marker+" > "+fname, pine.ExecOptions{}, nil); err != nil {
		t.Fatalf("Exec write: %v", err)
	} else if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("write exit = %v (stderr %q)", res.ExitCode, res.Stderr)
	}

	// List with no pattern first (isolates the write), then verify the glob matches it.
	files, err := sess.ListFiles(ctx, pine.ListFilesOptions{})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	found := findFile(files, fname)
	if found == nil {
		t.Fatalf("ListFiles did not include %s (got %v)", fname, names(files))
	}
	if found.Size != int64(len(marker)) {
		t.Errorf("size = %d, want %d", found.Size, len(marker))
	}
	globbed, err := sess.ListFiles(ctx, pine.ListFilesOptions{Pattern: "e2e_read.*"})
	if err != nil {
		t.Fatalf("ListFiles(glob): %v", err)
	}
	if findFile(globbed, fname) == nil {
		t.Errorf("glob e2e_read.* did not match %s (got %v)", fname, names(globbed))
	}

	got, err := sess.ReadFile(ctx, fname)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != marker {
		t.Errorf("ReadFile = %q, want %q", got, marker)
	}

	// upload_artifact → list_artifacts round-trip.
	art, err := sess.UploadArtifact(ctx, "out.txt", []byte("artifact-bytes"))
	if err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}
	if art.ID == "" {
		t.Error("uploaded artifact has no id")
	}
	back, err := sess.ReadArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if string(back) != "artifact-bytes" {
		t.Errorf("ReadArtifact = %q, want artifact-bytes", back)
	}

	_, _ = comp.Stop(ctx)
}

// J4 (optional) — a delegate-mode agent turn. Skips cleanly where no resident agent is
// configured (the run returns a 501/not-implemented).
func TestE2E_J4_Agent(t *testing.T) {
	c := newClient(t)
	ctx, cancel := newCtx(t)
	defer cancel()

	comp, err := c.CreateComputer(ctx, pine.AttachOptions{})
	if err != nil {
		t.Fatalf("CreateComputer: %v", err)
	}
	defer teardown(comp)
	sess := driveALittle(t, ctx, comp, "agent")

	if _, err := sess.Agent().Run(ctx, "Confirm the page title contains \"Example\".", pine.RunOptions{}); err != nil {
		var ae *pine.APIError
		if errors.As(err, &ae) && (ae.Status == 501 || ae.Status == 404) {
			t.Skipf("resident agent not configured here (run → %d); J4 needs PINE_MODEL_* on the pool", ae.Status)
		}
		t.Fatalf("agent.Run: %v", err)
	}

	// The Task is persistent (delegate mode); its `state` cycles idle→running→idle per turn.
	// The turn's OUTCOME is in /agent/result (TaskResult.terminal_reason). Poll the result
	// until this turn produces a terminal_reason — the outcome assertion (not the wire shape).
	deadline := time.Now().Add(5 * time.Minute)
	var reason string
	for {
		res, err := sess.Agent().Result(ctx)
		if err != nil {
			// While a turn is in flight the result isn't materialized — poll again. Uses the
			// typed sentinel (dogfoods errors.Is against the live coord problem-type).
			if errors.Is(err, pine.ErrTaskNotReady) {
				if time.Now().After(deadline) {
					t.Fatal("agent turn still in flight past the window")
				}
				time.Sleep(5 * time.Second)
				continue
			}
			t.Fatalf("agent.Result: %v", err)
		}
		// Typed AgentResult — assert the parse held against the LIVE wire (the typed
		// spine + Raw escape hatch both populated).
		if len(res.Raw) == 0 {
			t.Fatal("agent.Result: typed result has an empty Raw escape hatch")
		}
		if reason = res.TerminalReason; reason != "" {
			if res.Status == "" {
				t.Errorf("terminal result %q has empty status", reason)
			}
			break
		}
		// Cross-check the Task state stays a valid enum (idle/running/paused).
		if st, serr := sess.Agent().Status(ctx); serr == nil {
			if s := st.State; s != "" && s != "idle" && s != "running" && s != "paused" {
				t.Fatalf("unexpected Task.state %q (want idle/running/paused)", s)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent turn produced no terminal_reason within the window")
		}
		time.Sleep(5 * time.Second)
	}
	t.Logf("agent turn terminal_reason=%q", reason)
	_, _ = comp.Stop(ctx)
}

// J5 (optional) — the typed agent EVENT STREAM. Validates the resuming iterator against the
// LIVE TaskEvent wire: every event carries a non-empty type, the Raw escape hatch is
// populated, ids are monotonic, and the feed is bounded (a watchdog cancels it once the turn
// is terminal, so the test never hangs on a continuous feed). Skips cleanly where no resident
// agent is configured (run → 501/404), the same gate as J4.
func TestE2E_J5_AgentEventStream(t *testing.T) {
	c := newClient(t)
	ctx, cancel := newCtx(t)
	defer cancel()

	comp, err := c.CreateComputer(ctx, pine.AttachOptions{})
	if err != nil {
		t.Fatalf("CreateComputer: %v", err)
	}
	defer teardown(comp)
	sess := driveALittle(t, ctx, comp, "agent-stream")

	if _, err := sess.Agent().Run(ctx, "Confirm the page title contains \"Example\".", pine.RunOptions{}); err != nil {
		var ae *pine.APIError
		if errors.As(err, &ae) && (ae.Status == 501 || ae.Status == 404) {
			t.Skipf("resident agent not configured here (run → %d); J5 needs PINE_MODEL_* on the pool", ae.Status)
		}
		t.Fatalf("agent.Run: %v", err)
	}

	// Bounded stop: cancel the stream once this turn's result is terminal. The feed is
	// continuous (it does not EOF when a turn ends), so without this the iterator would
	// resume forever — the cancel is the deterministic terminator, not a feed EOF.
	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()
	go func() {
		for streamCtx.Err() == nil {
			if res, rerr := sess.Agent().Result(streamCtx); rerr == nil && res.TerminalReason != "" {
				time.Sleep(3 * time.Second) // let the turn's terminal event flush to the feed
				stopStream()
				return
			}
			time.Sleep(5 * time.Second)
		}
	}()

	var n int
	var lastID int64
	sawTerminal := false
	for ev, serr := range sess.Agent().Events(streamCtx, "") {
		if serr != nil {
			// The watchdog's cancel is the expected stop, not a failure.
			if errors.Is(serr, context.Canceled) || streamCtx.Err() != nil {
				break
			}
			var ae *pine.APIError
			if errors.As(serr, &ae) && (ae.Status == 501 || ae.Status == 404) {
				t.Skipf("agent event feed not implemented here (events → %d)", ae.Status)
			}
			t.Fatalf("agent events: %v", serr)
		}
		n++
		if ev.Type == "" {
			t.Errorf("event %d has an empty type (envelope did not parse)", n)
		}
		if len(ev.Raw) == 0 {
			t.Errorf("event %d (%s) has an empty Raw escape hatch", n, ev.Type)
		}
		if ev.EventID != 0 {
			if ev.EventID < lastID {
				t.Errorf("event id went backwards: %d after %d", ev.EventID, lastID)
			}
			lastID = ev.EventID
		}
		if ev.Terminal {
			sawTerminal = true
			break
		}
	}
	if n == 0 {
		t.Fatal("agent event stream delivered no events for a turn that ran (typed feed not wired?)")
	}
	t.Logf("agent event stream: %d typed events (terminal frame seen=%t, lastID=%d)", n, sawTerminal, lastID)
	_, _ = comp.Stop(ctx)
}

// --- helpers ---

// mustSnapshot polls LatestSnapshot until present, failing loud past the budget (the
// capture/persistence chain didn't land — never skip-as-green). PINE_CHECKPOINT_WAIT /
// _POLL tune the window (a freshly-rolled pool's first capture can lag).
func mustSnapshot(t *testing.T, ctx context.Context, comp *pine.Computer) map[string]any {
	t.Helper()
	wait := envDuration("PINE_CHECKPOINT_WAIT", 360*time.Second)
	poll := envDuration("PINE_CHECKPOINT_POLL", 15*time.Second)
	deadline := time.Now().Add(wait)
	for {
		raw, err := comp.LatestSnapshot(ctx)
		if err != nil {
			t.Fatalf("LatestSnapshot: %v", err)
		}
		if raw != nil {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("snapshot not JSON: %v", err)
			}
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("no snapshot after %v — capture did not land (coord→broker→GCS write NOT verified)", wait)
		}
		time.Sleep(poll)
	}
}

func tabsInclude(tabs []pine.Tab, prefix string) bool {
	for _, tab := range tabs {
		if strings.HasPrefix(tab.URL, prefix) {
			return true
		}
	}
	return false
}

func findFile(files []*pine.FileEntry, name string) *pine.FileEntry {
	for _, f := range files {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func names(files []*pine.FileEntry) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Name)
	}
	return out
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		if secs, err := time.ParseDuration(v + "s"); err == nil { // bare seconds, Ruby-compat
			return secs
		}
	}
	return def
}

// J6 — control lease + stateless session reuse. Guards the ct_/ps_ routing
// against the REAL coord (the non-enforcing unit stub can't): UpdateControl is
// ct_-only, so the pre-fix SDK — which routed control through the session ps_ —
// 403s here. Also exercises AdoptSession: rebuild a drivable handle from a
// persisted ps_ (the path a stateless/restarted backend takes).
func TestE2E_J6_ControlAndAdopt(t *testing.T) {
	c := newClient(t)
	ctx, cancel := newCtx(t)
	defer cancel()

	comp, err := c.CreateComputer(ctx, pine.AttachOptions{})
	if err != nil {
		t.Fatalf("CreateComputer: %v", err)
	}
	defer teardown(comp)
	sess := driveALittle(t, ctx, comp, "control")

	// Stateless reuse FIRST, while the session is still agent-driving (so the
	// drive isn't control-mode-gated): rebuild from the persisted {name, ps_}.
	adopted, err := comp.AdoptSession(sess.Name(), sess.Token())
	if err != nil {
		t.Fatalf("AdoptSession: %v", err)
	}
	if _, err := adopted.CreateTab(ctx, "https://example.org", ""); err != nil {
		t.Fatalf("drive via AdoptSession (persisted ps_): %v", err)
	}

	// Take control (ct_-only mutate, via the typed TakeControl helper — ETag fetch +
	// If-Match retry). A ps_-routed control mutate 403s here — the bug this guards.
	st, err := sess.TakeControl(ctx)
	if err != nil {
		t.Fatalf("TakeControl: %v (a 403 here is the ct_/ps_ routing bug)", err)
	}
	if st.Controller != pine.ControllerHuman {
		t.Errorf("controller after TakeControl = %q, want human", st.Controller)
	}
}
