package pinesandbox

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	artifactsComputer = "0190aaaa-bbbb-7ccc-8ddd-eeeeffff7001"
	artifactsOther    = "0190aaaa-bbbb-7ccc-8ddd-eeeeffff7002"
	artifactsPrefix   = artifactsComputer + "/artifacts/"
	otherPrefix       = artifactsOther + "/artifacts/"
)

// artifactStorage is one httptest server that answers Pine's artifact token route and
// the storage JSON API (the token names the server as its endpoint). Tokens are numbered
// ya29.1, ya29.2, … in minting order; storage answers with handle.
type artifactStorage struct {
	t      *testing.T
	srv    *httptest.Server
	clock  *virtualClock
	client *Client

	mu       sync.Mutex
	mints    []string // each token request's body
	storage  []string // "METHOD escaped-path?query" of each storage request
	writable bool
	ttl      time.Duration
	copyFrom map[string]string
	handle   func(w http.ResponseWriter, r *http.Request)
}

func newArtifactStorage(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *artifactStorage {
	t.Helper()
	a := &artifactStorage{t: t, writable: true, ttl: 30 * time.Minute, handle: handle,
		clock: &virtualClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}}
	a.srv = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.srv.Close)
	client, err := newClient(ClientOptions{
		Endpoint: "http://pine.localhost:8080", ControlHost: strings.TrimPrefix(a.srv.URL, "http://"), APIKey: "pk_test",
	}, clientHooks{clock: a.clock})
	if err != nil {
		t.Fatal(err)
	}
	a.client = client
	return a
}

func (a *artifactStorage) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/computers/"+artifactsComputer+"/artifacts/token" {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer pk_test" {
			a.t.Errorf("token request = %s with %q, want a POST with the project key", r.Method, r.Header.Get("Authorization"))
		}
		a.mu.Lock()
		a.mints = append(a.mints, string(body))
		token := map[string]any{
			"access_token": fmt.Sprintf("ya29.%d", len(a.mints)), "expires_at": a.clock.Now().Add(a.ttl).Format(time.RFC3339),
			"endpoint": a.srv.URL, "bucket": "bkt", "prefix": artifactsPrefix,
			"writable": a.writable, "used_bytes": 120, "limit_bytes": 100,
		}
		if a.copyFrom != nil {
			token["copy_from"] = a.copyFrom
		}
		a.mu.Unlock()
		_ = json.NewEncoder(w).Encode(token)
		return
	}
	call := r.Method + " " + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		call += "?" + r.URL.RawQuery
	}
	a.mu.Lock()
	a.storage = append(a.storage, call)
	a.mu.Unlock()
	a.handle(w, r)
}

func (a *artifactStorage) recorded() (mints, storage []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.mints...), append([]string(nil), a.storage...)
}

func (a *artifactStorage) advance(d time.Duration) {
	a.clock.mu.Lock()
	a.clock.now = a.clock.now.Add(d)
	a.clock.mu.Unlock()
}

func emptyListing(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) }

// TestArtifacts_TokenIsCachedUntilFiveMinutesBeforeItExpires: one token serves the
// handle's calls until 5 minutes before its expires_at, then the next call gets another.
func TestArtifacts_TokenIsCachedUntilFiveMinutesBeforeItExpires(t *testing.T) {
	var auth []string
	a := newArtifactStorage(t, func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		emptyListing(w, r)
	})
	artifacts := a.client.Artifacts(artifactsComputer)
	for _, step := range []time.Duration{0, 10 * time.Minute, 14*time.Minute + 59*time.Second, time.Second} {
		a.advance(step)
		if _, err := artifacts.List(context.Background(), ArtifactListOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"Bearer ya29.1", "Bearer ya29.1", "Bearer ya29.1", "Bearer ya29.2"}
	if mints, _ := a.recorded(); len(mints) != 2 || mints[0] != "" || !reflect.DeepEqual(auth, want) {
		t.Errorf("token requests %q, storage auth %q; want a body-less request, and the second token only at 25 minutes", mints, auth)
	}
}

