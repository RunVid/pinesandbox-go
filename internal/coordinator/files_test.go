package coordinator

import (
	"context"
	"io"
	"net/http"
	"testing"
)

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
