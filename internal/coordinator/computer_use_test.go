package coordinator

import (
	"context"
	"io"
	"net/http"
	"testing"
)

// TestComputerUse_TypedResult pins the typed computer-use outcome: action=screenshot
// → Screenshot (base64), other actions → OK (matching the spec's {screenshot}|{ok} union).
func TestComputerUse_TypedResult(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if contains(string(b), `"action":"screenshot"`) {
			_, _ = io.WriteString(w, `{"screenshot":"iVBORw0KGgo="}`)
		} else {
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	})

	shot, err := c.ComputerUse(context.Background(), "ps_", "s", "screenshot", nil)
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if shot.Screenshot != "iVBORw0KGgo=" || shot.OK {
		t.Errorf("screenshot result = %+v", shot)
	}

	act, err := c.ComputerUse(context.Background(), "ps_", "s", "click", map[string]any{"x": 1, "y": 2})
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	if !act.OK || act.Screenshot != "" {
		t.Errorf("click result = %+v", act)
	}
}
