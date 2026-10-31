package artifacts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/base/wait"
)

// errReaderClosed is a Read after Close.
var errReaderClosed = errors.New("pinesandbox: read from a closed artifact")

// Open streams the file at path; the caller closes the reader. A connection that drops
// mid-file is reopened from the bytes already read, on the shared reconnect cadence
// (connect). A missing file is a *NotFoundError.
func (s *Store) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	r := &reader{ctx: ctx, s: s, path: path, size: -1}
	if err := r.connect(nil); err != nil {
		return nil, err
	}
	return r, nil
}

// Download writes the file at path to destination through a temporary file beside it,
// renamed into place once complete, and returns the bytes written.
func (s *Store) Download(ctx context.Context, path, destination string) (int64, error) {
	if destination == "" {
		return 0, errors.New("pinesandbox: download destination required")
	}
	r, err := s.Open(ctx, path)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	tmp, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".*.part")
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(tmp, r)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), destination)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return 0, err
	}
	return n, nil
}

// reader is one file's bytes. After a dropped connection it asks storage again for the
// bytes from offset, of the same generation of the file, with a Range request.
type reader struct {
	ctx        context.Context
	s          *Store
	path       string
	body       io.ReadCloser
	host, op   string // the open request, for a read fault
	offset     int64  // bytes delivered
	size       int64  // the file's length, -1 until storage says
	generation string // the file's generation, from the first response
	failures   int    // resume attempts in a row that delivered nothing
	dropped    error  // why the last body ended early; the next Read reopens
	err        error  // the terminal error every later Read returns
}

func (r *reader) Read(p []byte) (int, error) {
	for {
		if r.err != nil {
			return 0, r.err
		}
		if r.body == nil {
			if err := r.connect(r.dropped); err != nil {
				r.err = err
				return 0, err
			}
			r.dropped = nil
		}
		n, err := r.body.Read(p)
		r.offset += int64(n)
		if n > 0 {
			r.failures = 0
		}
		switch {
		case err == nil:
			return n, nil
		case errors.Is(err, io.EOF) && (r.size < 0 || r.offset >= r.size):
			r.err = io.EOF
			return n, io.EOF
		}
		r.body.Close()
		r.body = nil
		if cerr := r.ctx.Err(); cerr != nil {
			r.err = cerr
			return n, cerr
		}
		r.dropped = transport.Fault(r.host, r.op, err)
		if n > 0 {
			return n, nil
		}
	}
}

func (r *reader) Close() error {
	r.err = errReaderClosed
	if r.body == nil {
		return nil
	}
	err := r.body.Close()
	r.body = nil
	return err
}

// connect opens the file at r.offset. After a failure (last) it waits the shared
// reconnect backoff first; it gives up after wait.ReconnectBudget attempts in a row that
// deliver nothing.
func (r *reader) connect(last error) error {
	for {
		if last != nil {
			if r.failures++; r.failures > wait.ReconnectBudget {
				return last
			}
			if err := r.s.clock.Sleep(r.ctx, wait.ReconnectBackoff(r.s.clock, r.failures)); err != nil {
				return err
			}
		}
		body, err := withToken(r.ctx, r.s, "", false, r.open)
		if err == nil {
			r.body = body
			return nil
		}
		if err = notFound(err, r.path); !resumable(r.ctx, err) {
			return err
		}
		last = err
	}
}

// open sends one GET for the file's bytes from r.offset with t.
func (r *reader) open(t *token) (io.ReadCloser, error) {
	object, redacted := t.object(t.prefix + r.path)
	query := url.Values{"alt": {"media"}}
	// identity keeps storage's byte offsets those of the file, which a Range resumes by.
	headers := map[string]string{"Accept-Encoding": "identity"}
	if r.offset > 0 {
		headers["Range"] = fmt.Sprintf("bytes=%d-", r.offset)
		if r.generation != "" {
			query.Set("ifGenerationMatch", r.generation)
		}
	}
	c := r.s.transport(t.scheme, t.host)
	r.host, r.op = c.Host(), "GET "+redacted
	resp, err := c.Stream(r.ctx, http.MethodGet, object+"?"+query.Encode(), transport.Request{
		Token: t.accessToken, Accept: "*/*", Headers: headers, Op: r.op,
	})
	if err != nil {
		return nil, err
	}
	switch resp.Status {
	case http.StatusOK:
		if r.generation == "" {
			r.generation = resp.Headers.Get("X-Goog-Generation")
		}
		if n, err := strconv.ParseInt(resp.Headers.Get("Content-Length"), 10, 64); err == nil {
			r.size = n
		}
		body := r.watch(resp.Body)
		// Storage sent the whole file, not the range: skip what was already read.
		if r.offset > 0 {
			if _, err := io.CopyN(io.Discard, body, r.offset); err != nil {
				body.Close()
				return nil, transport.Fault(r.host, r.op, err)
			}
		}
		return body, nil
	case http.StatusPartialContent:
		start, total, ok := contentRange(resp.Headers.Get("Content-Range"))
		if !ok || start != r.offset {
			resp.Body.Close()
			return nil, fmt.Errorf("pinesandbox: storage resumed the artifact at the wrong place (Content-Range %q)", resp.Headers.Get("Content-Range"))
		}
		if total >= 0 {
			r.size = total
		}
		return r.watch(resp.Body), nil
	default:
		body := r.watch(resp.Body)
		b, _ := io.ReadAll(io.LimitReader(body, replyCap))
		body.Close()
		return nil, storageError(r.host, r.op, resp.Status, b, resp.Headers)
	}
}

// watch is body, closed and failed as a timeout when a read waits transferIdleTimeout
// for bytes, so the reader resumes.
func (r *reader) watch(body io.ReadCloser) io.ReadCloser {
	return transport.WatchIdle(body, transferIdleTimeout,
		&transport.TimeoutError{Host: r.host, Op: r.op, Msg: "storage sent nothing for " + transferIdleTimeout.String()})
}

// contentRange reads "bytes start-end/total"; total is -1 when it is "*".
func contentRange(header string) (start, total int64, ok bool) {
	span, size, found := strings.Cut(strings.TrimPrefix(header, "bytes "), "/")
	first, _, found2 := strings.Cut(span, "-")
	if !found || !found2 {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if size == "*" {
		return start, -1, true
	}
	total, err = strconv.ParseInt(size, 10, 64)
	return start, total, err == nil
}
