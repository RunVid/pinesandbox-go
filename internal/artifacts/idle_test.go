package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.pinesandbox.io/computer/internal/base/transport"
)

// instantClock sleeps no time, so a resume backoff doesn't slow the tests.
type instantClock struct{}

func (instantClock) Now() time.Time                                   { return time.Now() }
func (instantClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func (instantClock) Jitter() float64                                  { return 0 }

const idleTestPrefix = "01980000-0000-7000-8000-000000000001/artifacts/"

// idleStore is a Store on one server that answers the token route and, through
// storage, handle. Its transfers stall after 100ms without bytes.
func idleStore(t *testing.T, handle http.HandlerFunc) *Store {
	t.Helper()
	saved := transferIdleTimeout
	t.Cleanup(func() { transferIdleTimeout = saved })
	transferIdleTimeout = 100 * time.Millisecond
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/artifacts/token") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "ya29.t", "expires_at": time.Now().Add(30 * time.Minute).Format(time.RFC3339),
				"endpoint": srv.URL, "bucket": "bkt", "prefix": idleTestPrefix,
				"writable": true, "used_bytes": 0, "limit_bytes": 1 << 30,
			})
			return
		}
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	return New(Config{
		ComputerID: "01980000-0000-7000-8000-000000000001", APIKey: "pk_test",
		Portal: transport.New("http", host), Clock: instantClock{},
	})
}

// A download whose connection goes quiet resumes from the bytes it has.
func TestDownloadResumesAStalledConnection(t *testing.T) {
	const content = "hello world"
	var mu sync.Mutex
	var ranges []string
	s := idleStore(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		if r.Header.Get("Range") == "" {
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			fmt.Fprint(w, content[:5])
			w.(http.Flusher).Flush()
			<-r.Context().Done() // the connection stays open and sends nothing more
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 5-%d/%d", len(content)-1, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		fmt.Fprint(w, content[5:])
	})
	body, err := s.Open(context.Background(), "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != content {
		t.Fatalf("read %q, %v; want %q", got, err, content)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(ranges, ",") != ",bytes=5-" {
		t.Fatalf("ranges = %q, want the resume from byte 5", ranges)
	}
}

// Time the caller spends between reads is not a stall.
func TestDownloadWaitsForASlowReader(t *testing.T) {
	gets := 0
	s := idleStore(t, func(w http.ResponseWriter, _ *http.Request) {
		gets++
		fmt.Fprint(w, "abc")
	})
	body, err := s.Open(context.Background(), "abc.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	var got bytes.Buffer
	b := make([]byte, 1)
	for {
		time.Sleep(150 * time.Millisecond)
		n, err := body.Read(b)
		got.Write(b[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if got.String() != "abc" || gets != 1 {
		t.Fatalf("read %q over %d GETs, want abc over one", got.String(), gets)
	}
}

// An upload whose reply stalls after its headers resumes too: storage is asked
// what it kept, and the upload finishes.
func TestUploadResumesAStalledReply(t *testing.T) {
	var mu sync.Mutex
	var puts []string
	var srvURL string
	stalled := make(chan struct{})
	s := idleStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Location", srvURL+"/upload-session")
			return
		}
		mu.Lock()
		puts = append(puts, r.Header.Get("Content-Range"))
		first := len(puts) == 1
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		if first {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "{")
			w.(http.Flusher).Flush()
			<-stalled // the rest of the reply never comes
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": idleTestPrefix + "small.txt", "size": "3", "updated": "2026-10-26T00:00:00Z"})
	})
	t.Cleanup(func() { close(stalled) })
	srvURL = "http://" + s.cfg.Portal.Host()
	file, err := s.Upload(context.Background(), "small.txt", BytesSource([]byte("abc")), UploadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(puts, ",") != "bytes 0-2/3,bytes */3" || file.Size != 3 {
		t.Fatalf("PUTs = %q, size %d; want the send and then a status query", puts, file.Size)
	}
}

// An upload whose send stalls is cancelled and resumed; the next send completes it.
func TestUploadResumesAStalledSend(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 32<<20) // more than the socket buffers hold
	var mu sync.Mutex
	var puts []string
	var srvURL string
	// A server doesn't see the client go while its handler leaves the body
	// unread, so the stalled handler waits for the test to end.
	stalled := make(chan struct{})
	s := idleStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Location", srvURL+"/upload-session")
			return
		}
		mu.Lock()
		puts = append(puts, r.Header.Get("Content-Range"))
		first := len(puts) == 1
		mu.Unlock()
		if first {
			<-stalled // storage stops taking the bytes
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Range"), "bytes */") {
			w.WriteHeader(http.StatusPermanentRedirect) // it kept none
			return
		}
		n, _ := io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"name": idleTestPrefix + "big.bin", "size": fmt.Sprint(n), "updated": "2026-10-26T00:00:00Z"})
	})
	t.Cleanup(func() { close(stalled) })
	srvURL = "http://" + s.cfg.Portal.Host()
	file, err := s.Upload(context.Background(), "big.bin", BytesSource(data), UploadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := fmt.Sprintf("bytes 0-%d/%d,bytes */%d,bytes 0-%d/%d", len(data)-1, len(data), len(data), len(data)-1, len(data))
	if strings.Join(puts, ",") != want || file.Size != int64(len(data)) {
		t.Fatalf("PUTs = %q, size %d; want %q", puts, file.Size, want)
	}
}
