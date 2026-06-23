package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.pinesandbox.io/computer/internal/base/problem"
)

func clientFor(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	u := strings.TrimPrefix(srv.URL, "http://")
	return New("http", u)
}

// TestDo_AuthHeaderAndBody: the bearer rides X-Pine-Auth (never the URL); 2xx body returns.
func TestDo_AuthHeaderAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Pine-Auth"); got != "ct_abc" {
			t.Errorf("X-Pine-Auth = %q, want ct_abc", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	resp, err := clientFor(t, srv).Do(context.Background(), "GET", "/health", Request{Token: "ct_abc"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if string(resp.Body) != `{"ok":true}` || resp.Status != 200 {
		t.Errorf("resp = %+v", resp)
	}
}

// TestDo_OmitsAuthWhenEmpty: token "" sends no X-Pine-Auth (bind-pubkey/health are token-less).
func TestDo_OmitsAuthWhenEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["X-Pine-Auth"]; ok {
			t.Errorf("X-Pine-Auth must be absent for a token-less call")
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	if _, err := clientFor(t, srv).Do(context.Background(), "GET", "/v1/coord/bind-pubkey", Request{}); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

// TestDo_Non2xxToTypedAPIError: a problem+json non-2xx → *problem.APIError with the
// type/status/request-id, retryable from the wire.
func TestDo_Non2xxToTypedAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"type":"/errors/session-busy","status":409,"detail":"busy","request_id":"rid-1","retryable":true}`))
	}))
	defer srv.Close()

	_, err := clientFor(t, srv).Do(context.Background(), "POST", "/v1/sessions/x/agent/run", Request{Token: "ct_x"})
	var ae *problem.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("want *problem.APIError, got %T: %v", err, err)
	}
	if ae.Status != 409 || ae.ProblemType != "/errors/session-busy" || ae.RequestID != "rid-1" || !ae.Retryable {
		t.Errorf("decoded %+v", ae)
	}
}

// TestDoRaw_ReturnsNon2xxAsResponse: DoRaw never converts a non-2xx to an error — it hands
// back the raw Response so a caller with a non-RFC-9457 error contract (the control plane's
// {code,message}) can parse the body itself. Only transport faults error.
func TestDoRaw_ReturnsNon2xxAsResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"code":"not_found","message":"sandbox gone"}`))
	}))
	defer srv.Close()

	resp, err := clientFor(t, srv).DoRaw(context.Background(), "GET", "/sandboxes/x", Request{Headers: map[string]string{"Authorization": "Bearer jws"}})
	if err != nil {
		t.Fatalf("DoRaw returned an error for a 404: %v", err)
	}
	if resp.Status != 404 {
		t.Errorf("Status = %d, want 404", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "sandbox gone") {
		t.Errorf("body = %q, want it preserved", resp.Body)
	}
}

// TestDoRaw_TransportFaultStillErrors: a real transport fault is still a typed error.
func TestDoRaw_TransportFaultStillErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	u := strings.TrimPrefix(srv.URL, "http://")
	srv.Close() // nothing is listening now
	_, err := New("http", u).DoRaw(context.Background(), "GET", "/x", Request{})
	var ce *ConnectionError
	if !errors.As(err, &ce) {
		t.Fatalf("want *ConnectionError, got %T: %v", err, err)
	}
}

// TestStream_LiveBodyAndHeaders: Stream returns the live, unbuffered body + status +
// headers for any status; the caller reads + closes it. Sends X-Pine-Auth + extra headers.
func TestStream_LiveBodyAndHeaders(t *testing.T) {
	var gotAuth, gotLEI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Pine-Auth")
		gotLEI = r.Header.Get("Last-Event-ID")
		w.Header().Set("X-Computer-Spec-Version", "1")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: hello\n\n"))
	}))
	defer srv.Close()

	sr, err := clientFor(t, srv).Stream(context.Background(), "GET", "/v1/sessions/s/agent/events",
		Request{Token: "ps_x", Accept: "text/event-stream", Headers: map[string]string{"Last-Event-ID": "7"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Body.Close()
	if sr.Status != 200 || sr.Headers.Get("X-Computer-Spec-Version") != "1" {
		t.Errorf("status/headers = %d %v", sr.Status, sr.Headers.Get("X-Computer-Spec-Version"))
	}
	if gotAuth != "ps_x" || gotLEI != "7" {
		t.Errorf("auth=%q lei=%q", gotAuth, gotLEI)
	}
	b, _ := io.ReadAll(sr.Body)
	if string(b) != "data: hello\n\n" {
		t.Errorf("body = %q", b)
	}
}

// TestStream_TransportFault: a dead server is a typed ConnectionError, not a raw net error.
func TestStream_TransportFault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	u := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	_, err := New("http", u).Stream(context.Background(), "GET", "/x", Request{})
	var ce *ConnectionError
	if !errors.As(err, &ce) {
		t.Fatalf("want *ConnectionError, got %T: %v", err, err)
	}
}

// TestDo_GatewayTimeoutToTimeoutError: 408/504 normalize to *TimeoutError (Ruby parity).
func TestDo_GatewayTimeoutToTimeoutError(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusGatewayTimeout} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		_, err := clientFor(t, srv).Do(context.Background(), "GET", "/x", Request{})
		srv.Close()
		var te *TimeoutError
		if !errors.As(err, &te) {
			t.Errorf("status %d → want *TimeoutError, got %T: %v", status, err, err)
		}
	}
}

// TestDo_RequestIDFromHeader: when the body omits request_id, the X-Request-Id header is
// the fallback (every response carries it — 0C).
func TestDo_RequestIDFromHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "hdr-rid")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"type":"/errors/session-not-found","status":404}`))
	}))
	defer srv.Close()
	_, err := clientFor(t, srv).Do(context.Background(), "GET", "/x", Request{})
	var ae *problem.APIError
	if !errors.As(err, &ae) || ae.RequestID != "hdr-rid" {
		t.Fatalf("RequestID from header: %v", err)
	}
}

// TestDo_ConnectionFault: an unreachable backend → *ConnectionError, not a raw net error.
func TestDo_ConnectionFault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	c := clientFor(t, srv)
	srv.Close() // now nothing listens → dial fails
	_, err := c.Do(context.Background(), "GET", "/x", Request{})
	var ce *ConnectionError
	if !errors.As(err, &ce) {
		t.Fatalf("want *ConnectionError, got %T: %v", err, err)
	}
}

// TestDo_Timeout: a slow server + a short context deadline → *TimeoutError.
func TestDo_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hang until the client gives up
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := clientFor(t, srv).Do(ctx, "GET", "/x", Request{})
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want *TimeoutError, got %T: %v", err, err)
	}
}
