package pinesandbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The cold provision (POST /sandboxes) is bounded by AttachOptions.Timeout — the readiness
// budget the caller already supplies — NOT by the transport's 30s fallback. With a short
// budget and a slow provision, Attach fails with a *TimeoutError from the provision itself
// (rather than waiting out the 30s fallback and proceeding to bind). The real-world win is
// the mirror: a budget LONGER than 30s now gives a cold provision that long instead of dying.
func TestAttach_ProvisionBoundedByReadinessBudget(t *testing.T) {
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sandboxes" && r.Method == http.MethodPost {
			time.Sleep(500 * time.Millisecond) // slower than the 50ms budget below
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer control.Close()

	conn := buildTestConnection(t, control.URL, control.URL)
	comp := newComputer("0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000", make([]byte, 32))

	err := comp.Attach(context.Background(), conn, AttachOptions{
		Timeout: 50 * time.Millisecond,
		Image:   "img",
	})

	// The provision times out within the budget — not a bind error after a 30s fallback.
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("Attach err = %T (%v), want the provision to time out (*TimeoutError) within the readiness budget", err, err)
	}
}