// TestArtifacts_StorageRefusingTheTokenRefreshesItOnce: a storage 401 gets a fresh token
// and the call is sent once more; a second 401 is the caller's APIError.
func TestArtifacts_StorageRefusingTheTokenRefreshesItOnce(t *testing.T) {
	refuse := map[string]bool{"Bearer ya29.1": true}
	a := newArtifactStorage(t, func(w http.ResponseWriter, r *http.Request) {
		if refuse[r.Header.Get("Authorization")] {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"message":"Invalid Credentials"}}`)
			return
		}
		emptyListing(w, r)
	})
	ctx := context.Background()
	if _, err := a.client.Artifacts(artifactsComputer).List(ctx, ArtifactListOptions{}); err != nil {
		t.Fatalf("List after one refused token: %v", err)
	}
	if mints, storage := a.recorded(); len(mints) != 2 || len(storage) != 2 {
		t.Fatalf("mints = %d, storage calls = %d, want 2 and 2", len(mints), len(storage))
	}
	refuse["Bearer ya29.3"], refuse["Bearer ya29.4"] = true, true
	_, err := a.client.Artifacts(artifactsComputer).List(ctx, ArtifactListOptions{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 || apiErr.Detail != "Invalid Credentials" {
		t.Fatalf("err = %v, want the storage 401 with its message", err)
	}
	if mints, storage := a.recorded(); len(mints) != 4 || len(storage) != 4 {
		t.Errorf("mints = %d, storage calls = %d, want one refresh only", len(mints), len(storage))
	}
}

// TestArtifacts_ListSendsThePageOptions: the prefix sits under the token's, a recursive
// listing has no delimiter, and the page options become pageToken / maxResults.
func TestArtifacts_ListSendsThePageOptions(t *testing.T) {
	a := newArtifactStorage(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"items":[{"name":%q,"size":"3","updated":"2026-10-22T12:00:00.250Z","contentType":"text/plain"}]}`, artifactsPrefix+"a/b/c.txt")
	})
	list, err := a.client.Artifacts(artifactsComputer).List(context.Background(), ArtifactListOptions{
		Prefix: "a/", Recursive: true, PageToken: "p2", PageSize: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := ArtifactFile{Path: "a/b/c.txt", Size: 3, UpdatedAt: time.Date(2026, 10, 22, 12, 0, 0, 250e6, time.UTC), ContentType: "text/plain"}
	if len(list.Files) != 1 || !reflect.DeepEqual(list.Files[0], want) || list.Folders == nil || list.NextPageToken != "" {
		t.Errorf("list = %+v", list)
	}
	_, storage := a.recorded()
	if want := "GET /storage/v1/b/bkt/o?maxResults=50&pageToken=p2&prefix=" + strings.ReplaceAll(artifactsPrefix, "/", "%2F") + "a%2F"; storage[0] != want {
		t.Errorf("list request = %s, want %s", storage[0], want)
	}
}

// TestArtifacts_StorageFullWritesNothing: a token that isn't writable refuses Upload and
// Copy with ArtifactStorageFullError before any storage call.
func TestArtifacts_StorageFullWritesNothing(t *testing.T) {
	a := newArtifactStorage(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("storage called: %s %s", r.Method, r.URL.Path)
	})
	a.writable = false
	a.copyFrom = map[string]string{artifactsOther: otherPrefix}
	artifacts := a.client.Artifacts(artifactsComputer)
	ctx := context.Background()
	_, uploadErr := artifacts.Upload(ctx, "x.txt", ArtifactFromBytes([]byte("x")), ArtifactUploadOptions{})
	_, copyErr := artifacts.Copy(ctx, artifactsOther, "a.txt", "b.txt")
	for name, err := range map[string]error{"Upload": uploadErr, "Copy": copyErr} {
		var full *ArtifactStorageFullError
		if !errors.As(err, &full) || full.UsedBytes != 120 || full.LimitBytes != 100 {
			t.Errorf("%s err = %v, want ArtifactStorageFullError{120, 100}", name, err)
		}
	}
}

