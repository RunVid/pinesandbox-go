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
	if !tabsInclude(t, tabs, "https://example.com") {
		t.Errorf("tabs %s do not include example.com", tabs)
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
		raw, err := sess.Agent().Result(ctx)
		if err != nil {
			// While a turn is in flight, /agent/result is a 409 task-not-ready — poll again.
			var ae *pine.APIError
			if errors.As(err, &ae) && ae.Status == 409 {
				if time.Now().After(deadline) {
					t.Fatalf("agent turn still in flight past the window (last: %s)", ae.ProblemType)
				}
				time.Sleep(5 * time.Second)
				continue
			}
			t.Fatalf("agent.Result: %v", err)
		}
		var r map[string]any
		_ = json.Unmarshal(raw, &r)
		if reason, _ = r["terminal_reason"].(string); reason != "" {
			break
		}
		// Cross-check the Task state stays a valid enum (idle/running/paused).
		if st, serr := sess.Agent().Status(ctx); serr == nil {
			var task map[string]any
			_ = json.Unmarshal(st, &task)
			if s, _ := task["state"].(string); s != "" && s != "idle" && s != "running" && s != "paused" {
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

func tabsInclude(t *testing.T, tabsJSON json.RawMessage, prefix string) bool {
	t.Helper()
	var tabs []map[string]any
	if err := json.Unmarshal(tabsJSON, &tabs); err != nil {
		t.Fatalf("tabs not a JSON array: %v", err)
	}
	for _, tab := range tabs {
		if u, _ := tab["url"].(string); strings.HasPrefix(u, prefix) {
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
