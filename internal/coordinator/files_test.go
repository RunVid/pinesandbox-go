package coordinator

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.pinesandbox.io/computer/internal/base/problem"
)

// TestOp_QueryStripped: an error on a query-bearing route must NOT leak the query string
// (a filename, a cursor, a selector) into APIError.Op — the op is "<METHOD> <path>" only.
func TestOp_QueryStripped(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"type":"/errors/x","status":500,"detail":"boom"}`)
	})
	ctx := context.Background()
	calls := []struct {
		name string
		run  func() error
		op   string
	}{
		{"ReadFile", func() error { _, e := c.ReadFile(ctx, "ps_", "s", "secret.txt"); return e }, "GET /v1/sessions/s/files"},
		{"ListFiles", func() error {
			_, e := c.ListFiles(ctx, "ps_", "s", ListFilesOptions{Path: "sub", Pattern: "*.txt"})
			return e
		}, "GET /v1/sessions/s/files/list"},
		{"UploadArtifact", func() error { _, e := c.UploadArtifact(ctx, "ps_", "s", "out.txt", []byte("x")); return e }, "POST /v1/sessions/s/artifacts"},
		{"PatchControl(force)", func() error {
			_, e := c.PatchControl(ctx, "ct_", "s", ControlPatch{}, PatchControlOptions{Force: true})
			return e
		}, "PATCH /v1/sessions/s/control"},
		{"DestroySession(clean)", func() error { return c.DestroySession(ctx, "ct_", "s", true) }, "DELETE /sessions/s"},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			var ae *problem.APIError
			if err := tc.run(); !errors.As(err, &ae) {
				t.Fatalf("err = %T (%v), want *problem.APIError", err, err)
			}
			if strings.ContainsAny(ae.Op, "?#") {
				t.Errorf("Op = %q must not contain a query string", ae.Op)
			}
			if ae.Op != tc.op {
				t.Errorf("Op = %q, want %q", ae.Op, tc.op)
			}
		})
	}
}

func TestListFiles(t *testing.T) {
	var gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/s/files/list" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"entries":[{"relative_path":"a.txt","name":"a.txt","is_dir":false,"size":3,"modified_at":"2026-01-01T00:00:00Z"},{"relative_path":"d","name":"d","is_dir":true}]}`)
	})
	entries, err := c.ListFiles(context.Background(), "ps_", "s", ListFilesOptions{Path: "sub", Pattern: "*.txt"})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if gotQuery == "" || !(contains(gotQuery, "path=sub") && contains(gotQuery, "pattern=")) {
		t.Errorf("query = %q, want path+pattern", gotQuery)
	}
	if len(entries) != 2 || entries[0].Name != "a.txt" || entries[0].Size != 3 || !entries[1].IsDir {
		t.Errorf("entries = %+v", entries)
	}
	if entries[0].ModifiedAt == nil {
		t.Error("modified_at not parsed")
	}
}