// TestArtifacts_UploadAfterMakingRoomOnTheSameHandle: a kept token that isn't writable is
// replaced before a write is refused, so once a delete has made room, the same handle
// saves again. While the Computer is still full, each refused upload asks for one fresh
// token and sends nothing to storage.
func TestArtifacts_UploadAfterMakingRoomOnTheSameHandle(t *testing.T) {
	a := newArtifactStorage(t, nil)
	a.handle = func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			a.mu.Lock()
			a.writable = true // the delete brought the Computer under its limit
			a.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPost:
			w.Header().Set("Location", a.srv.URL+"/upload/storage/v1/b/bkt/o?upload_id=u1")
		case http.MethodPut:
			fmt.Fprintf(w, `{"name":%q,"size":"1"}`, artifactsPrefix+"new.txt")
		}
	}
	a.writable = false
	artifacts := a.client.Artifacts(artifactsComputer)
	ctx := context.Background()
	for range 2 {
		var full *ArtifactStorageFullError
		if _, err := artifacts.Upload(ctx, "new.txt", ArtifactFromBytes([]byte("x")), ArtifactUploadOptions{}); !errors.As(err, &full) {
			t.Fatalf("Upload while full = %v, want ArtifactStorageFullError", err)
		}
	}
	if mints, storage := a.recorded(); len(mints) != 2 || len(storage) != 0 {
		t.Fatalf("while full: %d token requests, storage %q; want one fresh token per refused upload and no storage call", len(mints), storage)
	}
	if err := artifacts.Delete(ctx, "old.txt"); err != nil {
		t.Fatal(err)
	}
	file, err := artifacts.Upload(ctx, "new.txt", ArtifactFromBytes([]byte("x")), ArtifactUploadOptions{})
	if err != nil || file.Path != "new.txt" {
		t.Fatalf("Upload after the delete = %+v, %v; want the saved file", file, err)
	}
	mints, storage := a.recorded()
	if len(mints) != 3 || len(storage) != 3 || !strings.HasPrefix(storage[0], "DELETE ") {
		t.Errorf("%d token requests, storage %q; want the delete on the kept token, then one fresh writable token for the upload", len(mints), storage)
	}
}

// TestArtifacts_CopyFollowsTheRewriteUntilDone: Copy gets a token that reads the source
// Computer, rewrites its object inside storage, and repeats with the rewriteToken until
// storage says done.
func TestArtifacts_CopyFollowsTheRewriteUntilDone(t *testing.T) {
	var bodies []string
	a := newArtifactStorage(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if r.URL.Query().Get("rewriteToken") == "" {
			fmt.Fprint(w, `{"done":false,"rewriteToken":"rw1","totalBytesRewritten":"1"}`)
			return
		}
		fmt.Fprintf(w, `{"done":true,"resource":{"name":%q,"size":"2","contentType":"text/csv","metadata":{"pine-note":"copied"}}}`, artifactsPrefix+"in/q3.csv")
	})
	a.copyFrom = map[string]string{artifactsOther: otherPrefix}
	file, err := a.client.Artifacts(artifactsComputer).Copy(context.Background(), artifactsOther, "out/q3.csv", "in/q3.csv")
	if err != nil {
		t.Fatal(err)
	}
	if file.Path != "in/q3.csv" || file.Size != 2 || file.Note != "copied" {
		t.Errorf("copy = %+v", file)
	}
	mints, storage := a.recorded()
	if len(mints) != 1 || mints[0] != `{"copy_from":["`+artifactsOther+`"]}` {
		t.Errorf("token requests = %q, want one with copy_from", mints)
	}
	rewrite := "POST /storage/v1/b/bkt/o/" + artifactsOther + "%2Fartifacts%2Fout%2Fq3.csv/rewriteTo/b/bkt/o/" + artifactsComputer + "%2Fartifacts%2Fin%2Fq3.csv"
	if want := []string{rewrite, rewrite + "?rewriteToken=rw1"}; !reflect.DeepEqual(storage, want) || !reflect.DeepEqual(bodies, []string{"{}", "{}"}) {
		t.Errorf("storage = %q bodies %q, want %q with empty JSON bodies", storage, bodies, want)
	}
}

