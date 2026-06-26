package pinesandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDriveHelpers_TypedActions pins the typed computer-use convenience wrappers:
// each sends the right action verb + canonical params (coordinate array, text,
// scroll fields) so callers don't hand-build the body.
func TestDriveHelpers_TypedActions(t *testing.T) {
	var action string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = map[string]any{}
		_ = json.Unmarshal(b, &body)
		action, _ = body["action"].(string)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(buildTestConnection(t, "http://unused", srv.URL), "sb-1", "ct_tok", "running"); err != nil {
		t.Fatal(err)
	}
	sess, err := comp.AdoptSession("s1", "ps_x")
	if err != nil {
		t.Fatal(err)
	}
	d := sess.Drive()
	ctx := context.Background()

	if _, err := d.Click(ctx, 12, 34); err != nil {
		t.Fatalf("Click: %v", err)
	}
	coord, _ := body["coordinate"].([]any)
	if action != "left_click" || len(coord) != 2 || coord[0].(float64) != 12 || coord[1].(float64) != 34 {
		t.Errorf("Click → action=%q body=%v", action, body)
	}

	if _, err := d.TypeText(ctx, "hello"); err != nil {
		t.Fatalf("TypeText: %v", err)
	}
	if action != "type" || body["text"] != "hello" {
		t.Errorf("TypeText → action=%q body=%v", action, body)
	}

	if _, err := d.Scroll(ctx, 1, 2, "down", 3); err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	if action != "scroll" || body["scroll_direction"] != "down" || body["scroll_amount"].(float64) != 3 {
		t.Errorf("Scroll → action=%q body=%v", action, body)
	}

	if _, err := d.Screenshot(ctx); err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if action != "screenshot" {
		t.Errorf("Screenshot → action=%q", action)
	}
}