func TestReadAndWriteFile(t *testing.T) {
	var putBody []byte
	var putCT string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			if r.Header.Get("Accept") != "application/octet-stream" {
				t.Errorf("read Accept = %q", r.Header.Get("Accept"))
			}
			if r.URL.Query().Get("path") != "a.txt" {
				t.Errorf("read path = %q", r.URL.Query().Get("path"))
			}
			_, _ = w.Write([]byte("file-bytes"))
		case "PUT":
			putCT = r.Header.Get("Content-Type")
			putBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"root":"attachments","relative_path":"x","size":5}`)
		}
	})
	b, err := c.ReadFile(context.Background(), "ps_", "s", "a.txt")
	if err != nil || string(b) != "file-bytes" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	if _, err := c.WriteFile(context.Background(), "ps_", "s", "x", []byte("hello")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if string(putBody) != "hello" || putCT != "application/octet-stream" {
		t.Errorf("PUT body=%q ct=%q", putBody, putCT)
	}
}

func TestArtifacts(t *testing.T) {
	var listQuery, uploadQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/sessions/s/artifacts" && r.Method == "GET":
			listQuery = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"artifacts":[{"id":"a1","created_by":"agent","size":10,"created_at":"2026-01-01T00:00:00Z"}]}`)
		case r.URL.Path == "/v1/sessions/s/artifacts/a1" && r.Method == "GET":
			if r.Header.Get("Accept") != "application/octet-stream" {
				t.Errorf("read Accept = %q", r.Header.Get("Accept"))
			}
			_, _ = w.Write([]byte("artifact-bytes"))
		case r.URL.Path == "/v1/sessions/s/artifacts/zip":
			if r.Header.Get("Accept") != "application/zip" {
				t.Errorf("zip Accept = %q", r.Header.Get("Accept"))
			}
			_, _ = w.Write([]byte("PK\x03\x04zip"))
		case r.URL.Path == "/v1/sessions/s/artifacts" && r.Method == "POST":
			uploadQuery = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"id":"up1","created_by":"upload","size":4}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	arts, err := c.ListArtifacts(context.Background(), "ps_", "s", "turn-9")
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(arts) != 1 || arts[0].ID != "a1" || arts[0].CreatedBy != "agent" || arts[0].CreatedAt == nil {
		t.Errorf("artifacts = %+v", arts)
	}
	if !contains(listQuery, "turn_id=turn-9") {
		t.Errorf("list query = %q, want turn_id", listQuery)
	}

	b, err := c.ReadArtifact(context.Background(), "ps_", "s", "a1")
	if err != nil || string(b) != "artifact-bytes" {
		t.Fatalf("ReadArtifact = %q, %v", b, err)
	}
	z, err := c.ZipArtifacts(context.Background(), "ps_", "s", "")
	if err != nil || string(z) != "PK\x03\x04zip" {
		t.Fatalf("ZipArtifacts = %q, %v", z, err)
	}
	up, err := c.UploadArtifact(context.Background(), "ps_", "s", "out.txt", []byte("data"))
	if err != nil || up.ID != "up1" || up.CreatedBy != "upload" {
		t.Fatalf("UploadArtifact = %+v, %v", up, err)
	}
	if !contains(uploadQuery, "filename=out.txt") {
		t.Errorf("upload query = %q, want filename", uploadQuery)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestArtifactFilename_ParsedAndDerived: Artifact.Filename is the human name
// (with extension) — taken from the wire `filename` when present, else derived
// from the basename of the id-prefixed relative_path (older coordinator).
func TestArtifactFilename_ParsedAndDerived(t *testing.T) {
	got := artifactWire{ID: "art_x", RelativePath: "art_x/filled_w9.pdf", Filename: "filled_w9.pdf"}.toArtifact()
	if got.Filename != "filled_w9.pdf" {
		t.Errorf("Filename = %q, want filled_w9.pdf (from wire)", got.Filename)
	}
	derived := artifactWire{ID: "art_y", RelativePath: "art_y/report.csv"}.toArtifact()
	if derived.Filename != "report.csv" {
		t.Errorf("derived Filename = %q, want report.csv (basename of relative_path)", derived.Filename)
	}
}

// TestOpenArtifact_StreamsBytesAndMapsErrors: the streaming artifact read returns the live
// body (octet-stream Accept; the caller closes it) and maps a non-2xx exactly like the
// buffered read — a typed *problem.APIError with the query-less Op — closing the body on
// that path so a reject can't leak the connection.
func TestOpenArtifact_StreamsBytesAndMapsErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("method = %s", r.Method)
		}
		switch r.URL.Path {
		case "/v1/sessions/s/artifacts/a1":
			if r.Header.Get("Accept") != "application/octet-stream" {
				t.Errorf("Accept = %q", r.Header.Get("Accept"))
			}
			_, _ = w.Write([]byte("streamed-artifact-bytes"))
		case "/v1/sessions/s/artifacts/missing":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"type":"/errors/artifact-not-found","status":404,"detail":"no such artifact"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	rc, err := c.OpenArtifact(context.Background(), "ps_", "s", "a1")
	if err != nil {
		t.Fatalf("OpenArtifact: %v", err)
	}
	b, rerr := io.ReadAll(rc)
	if cerr := rc.Close(); cerr != nil {
		t.Errorf("Close: %v", cerr)
	}
	if rerr != nil || string(b) != "streamed-artifact-bytes" {
		t.Fatalf("stream = %q, %v", b, rerr)
	}

	_, err = c.OpenArtifact(context.Background(), "ps_", "s", "missing")
	var ae *problem.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %T (%v), want *problem.APIError", err, err)
	}
	if ae.Status != 404 {
		t.Errorf("Status = %d, want 404", ae.Status)
	}
	if ae.Op != "GET /v1/sessions/s/artifacts/missing" {
		t.Errorf("Op = %q", ae.Op)
	}
}

// TestOpenArtifact_RetriesTransientOpenFault: the streaming read's OPEN rides the same
// transient-retry budget as the buffered ReadArtifact (Codex review, PR #170) — a
// gateway/TCP blip before response headers must not surface to the caller. The first
// request is killed pre-headers (hijack + close → EOF); the retry must land the bytes.
func TestOpenArtifact_RetriesTransientOpenFault(t *testing.T) {
	var n int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			_ = conn.Close() // pre-headers connection fault
			return
		}
		_, _ = w.Write([]byte("retried-bytes"))
	})
	rc, err := c.OpenArtifact(context.Background(), "ps_", "s", "a1")
	if err != nil {
		t.Fatalf("OpenArtifact should absorb a pre-headers fault: %v", err)
	}
	b, rerr := io.ReadAll(rc)
	_ = rc.Close()
	if rerr != nil || string(b) != "retried-bytes" {
		t.Fatalf("stream = %q, %v", b, rerr)
	}
	if n != 2 {
		t.Errorf("requests = %d, want 2 (1 fault + 1 retry)", n)
	}
}