// TestArtifacts_CopyWithinTheComputerUsesItsOwnToken: copying from the Computer itself
// needs no copy source; its own prefix is the source's.
func TestArtifacts_CopyWithinTheComputerUsesItsOwnToken(t *testing.T) {
	a := newArtifactStorage(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"done":true,"resource":{"name":%q,"size":"1"}}`, artifactsPrefix+"b.txt")
	})
	if _, err := a.client.Artifacts(artifactsComputer).Copy(context.Background(), artifactsComputer, "a.txt", "b.txt"); err != nil {
		t.Fatal(err)
	}
	mints, storage := a.recorded()
	if len(mints) != 1 || mints[0] != "" || !strings.HasPrefix(storage[0], "POST /storage/v1/b/bkt/o/"+artifactsComputer+"%2Fartifacts%2Fa.txt/rewriteTo/") {
		t.Errorf("mints %q storage %q", mints, storage)
	}
}

// TestArtifacts_DeleteAndAMissingFile: Delete sends the encoded object name; a storage 404
// is ArtifactNotFoundError naming the caller's path, wrapping storage's APIError.
func TestArtifacts_DeleteAndAMissingFile(t *testing.T) {
	gone := false
	a := newArtifactStorage(t, func(w http.ResponseWriter, r *http.Request) {
		if gone {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"No such object"}}`)
			return
		}
		gone = true
		w.WriteHeader(http.StatusNoContent)
	})
	artifacts := a.client.Artifacts(artifactsComputer)
	if err := artifacts.Delete(context.Background(), "old/report v1.pdf"); err != nil {
		t.Fatal(err)
	}
	err := artifacts.Delete(context.Background(), "old/report v1.pdf")
	var missing *ArtifactNotFoundError
	var apiErr *APIError
	if !errors.As(err, &missing) || missing.Path != "old/report v1.pdf" || !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("err = %v, want ArtifactNotFoundError for the path, wrapping the 404", err)
	}
	if apiErr.Op != "DELETE /storage/v1/b/bkt/o/{object}" || strings.Contains(apiErr.Error(), "report") {
		t.Errorf("storage error %q (op %q) names the object; its op is the route with {object}", apiErr.Error(), apiErr.Op)
	}
	_, storage := a.recorded()
	if want := "DELETE /storage/v1/b/bkt/o/" + artifactsComputer + "%2Fartifacts%2Fold%2Freport%20v1.pdf"; storage[0] != want {
		t.Errorf("delete = %s, want %s", storage[0], want)
	}
}

