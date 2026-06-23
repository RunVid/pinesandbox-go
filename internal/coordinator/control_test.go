package coordinator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestTabs(t *testing.T) {
	var patchBody map[string]any
	var closed bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sessions/s/tabs" && r.Method == "GET":
			_, _ = io.WriteString(w, `{"tabs":[{"target_id":"t1"},{"target_id":"t2"}]}`)
		case r.URL.Path == "/sessions/s/tabs" && r.Method == "POST":
			_, _ = io.WriteString(w, `{"tab":{"target_id":"new","url":"https://x"}}`)
		case r.URL.Path == "/sessions/s/tabs/t1" && r.Method == "PATCH":
			_ = json.NewDecoder(r.Body).Decode(&patchBody)
			_, _ = io.WriteString(w, `{"tab":{"target_id":"t1","active":true}}`)
		case r.URL.Path == "/sessions/s/tabs/t1" && r.Method == "DELETE":
			closed = true
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	list, err := c.ListTabs(context.Background(), "ps_", "s")
	if err != nil {
		t.Fatalf("ListTabs: %v", err)
	}
	if len(list) != 2 || list[0].TargetID != "t1" || list[1].TargetID != "t2" {
		t.Errorf("tabs = %+v, want 2 typed tabs t1/t2", list)
	}
	tab, err := c.CreateTab(context.Background(), "ps_", "s", "https://x", "lbl")
	if err != nil || tab.TargetID != "new" || tab.URL != "https://x" {
		t.Errorf("CreateTab = %+v, %v, want typed tab target_id=new", tab, err)
	}
	active := true
	patched, err := c.PatchTab(context.Background(), "ps_", "s", "t1", PatchTabOptions{Active: &active})
	if err != nil {
		t.Fatalf("PatchTab: %v", err)
	}
	if patched.TargetID != "t1" || !patched.Active {
		t.Errorf("PatchTab returned %+v, want typed tab t1 active=true", patched)
	}
	if patchBody["active"] != true {
		t.Errorf("patch body = %v", patchBody)
	}
	if err := c.CloseTab(context.Background(), "ps_", "s", "t1"); err != nil || !closed {
		t.Errorf("CloseTab err=%v closed=%v", err, closed)
	}
}

// TestExtractField_MissingFieldErrors verifies a 200 whose envelope lacks the expected
// field is a clear error, not a silent (nil, nil).
func TestExtractField_MissingFieldErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{}`) // no "tab"
	})
	if _, err := c.CreateTab(context.Background(), "ps_", "s", "https://x", ""); err == nil {
		t.Fatal("expected an error for a response missing the \"tab\" field")
	}
}

func TestControlState(t *testing.T) {
	var ifMatch, idem, force string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/sessions/s/control" && r.Method == "GET":
			w.Header().Set("ETag", `"v1"`)
			_, _ = io.WriteString(w, `{"mode":"agent"}`)
		case r.URL.Path == "/v1/sessions/s/control" && r.Method == "PATCH":
			ifMatch = r.Header.Get("If-Match")
			idem = r.Header.Get("Idempotency-Key")
			force = r.URL.Query().Get("force")
			w.Header().Set("ETag", `"v2"`)
			_, _ = io.WriteString(w, `{"mode":"control"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	cs, err := c.GetControl(context.Background(), "ct_", "s")
	if err != nil {
		t.Fatalf("GetControl: %v", err)
	}
	if cs.ETag != `"v1"` || !contains(string(cs.State), "agent") {
		t.Errorf("control = %+v", cs)
	}
	out, err := c.PatchControl(context.Background(), "ct_", "s", map[string]any{"mode": "control"},
		PatchControlOptions{IfMatch: `"v1"`, IdempotencyKey: "idem-1"})
	if err != nil {
		t.Fatalf("PatchControl: %v", err)
	}
	if out.ETag != `"v2"` {
		t.Errorf("new etag = %q", out.ETag)
	}
	if ifMatch != `"v1"` || idem != "idem-1" {
		t.Errorf("headers: if-match=%q idem=%q", ifMatch, idem)
	}
	if force != "" {
		t.Errorf("force should be absent: %q", force)
	}
}

// The typed control-event iterator is covered in stream_test.go.

func TestControlNotifyAndDesktopAndHandoffs(t *testing.T) {
	var notifyIfMatch string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions/s/control/notify":
			notifyIfMatch = r.Header.Get("If-Match")
			w.WriteHeader(200)
		case "/v1/sessions/s/desktop-token":
			_, _ = io.WriteString(w, `{"token":"dt_x","expires_at":"2026-01-01T00:00:30Z"}`)
		case "/v1/sessions/s/handoffs":
			if r.URL.Query().Get("limit") != "5" {
				t.Errorf("limit = %q", r.URL.Query().Get("limit"))
			}
			_, _ = io.WriteString(w, `{"handoffs":[{"id":"h1"}]}`)
		case "/v1/sessions/s/handoffs/h1":
			_, _ = io.WriteString(w, `{"id":"h1","reason":"teach"}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})

	if err := c.ControlNotify(context.Background(), "ct_", "s", "human_needed", "stuck", `"v1"`); err != nil {
		t.Fatalf("ControlNotify: %v", err)
	}
	if notifyIfMatch != `"v1"` {
		t.Errorf("notify if-match = %q", notifyIfMatch)
	}
	dt, err := c.MintDesktopToken(context.Background(), "ct_", "s")
	if err != nil || dt.Token != "dt_x" {
		t.Fatalf("MintDesktopToken = %+v, %v", dt, err)
	}
	hs, err := c.ListHandoffs(context.Background(), "ct_", "s", 5, "")
	if err != nil || !contains(string(hs), "h1") {
		t.Fatalf("ListHandoffs = %s, %v", hs, err)
	}
	h, err := c.GetHandoff(context.Background(), "ct_", "s", "h1")
	if err != nil || !contains(string(h), "teach") {
		t.Fatalf("GetHandoff = %s, %v", h, err)
	}
}
