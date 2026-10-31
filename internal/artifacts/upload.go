package artifacts

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/base/wait"
)

// contentTypesJSON is the embedded copy of contract/artifact-content-types.json, the
// table every server SDK takes an upload's default content type from, so the same upload
// gets the same type everywhere. A drift test guards it against the canonical file.
//
//go:embed artifact-content-types.json
var contentTypesJSON []byte

// contentTypes is the embedded table, parsed once.
var contentTypes = mustLoadContentTypes()

type contentTypeTable struct {
	// Default is the type of a path whose extension the table doesn't name.
	Default string `json:"default"`
	// Types maps a lower-case extension, without its ".", to a type.
	Types map[string]string `json:"types"`
}

func mustLoadContentTypes() contentTypeTable {
	var table contentTypeTable
	if err := json.Unmarshal(contentTypesJSON, &table); err != nil {
		panic("artifacts: embedded artifact-content-types.json is invalid: " + err.Error())
	}
	return table
}

// defaultContentType is the content type of an upload to path that names none: the
// table's type for the extension of the path's last segment (after its last ".", when
// that isn't the segment's first character), compared in lower case, else the table's
// default.
func defaultContentType(path string) string {
	segment := path[strings.LastIndex(path, "/")+1:]
	if dot := strings.LastIndex(segment, "."); dot > 0 {
		if t, ok := contentTypes.Types[strings.ToLower(segment[dot+1:])]; ok {
			return t
		}
	}
	return contentTypes.Default
}

// Source is the content of an upload: a local file, bytes, or a reader of known size.
// A resumed upload reads it again from where storage stopped.
type Source struct {
	path string
	data io.ReaderAt
	size int64
}

// FileSource is the local file at path.
func FileSource(path string) Source { return Source{path: path} }

// BytesSource is content.
func BytesSource(content []byte) Source {
	return Source{data: bytes.NewReader(content), size: int64(len(content))}
}

// ReaderSource is the size bytes r reads from offset 0.
func ReaderSource(r io.ReaderAt, size int64) Source { return Source{data: r, size: size} }

// open returns the source's bytes, their length and what releases them.
func (src Source) open() (io.ReaderAt, int64, func() error, error) {
	if src.path != "" {
		f, err := os.Open(src.path)
		if err != nil {
			return nil, 0, nil, err
		}
		info, err := f.Stat()
		if err == nil && !info.Mode().IsRegular() {
			err = fmt.Errorf("pinesandbox: %s is not a regular file", src.path)
		}
		if err != nil {
			f.Close()
			return nil, 0, nil, err
		}
		return f, info.Size(), f.Close, nil
	}
	if src.data == nil || src.size < 0 {
		return nil, 0, nil, errors.New("pinesandbox: an upload source (a file, bytes, or a reader and its size) is required")
	}
	return src.data, src.size, func() error { return nil }, nil
}

// UploadOptions describe an uploaded file.
type UploadOptions struct {
	// ContentType is the file's media type; empty takes the type the shared table gives
	// the path's extension, else application/octet-stream.
	ContentType string
	// Note is a short description saved with the file and listed with it.
	Note string
}

// Upload saves src at path, replacing any file there, and returns the saved file. The
// bytes go to a resumable upload session; see sendUpload. While the Computer is over its
// storage limit (no writable token, after a fresh one) it is a *StorageFullError, and
// nothing is sent to storage.
func (s *Store) Upload(ctx context.Context, path string, src Source, opts UploadOptions) (*File, error) {
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	data, size, release, err := src.open()
	if err != nil {
		return nil, err
	}
	defer release()
	contentType := opts.ContentType
	if contentType == "" {
		contentType = defaultContentType(path)
	}
	type started struct {
		prefix  string
		session *url.URL
	}
	upload, err := withToken(ctx, s, "", true, func(t *token) (started, error) {
		session, err := s.startUpload(ctx, t, t.prefix+path, size, contentType, opts.Note)
		return started{t.prefix, session}, err
	})
	if err != nil {
		return nil, err
	}
	return s.sendUpload(ctx, upload.session, upload.prefix, data, size)
}