// TestArtifacts_DownloadResumesADroppedConnection: a connection that drops mid-file is
// reopened after the resume backoff for the remaining bytes of the same generation, and
// the file lands whole at the destination with no temporary file left beside it. Storage
// may answer the Range with the rest (206) or with the whole file again (200).
func TestArtifacts_DownloadResumesADroppedConnection(t *testing.T) {
	const content = "hello world"
	for _, resumeStatus := range []int{http.StatusPartialContent, http.StatusOK} {
		t.Run(http.StatusText(resumeStatus), func(t *testing.T) {
			var ranges, generations []string
			a := newArtifactStorage(t, func(w http.ResponseWriter, r *http.Request) {
				ranges = append(ranges, r.Header.Get("Range"))
				generations = append(generations, r.URL.Query().Get("ifGenerationMatch"))
				w.Header().Set("X-Goog-Generation", "1700")
				if r.Header.Get("Range") == "" {
					w.Header().Set("Content-Length", fmt.Sprint(len(content)))
					fmt.Fprint(w, content[:5])
					w.(http.Flusher).Flush()
					panic(http.ErrAbortHandler) // the connection drops after 5 bytes
				}
				if resumeStatus == http.StatusOK {
					fmt.Fprint(w, content)
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes 5-%d/%d", len(content)-1, len(content)))
				w.WriteHeader(http.StatusPartialContent)
				fmt.Fprint(w, content[5:])
			})
			dir := t.TempDir()
			destination := filepath.Join(dir, "hello.txt")
			n, err := a.client.Artifacts(artifactsComputer).Download(context.Background(), "greetings/hello.txt", destination)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(destination)
			entries, _ := os.ReadDir(dir)
			if n != int64(len(content)) || string(got) != content || len(entries) != 1 {
				t.Errorf("Download = %d, file %q, %d entries; want %d bytes of %q and only the file", n, got, len(entries), len(content), content)
			}
			if !reflect.DeepEqual(ranges, []string{"", "bytes=5-"}) || !reflect.DeepEqual(generations, []string{"", "1700"}) {
				t.Errorf("ranges %q generations %q, want the resume from byte 5 of generation 1700", ranges, generations)
			}
			if sleeps := a.clock.recorded(); !reflect.DeepEqual(sleeps, []int64{500}) {
				t.Errorf("sleeps = %v ms, want the 500 ms resume backoff", sleeps)
			}
		})
	}
}

// TestArtifacts_DownloadOfAMissingFileWritesNothing: a 404 is ArtifactNotFoundError and
// leaves no file behind; only its Path names the file, not storage's operation.
func TestArtifacts_DownloadOfAMissingFileWritesNothing(t *testing.T) {
	a := newArtifactStorage(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":404,"message":"No such object"}}`)
	})
	dir := t.TempDir()
	_, err := a.client.Artifacts(artifactsComputer).Download(context.Background(), "gone.txt", filepath.Join(dir, "gone.txt"))
	var missing *ArtifactNotFoundError
	entries, _ := os.ReadDir(dir)
	if !errors.As(err, &missing) || missing.Path != "gone.txt" || len(entries) != 0 {
		t.Fatalf("err = %v with %d files, want ArtifactNotFoundError and none", err, len(entries))
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Op != "GET /storage/v1/b/bkt/o/{object}" || strings.Contains(apiErr.Error(), "gone") {
		t.Errorf("storage error = %v, want the 404 with op GET /storage/v1/b/bkt/o/{object}", apiErr)
	}
}

// TestArtifacts_UploadFromAFileTakesTheTypeFromItsExtension: a local file is sent with
// the type the shared table gives its path's extension, or application/octet-stream,
// and no note metadata without a note.
func TestArtifacts_UploadFromAFileTakesTheTypeFromItsExtension(t *testing.T) {
	local := filepath.Join(t.TempDir(), "in.bin")
	if err := os.WriteFile(local, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var types, lengths, ranges []string
	var starts []map[string]any
	var sent []string
	a := newArtifactStorage(t, nil)
	a.handle = func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var start map[string]any
			_ = json.NewDecoder(r.Body).Decode(&start)
			starts = append(starts, start)
			types = append(types, r.Header.Get("X-Upload-Content-Type"))
			lengths = append(lengths, r.Header.Get("X-Upload-Content-Length"))
			w.Header().Set("Location", a.srv.URL+"/upload/storage/v1/b/bkt/o?uploadType=resumable&upload_id=u1")
		case http.MethodPut:
			if r.Header.Get("Authorization") != "" {
				t.Error("the upload session was sent the storage token")
			}
			body, _ := io.ReadAll(r.Body)
			sent, ranges = append(sent, string(body)), append(ranges, r.Header.Get("Content-Range"))
			fmt.Fprintf(w, `{"name":%q,"size":"7"}`, artifactsPrefix+"data/in.json")
		}
	}
	artifacts := a.client.Artifacts(artifactsComputer)
	ctx := context.Background()
	file, err := artifacts.Upload(ctx, "data/in.json", ArtifactFromFile(local), ArtifactUploadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifacts.Upload(ctx, "data/in.unknownext", ArtifactFromBytes(nil), ArtifactUploadOptions{}); err != nil {
		t.Fatal(err)
	}
	if file.Path != "data/in.json" || file.Size != 7 {
		t.Errorf("file = %+v", file)
	}
	if !reflect.DeepEqual(types, []string{"application/json", "application/octet-stream"}) || !reflect.DeepEqual(lengths, []string{"7", "0"}) {
		t.Errorf("content types %q lengths %q", types, lengths)
	}
	if _, hasMetadata := starts[0]["metadata"]; hasMetadata || starts[0]["name"] != artifactsPrefix+"data/in.json" {
		t.Errorf("upload start = %v, want the object name and no metadata without a note", starts[0])
	}
	if !reflect.DeepEqual(sent, []string{`{"a":1}`, ""}) || !reflect.DeepEqual(ranges, []string{"bytes 0-6/7", ""}) {
		t.Errorf("sent %q with ranges %q, want the file then an empty body without a Content-Range", sent, ranges)
	}
}

// TestArtifacts_UploadGivesUpAfterTheResumeBudget: an upload whose session keeps failing
// asks storage what it kept after each backoff, and returns the last refusal after five
// attempts in a row that move nothing forward.
func TestArtifacts_UploadGivesUpAfterTheResumeBudget(t *testing.T) {
	var puts []string
	a := newArtifactStorage(t, nil)
	a.handle = func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Location", a.srv.URL+"/upload/storage/v1/b/bkt/o?upload_id=u1")
			return
		}
		puts = append(puts, r.Header.Get("Content-Range"))
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"code":503,"message":"Backend Error"}}`)
	}
	_, err := a.client.Artifacts(artifactsComputer).Upload(context.Background(), "x.txt", ArtifactFromBytes([]byte("abc")), ArtifactUploadOptions{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 || apiErr.Detail != "Backend Error" || !apiErr.Retryable {
		t.Fatalf("err = %v, want the retryable storage 503", err)
	}
	want := []string{"bytes 0-2/3", "bytes */3", "bytes */3", "bytes */3", "bytes */3", "bytes */3"}
	if !reflect.DeepEqual(puts, want) {
		t.Errorf("PUTs = %q, want %q", puts, want)
	}
	if sleeps := a.clock.recorded(); !reflect.DeepEqual(sleeps, []int64{500, 1000, 2000, 4000, 5000}) {
		t.Errorf("sleeps = %v ms", sleeps)
	}
}

func TestArtifacts_RefusesBadArguments(t *testing.T) {
	ctx := context.Background()
	a := newArtifactStorage(t, func(w http.ResponseWriter, r *http.Request) { t.Errorf("storage called: %s", r.URL.Path) })
	if _, err := a.client.Artifacts("not-a-uuid").List(ctx, ArtifactListOptions{}); err == nil {
		t.Error("List accepted an invalid Computer id")
	}
	if _, err := a.client.Artifacts(artifactsComputer).Copy(ctx, "not-a-uuid", "a", "b"); err == nil {
		t.Error("Copy accepted an invalid source Computer id")
	}
	if err := a.client.Artifacts(artifactsComputer).Delete(ctx, ""); err == nil {
		t.Error("Delete accepted an empty path")
	}
	if mints, _ := a.recorded(); len(mints) != 0 {
		t.Errorf("a refused call minted %d tokens", len(mints))
	}
	withoutKey, err := NewClient(ClientOptions{
		Endpoint: "http://pine.localhost:8080", ControlTokenProvider: func(context.Context, bool) (string, error) { return "", nil },
		AttachCredentialsProvider: func(context.Context, AttachCredentialsRequest) (*AttachCredentials, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutKey.Artifacts(artifactsComputer).List(ctx, ArtifactListOptions{}); err == nil || !strings.Contains(err.Error(), "APIKey") {
		t.Errorf("List without a project key = %v, want an error naming APIKey", err)
	}
	comp := newComputer(artifactsComputer, make([]byte, 32))
	for _, expiry := range []time.Duration{90500 * time.Millisecond, -time.Minute, 59 * time.Second, 30*24*time.Hour + time.Second} {
		if _, err := comp.ArtifactLink(ctx, "a.pdf", expiry); err == nil || errors.Is(err, ErrNotBound) {
			t.Errorf("ArtifactLink(%v) = %v, want the expiry refused", expiry, err)
		}
	}
	for _, expiry := range []time.Duration{time.Minute, 30 * 24 * time.Hour} {
		if _, err := comp.ArtifactLink(ctx, "a.pdf", expiry); !errors.Is(err, ErrNotBound) {
			t.Errorf("ArtifactLink(%v) = %v, want it past the expiry check", expiry, err)
		}
	}
	if _, err := comp.ArtifactLink(ctx, "a.pdf", 0); !errors.Is(err, ErrNotBound) {
		t.Errorf("ArtifactLink on an unbound Computer = %v, want ErrNotBound", err)
	}
}

// TestArtifacts_AnUnknownComputerIsUnknownComputerError: the Portal's 404 for the token
// is the same *UnknownComputerError the other project-key calls return.
func TestArtifacts_AnUnknownComputerIsUnknownComputerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":"COMPUTER_NOT_FOUND","message":"The Computer wasn't found."}}`)
	}))
	defer srv.Close()
	client, err := NewClient(ClientOptions{Endpoint: "http://pine.localhost:8080", ControlHost: strings.TrimPrefix(srv.URL, "http://"), APIKey: "pk_test"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Artifacts(artifactsComputer).List(context.Background(), ArtifactListOptions{})
	var unknown *UnknownComputerError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %T (%v), want *UnknownComputerError", err, err)
	}
}

