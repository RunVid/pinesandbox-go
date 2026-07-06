package problem

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRetryableFallback_MatchesTaxonomy asserts the embedded table drives the fallback
// for every slug the contract pins — drift = the SDK and the server disagree on what's
// retryable when an old server omits the wire field.
func TestRetryableFallback_MatchesTaxonomy(t *testing.T) {
	var f struct {
		Entries []struct {
			Type      string `json:"type"`
			Status    int    `json:"status"`
			Retryable bool   `json:"retryable"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(taxonomyJSON, &f); err != nil {
		t.Fatalf("decode embedded taxonomy: %v", err)
	}
	if len(f.Entries) == 0 {
		t.Fatal("empty taxonomy")
	}
	for _, e := range f.Entries {
		if got := RetryableFallback(e.Type, e.Status); got != e.Retryable {
			t.Errorf("RetryableFallback(%q, %d) = %v, want %v", e.Type, e.Status, got, e.Retryable)
		}
	}
}

// TestRetryableFallback_Heuristic pins the unknown-slug heuristic (412 true / 501 false
// / ≥500 true / else false) — the only judgment for a server slug the SDK predates.
func TestRetryableFallback_Heuristic(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{412, true},  // precondition-failed → retry with a fresh ETag
		{501, false}, // not-implemented → never
		{500, true},  // server error → transient
		{503, true},
		{400, false}, // client error → terminal
		{404, false},
		{409, false},
	}
	for _, c := range cases {
		if got := RetryableFallback("/errors/unknown-future-slug", c.status); got != c.want {
			t.Errorf("heuristic for status %d = %v, want %v", c.status, got, c.want)
		}
	}
}

// TestParse_WireJudgmentWins: the 0C wire `retryable` overrides the taxonomy — a server
// can declare a normally-terminal slug retryable (and vice-versa) and the SDK obeys it.
func TestParse_WireJudgmentWins(t *testing.T) {
	// session-busy is retryable=true in the taxonomy; the wire says false here.
	body := []byte(`{"type":"/errors/session-busy","status":409,"detail":"busy","retryable":false}`)
	e := Parse(409, body, "")
	if e.Retryable {
		t.Errorf("wire retryable:false must win over the taxonomy, got Retryable=true")
	}
	if e.ProblemType != "/errors/session-busy" || e.Status != 409 {
		t.Errorf("decoded %+v", e)
	}

	// Absent wire field → fall back to the taxonomy (session-busy → true).
	e2 := Parse(409, []byte(`{"type":"/errors/session-busy","status":409}`), "")
	if !e2.Retryable {
		t.Errorf("absent wire field must fall back to taxonomy (session-busy=true), got false")
	}
}

// TestParse_RequestID: the body request_id wins; the X-Request-Id header is the fallback
// (every response carries it — 0C).
func TestParse_RequestID(t *testing.T) {
	if e := Parse(404, []byte(`{"type":"/errors/x","request_id":"body-rid"}`), "hdr-rid"); e.RequestID != "body-rid" {
		t.Errorf("RequestID = %q, want body-rid", e.RequestID)
	}
	if e := Parse(404, []byte(`{"type":"/errors/x"}`), "hdr-rid"); e.RequestID != "hdr-rid" {
		t.Errorf("RequestID = %q, want hdr-rid (header fallback)", e.RequestID)
	}
	// Non-JSON body still yields a usable error with the header request id.
	if e := Parse(502, []byte(`<html>bad gateway</html>`), "hdr-rid"); e.Status != 502 || e.RequestID != "hdr-rid" {
		t.Errorf("non-JSON body: %+v", e)
	}
}

// TestError_IncludesRequestID: the request id must land IN the Error() string (integrators
// overwhelmingly log only err.Error()) — with a slug and without, and be omitted when absent.
func TestError_IncludesRequestID(t *testing.T) {
	withSlug := (&APIError{Status: 409, ProblemType: "/errors/session-busy", Detail: "busy", RequestID: "rid-7"}).Error()
	if !strings.Contains(withSlug, "request_id=rid-7") {
		t.Errorf("Error() = %q, want it to contain request_id=rid-7", withSlug)
	}
	noSlug := (&APIError{Status: 500, Detail: "boom", RequestID: "rid-8"}).Error()
	if !strings.Contains(noSlug, "request_id=rid-8") {
		t.Errorf("Error() = %q, want it to contain request_id=rid-8", noSlug)
	}
	absent := (&APIError{Status: 500, Detail: "boom"}).Error()
	if strings.Contains(absent, "request_id=") {
		t.Errorf("Error() = %q, want no request_id fragment when none is set", absent)
	}
}

// TestError_ResourceFirstContext: the message is self-describing at the RESOURCE level —
// host (WHICH Computer) then op (WHICH operation) precede the request_id precision handle,
// in that order, and each part appears only when set.
func TestError_ResourceFirstContext(t *testing.T) {
	full := (&APIError{
		Status: 409, ProblemType: "/errors/session-busy", Detail: "busy",
		Host: "abc123.computer.test.example", Op: "POST /v1/sessions/main/agent/run", RequestID: "req-xyz",
	}).Error()
	want := "pinesandbox: 409 /errors/session-busy: busy (host=abc123.computer.test.example, op=POST /v1/sessions/main/agent/run, request_id=req-xyz)"
	if full != want {
		t.Errorf("Error() = %q, want %q", full, want)
	}
	// host + op present without a request_id (a transport fault has no id) still self-describes.
	noRID := (&APIError{Status: 503, Detail: "unavailable", Host: "abc123.computer.test.example", Op: "GET /health"}).Error()
	if !strings.Contains(noRID, "host=abc123.computer.test.example") || !strings.Contains(noRID, "op=GET /health") {
		t.Errorf("Error() = %q, want host+op even with no request_id", noRID)
	}
	if strings.Contains(noRID, "request_id=") {
		t.Errorf("Error() = %q, want no request_id fragment when none is set", noRID)
	}
	// host must precede op must precede request_id.
	if h, o := strings.Index(full, "host="), strings.Index(full, "op="); h < 0 || o < 0 || h > o {
		t.Errorf("host= must precede op= in %q", full)
	}
	if o, r := strings.Index(full, "op="), strings.Index(full, "request_id="); o < 0 || r < 0 || o > r {
		t.Errorf("op= must precede request_id= in %q", full)
	}
}

// TestContextSuffix_OmitsAbsentParts: the shared renderer includes each part only when set,
// and returns "" when all three are absent (a bare error stays clean).
func TestContextSuffix_OmitsAbsentParts(t *testing.T) {
	if got := ContextSuffix("", "", ""); got != "" {
		t.Errorf("ContextSuffix(all empty) = %q, want empty", got)
	}
	if got := ContextSuffix("h", "", "r"); got != " (host=h, request_id=r)" {
		t.Errorf("ContextSuffix skipping op = %q", got)
	}
}

// TestParse_HTTPStatusAuthoritative: the real transport status wins; a body whose `status`
// disagrees (proxy rewrite / forwarded inner error) must NOT move e.Status, which callers
// branch on (e.g. LatestSnapshot's 404 → nil).
func TestParse_HTTPStatusAuthoritative(t *testing.T) {
	e := Parse(404, []byte(`{"type":"/errors/x","status":503,"detail":"d"}`), "")
	if e.Status != 404 {
		t.Errorf("Status = %d, want 404 (the real HTTP status, not the body's 503)", e.Status)
	}
}

// TestAPIError_IsAsError: *APIError satisfies error and is errors.As-able.
func TestAPIError_IsAsError(t *testing.T) {
	var err error = Parse(409, []byte(`{"type":"/errors/control-not-held","status":409}`), "")
	var ae *APIError
	if !errors.As(err, &ae) || ae.ProblemType != "/errors/control-not-held" {
		t.Fatalf("errors.As failed: %v", err)
	}
}

// TestTaxonomyMatchesCanonical guards the embedded copy against the canonical artifact
// (skips on the mirror where the canonical is absent — §9.1).
func TestTaxonomyMatchesCanonical(t *testing.T) {
	canonical := filepath.Join("..", "..", "..", "..", "contract", "error-taxonomy.json")
	want, err := os.ReadFile(canonical)
	if err != nil {
		t.Skipf("canonical artifact not present (mirror build): %v", err)
	}
	got, err := os.ReadFile("error-taxonomy.json")
	if err != nil {
		t.Fatalf("read embedded copy: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("embedded error-taxonomy.json drifted from %s — re-copy the canonical", canonical)
	}
}