type uploadWire struct {
	Name        string            `json:"name"`
	ContentType string            `json:"contentType"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// startUpload opens a resumable upload session for the object name and returns its URI.
func (s *Store) startUpload(ctx context.Context, t *token, name string, size int64, contentType, note string) (*url.URL, error) {
	body := uploadWire{Name: name, ContentType: contentType}
	if note != "" {
		body.Metadata = map[string]string{noteKey: note}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal artifact upload: %w", err)
	}
	query := url.Values{"uploadType": {"resumable"}, "name": {name}}
	resp, err := s.do(ctx, t, http.MethodPost, t.basePath+"/upload/storage/v1/b/"+escape(t.bucket)+"/o?"+query.Encode(), transport.Request{
		Body: b, ContentType: "application/json; charset=UTF-8",
		Headers: map[string]string{
			"X-Upload-Content-Type":   contentType,
			"X-Upload-Content-Length": strconv.FormatInt(size, 10),
		},
		// Starting a session writes nothing; a session a lost response left unused
		// expires on its own.
		RetryOnTransient: true,
	})
	if err != nil {
		return nil, err
	}
	location, err := url.Parse(resp.Headers.Get("Location"))
	if err == nil {
		location = (&url.URL{Scheme: t.scheme, Host: t.host}).ResolveReference(location)
	}
	if err != nil || resp.Headers.Get("Location") == "" || (location.Scheme != "https" && location.Scheme != "http") {
		return nil, errors.New("pinesandbox: storage started no upload session")
	}
	return location, nil
}

// uploadReply is storage's answer to a PUT on an upload session: the saved object once
// the upload is complete, else how many bytes it has kept (a 308).
type uploadReply struct {
	object *objectWire
	kept   int64
}

// sendUpload sends size bytes of data to the upload session, which takes no credential.
// After a dropped connection, a 429 or a 5xx it waits the shared reconnect backoff, asks
// storage how many bytes it kept, and sends the rest. It gives up after
// wait.ReconnectBudget attempts in a row that move nothing forward.
func (s *Store) sendUpload(ctx context.Context, session *url.URL, prefix string, data io.ReaderAt, size int64) (*File, error) {
	c := s.transport(session.Scheme, session.Host)
	uri := session.RequestURI()
	var offset int64
	var last error // why the previous step failed; the next one asks what storage kept
	failures := 0
	for {
		var reply uploadReply
		var err error
		if last == nil {
			reply, err = s.putBytes(ctx, c, uri, data, offset, size)
		} else {
			if failures++; failures > wait.ReconnectBudget {
				return nil, last
			}
			if err := s.clock.Sleep(ctx, wait.ReconnectBackoff(s.clock, failures)); err != nil {
				return nil, err
			}
			reply, err = s.put(ctx, c, uri, nil, 0, fmt.Sprintf("bytes */%d", size))
		}
		switch {
		case err != nil && resumable(ctx, err):
			last = err
		case err != nil:
			return nil, err
		case reply.object != nil:
			return reply.object.file(prefix), nil
		case reply.kept > offset:
			offset, failures, last = reply.kept, 0, nil
		case last == nil:
			last = errors.New("pinesandbox: storage kept none of the bytes sent")
		default:
			offset, last = reply.kept, nil
		}
	}
}

// putBytes sends data from offset to the end, or, with nothing left to send, asks for
// the finished upload.
func (s *Store) putBytes(ctx context.Context, c *transport.Client, uri string, data io.ReaderAt, offset, size int64) (uploadReply, error) {
	switch {
	case size == 0:
		return s.put(ctx, c, uri, nil, 0, "")
	case offset >= size:
		return s.put(ctx, c, uri, nil, 0, fmt.Sprintf("bytes */%d", size))
	}
	n := size - offset
	return s.put(ctx, c, uri, io.NewSectionReader(data, offset, n), n, fmt.Sprintf("bytes %d-%d/%d", offset, size-1, size))
}

// put sends one PUT to the upload session. It goes over the stream client, which sets no
// overall deadline, so a large file takes as long as it needs; a send that stalls for
// transferIdleTimeout is cancelled as a timeout, which the upload resumes from.
func (s *Store) put(ctx context.Context, c *transport.Client, uri string, body io.Reader, length int64, contentRange string) (uploadReply, error) {
	op := transport.Operation(http.MethodPut, uri)
	sendCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req := transport.Request{ContentLength: length}
	var watch *sendWatchdog
	if body != nil {
		watch = newSendWatchdog(cancel)
		defer watch.stop()
		req.BodyReader = watch.watch(body)
	}
	if contentRange != "" {
		req.Headers = map[string]string{"Content-Range": contentRange}
	}
	resp, err := c.Stream(sendCtx, http.MethodPut, uri, req)
	if err != nil {
		if watch != nil && watch.fired.Load() && ctx.Err() == nil {
			return uploadReply{}, &transport.TimeoutError{Host: c.Host(), Op: op, Msg: "storage took no bytes for " + transferIdleTimeout.String()}
		}
		return uploadReply{}, err
	}
	// The stream client bounds only the headers; a reply that stalls after them
	// is a timeout, which the upload resumes from.
	reply := transport.WatchIdle(resp.Body, transferIdleTimeout,
		&transport.TimeoutError{Host: c.Host(), Op: op, Msg: "storage sent nothing for " + transferIdleTimeout.String()})
	defer reply.Close()
	b, err := io.ReadAll(io.LimitReader(reply, replyCap))
	if err != nil {
		return uploadReply{}, transport.Fault(c.Host(), op, err)
	}
	switch resp.Status {
	case http.StatusOK, http.StatusCreated:
		var o objectWire
		if err := json.Unmarshal(b, &o); err != nil {
			return uploadReply{}, fmt.Errorf("pinesandbox: unparseable uploaded artifact: %w", err)
		}
		return uploadReply{object: &o}, nil
	case http.StatusPermanentRedirect:
		return uploadReply{kept: keptBytes(resp.Headers.Get("Range"))}, nil
	default:
		return uploadReply{}, storageError(c.Host(), op, resp.Status, b, resp.Headers)
	}
}

// keptBytes reads a resumable upload's Range header, "bytes=0-N": storage has N+1 bytes.
// No header means none.
func keptBytes(header string) int64 {
	last, ok := strings.CutPrefix(header, "bytes=0-")
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(last, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n + 1
}