// TestArtifacts_InvalidPathsAreRefusedBeforeAnyRequest: every artifact path argument is
// held to the shared rule (contract/artifact-paths.json) before the SDK asks for a token
// or calls the Computer.
func TestArtifacts_InvalidPathsAreRefusedBeforeAnyRequest(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "contract", "artifact-paths.json"))
	if err != nil {
		t.Skipf("contract artifact not present (mirror build): %v", err)
	}
	var vectors struct {
		Invalid      []string `json:"invalid"`
		InvalidBytes []struct {
			Hex string `json:"hex"`
		} `json:"invalid_bytes"`
		Length struct {
			InvalidBytes int `json:"invalid_bytes"`
		} `json:"length"`
	}
	if err := json.Unmarshal(b, &vectors); err != nil {
		t.Fatal(err)
	}
	invalid := append(vectors.Invalid, strings.Repeat("a", vectors.Length.InvalidBytes))
	for _, c := range vectors.InvalidBytes {
		raw, err := hex.DecodeString(c.Hex)
		if err != nil {
			t.Fatal(err)
		}
		invalid = append(invalid, string(raw))
	}

	var requests []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()
	client, err := NewClient(ClientOptions{Endpoint: "http://pine.localhost:8080", ControlHost: strings.TrimPrefix(srv.URL, "http://"), APIKey: "pk_test"})
	if err != nil {
		t.Fatal(err)
	}
	comp := newComputer(artifactsComputer, make([]byte, 32))
	if err := comp.adopt(buildTestConnection(t, srv.URL, srv.URL), "sb-1", "ct_tok", "running"); err != nil {
		t.Fatal(err)
	}
	arts := client.Artifacts(artifactsComputer)
	ctx := context.Background()
	destination := filepath.Join(t.TempDir(), "out")
	for _, p := range invalid {
		calls := map[string]error{}
		_, calls["Upload"] = arts.Upload(ctx, p, ArtifactFromBytes([]byte("x")), ArtifactUploadOptions{})
		_, calls["Download"] = arts.Download(ctx, p, destination)
		_, calls["Open"] = arts.Open(ctx, p)
		_, calls["Copy source"] = arts.Copy(ctx, artifactsOther, p, "ok.txt")
		_, calls["Copy destination"] = arts.Copy(ctx, artifactsOther, "ok.txt", p)
		calls["Delete"] = arts.Delete(ctx, p)
		_, calls["ArtifactLink"] = comp.ArtifactLink(ctx, p, 0)
		for name, err := range calls {
			if err == nil {
				t.Errorf("%s(%q) accepted the path", name, p)
			}
		}
	}
	if len(requests) != 0 {
		t.Errorf("refused paths still sent %q", requests)
	}
}
