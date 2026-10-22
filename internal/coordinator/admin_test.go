package coordinator

import (
	"context"
	"io"
	"net/http"
	"testing"
)

func TestAdminRoutes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/state" && r.Method == "GET":
			_, _ = io.WriteString(w, `{"snapshot_id":"snap-1"}`)
		case r.URL.Path == "/v1/coord/capture" && r.Method == "POST":
			_, _ = io.WriteString(w, `{"snapshot_id":"snap-2","skipped":false}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()

	if s, err := c.LatestSnapshot(ctx, "ct_"); err != nil || !contains(string(s), "snap-1") {
		t.Fatalf("LatestSnapshot = %s, %v", s, err)
	}
	if cap, err := c.Capture(ctx, "ct_"); err != nil || !contains(string(cap), "snap-2") {
		t.Fatalf("Capture = %s, %v", cap, err)
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
