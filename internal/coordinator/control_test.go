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
	var tabs []map[string]any
	if json.Unmarshal(list, &tabs); len(tabs) != 2 {
		t.Errorf("tabs = %s", list)
	}
	tab, err := c.CreateTab(context.Background(), "ps_", "s", "https://x", "lbl")
	if err != nil || !contains(string(tab), `"target_id":"new"`) {
		t.Errorf("CreateTab = %s, %v", tab, err)
	}
	active := true
	if _, err := c.PatchTab(context.Background(), "ps_", "s", "t1", PatchTabOptions{Active: &active}); err != nil {
		t.Fatalf("PatchTab: %v", err)
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

// TestControlEvents_StreamAndResume: the control-event feed streams + tracks the resume
// cursor, and sends Last-Event-ID on reconnect (the second leg resumes from the first's cursor).
func TestControlEvents_StreamAndResume(t *testing.T) {
	var leg2LastEventID string
	leg := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/s/control/events" {
			t.Errorf("path = %s", r.URL.Path)
		}
		leg++
		if leg == 1 {
			_, _ = io.WriteString(w, "id: 7\ndata: {\"mode\":\"agent\"}\n\n")
			return
		}
		leg2LastEventID = r.Header.Get("Last-Event-ID")
		_, _ = io.WriteString(w, "id: 8\ndata: {\"mode\":\"control\"}\n\n")
	})

	var seen []string
	collect := func(data []byte) error {
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		seen = append(seen, m["mode"].(string))
		return nil
	}
	cursor, err := c.ControlEvents(context.Background(), "ps_", "s", "", collect)
	if err != nil {
		t.Fatalf("ControlEvents leg 1: %v", err)
	}
	if cursor != "7" {
		t.Errorf("leg-1 cursor = %q, want 7", cursor)
	}
	// Reconnect with the cursor — the server must see it as Last-Event-ID and resume.
	cursor2, err := c.ControlEvents(context.Background(), "ps_", "s", cursor, collect)
	if err != nil {
		t.Fatalf("ControlEvents leg 2: %v", err)
	}
	if leg2LastEventID != "7" {
		t.Errorf("reconnect sent Last-Event-ID %q, want 7", leg2LastEventID)
	}
	if cursor2 != "8" {
		t.Errorf("leg-2 cursor = %q, want 8", cursor2)
	}
	if len(seen) != 2 || seen[0] != "agent" || seen[1] != "control" {
		t.Errorf("events = %v, want [agent control]", seen)
	}
}

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
