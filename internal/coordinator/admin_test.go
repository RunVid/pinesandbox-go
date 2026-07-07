package coordinator

import (
	"context"
	"io"
	"net/http"
	"testing"
)

func TestAdminRoutes(t *testing.T) {
	var claimBody string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/health":
			if _, ok := r.Header["X-Pine-Auth"]; ok {
				t.Error("health must be token-less")
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.URL.Path == "/metrics":
			if r.Header.Get("Accept") != "text/plain" {
				t.Errorf("metrics Accept = %q", r.Header.Get("Accept"))
			}
			_, _ = io.WriteString(w, "pine_up 1\n")
		case r.URL.Path == "/state" && r.Method == "GET":
			_, _ = io.WriteString(w, `{"snapshot_id":"snap-1"}`)
		case r.URL.Path == "/v1/coord/capture" && r.Method == "POST":
			_, _ = io.WriteString(w, `{"snapshot_id":"snap-2","skipped":false}`)
		case r.URL.Path == "/downloads/orphans" && r.Method == "GET":
			_, _ = io.WriteString(w, `{"orphans":[{"guid":"g1"}]}`)
		case r.URL.Path == "/downloads/orphans/g1/claim" && r.Method == "POST":
			b, _ := io.ReadAll(r.Body)
			claimBody = string(b)
			_, _ = io.WriteString(w, `{"claimed":true}`)
		case r.URL.Path == "/downloads/orphans/g1" && r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()

	if h, err := c.Health(ctx); err != nil || !contains(string(h), "ok") {
		t.Fatalf("Health = %s, %v", h, err)
	}
	if m, err := c.Metrics(ctx); err != nil || !contains(string(m), "pine_up") {
		t.Fatalf("Metrics = %s, %v", m, err)
	}
	if s, err := c.LatestSnapshot(ctx, "ct_"); err != nil || !contains(string(s), "snap-1") {
		t.Fatalf("LatestSnapshot = %s, %v", s, err)
	}
	if cap, err := c.Capture(ctx, "ct_"); err != nil || !contains(string(cap), "snap-2") {
		t.Fatalf("Capture = %s, %v", cap, err)
	}
	if o, err := c.ListOrphanDownloads(ctx, "ct_"); err != nil || !contains(string(o), "g1") {
		t.Fatalf("ListOrphanDownloads = %s, %v", o, err)
	}
	if _, err := c.ClaimOrphanDownload(ctx, "ct_", "g1", "sess-1", "f.pdf"); err != nil {
		t.Fatalf("ClaimOrphanDownload: %v", err)
	}
	if !contains(claimBody, `"session_name":"sess-1"`) || !contains(claimBody, `"filename":"f.pdf"`) {
		t.Errorf("claim body = %s", claimBody)
	}
	if err := c.DiscardOrphanDownload(ctx, "ct_", "g1"); err != nil {
		t.Fatalf("DiscardOrphanDownload: %v", err)
	}
}

func TestLatestSnapshot_404IsNil(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"type":"/errors/no-snapshot","status":404}`)
	})
	snap, err := c.LatestSnapshot(context.Background(), "ct_")
	if err != nil {
		t.Fatalf("LatestSnapshot 404 should be (nil,nil), got err %v", err)
	}
	if snap != nil {
		t.Errorf("snap = %s, want nil", snap)
	}
}
