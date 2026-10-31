// Package artifacts reaches a Computer's artifacts: the files it keeps beyond its
// sessions, whether or not it is running. Pine hands out one short-lived storage token
// for the Computer (POST /v1/computers/{id}/artifacts/token, with the project key); every
// file operation then goes to the Cloud Storage JSON API at the token's endpoint
// directly, so no file bytes or per-file calls pass through Pine. Domain package (uses
// internal/base/transport).
package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/base/wait"
	"go.pinesandbox.io/computer/internal/tokens"
)

const (
	// refreshBefore is how long before its expiry a cached token is replaced.
	refreshBefore = 5 * time.Minute
	// noteKey is the object metadata key that holds a file's note.
	noteKey = "pine-note"
	// replyCap bounds how much of a storage reply that is not a file is read.
	replyCap = 64 << 10
)

// Config is what a Store needs.
type Config struct {
	ComputerID string
	// Portal is the control host's client; APIKey is the project key the token request
	// sends. Without one, every operation returns an error.
	Portal *transport.Client
	APIKey string
	// NewTransport builds the client for a storage host (host[:port] over scheme).
	NewTransport func(scheme, host string) *transport.Client
	Clock        wait.Clock
}

// Store is one Computer's artifacts. It caches a storage token for the Computer, and one
// per Computer it copies from, until refreshBefore its expiry. Safe for concurrent use.
type Store struct {
	cfg   Config
	clock wait.Clock

	tokenMu sync.Mutex
	tokens  map[string]*token // by copy source; "" is the Computer's own

	transportMu sync.Mutex
	transports  map[string]*transport.Client // by scheme://host
}

// New builds a Store.
func New(cfg Config) *Store {
	clock := wait.Or(cfg.Clock)
	if cfg.NewTransport == nil {
		cfg.NewTransport = func(scheme, host string) *transport.Client {
			return transport.New(scheme, host, transport.WithClock(clock))
		}
	}
	return &Store{cfg: cfg, clock: clock, tokens: map[string]*token{}, transports: map[string]*transport.Client{}}
}

// token is one storage token and where the Computer's artifacts are.
type token struct {
	accessToken string
	expiresAt   time.Time
	// scheme, host and basePath are the storage endpoint.
	scheme, host, basePath string
	bucket, prefix         string
	writable               bool
	usedBytes, limitBytes  int64
	copyFrom               map[string]string
}

// tokenWire is the ArtifactToken response (portal-public-api.yaml), held to the spec by
// schema_conformance_test.go. Pointers tell a missing required field from its zero value.
type tokenWire struct {
	AccessToken string            `json:"access_token"`
	ExpiresAt   string            `json:"expires_at"`
	Endpoint    string            `json:"endpoint"`
	Bucket      string            `json:"bucket"`
	Prefix      string            `json:"prefix"`
	Writable    *bool             `json:"writable"`
	UsedBytes   *int64            `json:"used_bytes"`
	LimitBytes  *int64            `json:"limit_bytes"`
	CopyFrom    map[string]string `json:"copy_from"`
}

type tokenRequestWire struct {
	CopyFrom []string `json:"copy_from,omitempty"`
}

// tokenFor returns the cached token for from ("" for the Computer's own, else a Computer
// the token also reads), minting one when there is none or it is near its expiry.
// Concurrent callers wait for one mint. For a write, a kept token that isn't writable is
// replaced too, since the Computer may have made room since it was minted; a write whose
// fresh token isn't writable either is a *StorageFullError.
func (s *Store) tokenFor(ctx context.Context, from string, write bool) (*token, error) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	t := s.tokens[from]
	if t == nil || !s.clock.Now().Before(t.expiresAt.Add(-refreshBefore)) || write && !t.writable {
		var err error
		if t, err = s.mint(ctx, from); err != nil {
			return nil, err
		}
		s.tokens[from] = t
	}
	if write && !t.writable {
		return nil, &StorageFullError{UsedBytes: t.usedBytes, LimitBytes: t.limitBytes}
	}
	return t, nil
}

// drop forgets t once storage has refused it, so the next call mints another.
func (s *Store) drop(from string, t *token) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if s.tokens[from] == t {
		delete(s.tokens, from)
	}
}

// mint asks Pine for a storage token for the Computer's artifacts that also reads from's.
// A refusal is Portal's *problem.APIError.
func (s *Store) mint(ctx context.Context, from string) (*token, error) {
	if s.cfg.APIKey == "" || s.cfg.Portal == nil {
		return nil, errors.New("pinesandbox: artifacts need the project key (ClientOptions.APIKey)")
	}
	req := transport.Request{Headers: map[string]string{"Authorization": "Bearer " + s.cfg.APIKey}}
	if from != "" {
		body, err := json.Marshal(tokenRequestWire{CopyFrom: []string{from}})
		if err != nil {
			return nil, fmt.Errorf("pinesandbox: marshal artifact token request: %w", err)
		}
		req.Body, req.ContentType = body, "application/json"
	}
	resp, err := s.cfg.Portal.Do(ctx, http.MethodPost, "/v1/computers/"+url.PathEscape(s.cfg.ComputerID)+"/artifacts/token", req)
	var ae *problem.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return nil, tokens.NewUnknownComputerError("unknown, deleted, or cross-project computer_id "+s.cfg.ComputerID, ae)
	}
	if err != nil {
		return nil, err
	}
	t, err := parseToken(resp.Body)
	if err == nil && from != "" && t.copyFrom[from] == "" {
		err = fmt.Errorf("it doesn't reach Computer %s", from)
	}
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: invalid artifact token response (request_id=%s): %w", resp.Headers.Get("X-Request-Id"), err)
	}
	return t, nil
}

func parseToken(body []byte) (*token, error) {
	var w tokenWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}
	if w.AccessToken == "" || w.Bucket == "" || w.Prefix == "" || w.Writable == nil || w.UsedBytes == nil || w.LimitBytes == nil {
		return nil, errors.New("a required field is missing")
	}
	expiresAt, err := time.Parse(time.RFC3339, w.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("expires_at: %w", err)
	}
	endpoint, err := url.Parse(w.Endpoint)
	if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Host == "" {
		return nil, fmt.Errorf("endpoint %q is not an http(s) URL", w.Endpoint)
	}
	return &token{
		accessToken: w.AccessToken, expiresAt: expiresAt,
		scheme: endpoint.Scheme, host: endpoint.Host, basePath: strings.TrimSuffix(endpoint.EscapedPath(), "/"),
		bucket: w.Bucket, prefix: w.Prefix, writable: *w.Writable,
		usedBytes: *w.UsedBytes, limitBytes: *w.LimitBytes, copyFrom: w.CopyFrom,
	}, nil
}

// objects is the JSON API path of the objects in the token's bucket.
func (t *token) objects() string { return t.basePath + "/storage/v1/b/" + escape(t.bucket) + "/o" }

// object is the JSON API path of the object name, and the same path with the name left
// out, for errors.
func (t *token) object(name string) (path, redacted string) {
	return t.objects() + "/" + escape(name), t.objects() + "/{object}"
}

// withToken runs send with the token for from, a writable one for a write (tokenFor).
// When storage refuses it with 401, it mints a fresh token and runs send once more.
func withToken[T any](ctx context.Context, s *Store, from string, write bool, send func(*token) (T, error)) (T, error) {
	for attempt := 1; ; attempt++ {
		t, err := s.tokenFor(ctx, from, write)
		if err != nil {
			var zero T
			return zero, err
		}
		out, err := send(t)
		var ae *problem.APIError
		if attempt == 1 && errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
			s.drop(from, t)
			continue
		}
		return out, err
	}
}

// transport is the client for one storage host, built once.
func (s *Store) transport(scheme, host string) *transport.Client {
	s.transportMu.Lock()
	defer s.transportMu.Unlock()
	key := scheme + "://" + host
	c := s.transports[key]
	if c == nil {
		c = s.cfg.NewTransport(scheme, host)
		s.transports[key] = c
	}
	return c
}

// send sends one JSON API request with t's access token and returns storage's response,
// whatever its status.
func (s *Store) send(ctx context.Context, t *token, method, path string, req transport.Request) (*transport.Response, error) {
	req.Token = t.accessToken
	// A request the transport may resend after a transient fault is resent after an
	// answer the storage retry rule takes too.
	req.RetryStatus = retryableStatus
	return s.transport(t.scheme, t.host).DoRaw(ctx, method, path, req)
}

// do is send for a 2xx response; a refusal is a storageError.
func (s *Store) do(ctx context.Context, t *token, method, path string, req transport.Request) (*transport.Response, error) {
	resp, err := s.send(ctx, t, method, path, req)
	if err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		op := req.Op
		if op == "" {
			op = transport.Operation(method, path)
		}
		return nil, storageError(t.host, op, resp.Status, resp.Body, resp.Headers)
	}
	return resp, nil
}

// storageError is a storage refusal as the SDK's API error: the HTTP status and Cloud
// Storage's error.message, retryable by retryableStatus.
func storageError(host, op string, status int, body []byte, headers http.Header) *problem.APIError {
	var w struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	detail := http.StatusText(status)
	if json.Unmarshal(body, &w) == nil && w.Error.Message != "" {
		detail = w.Error.Message
	}
	return &problem.APIError{
		Status: status, Detail: detail, Host: host, Op: op, RequestID: headers.Get("X-Request-Id"),
		Retryable:  retryableStatus(status),
		RetryAfter: transport.RetryAfter(headers),
	}
}

// retryableStatus is the storage answers worth sending again
// (contract/storage-retry.json): a request timeout, too many requests, and any 5xx but
// 501. A 412, a read whose pinned version changed, never is.
func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests ||
		status >= 500 && status != http.StatusNotImplemented
}

// resumable reports whether a failed step of an upload or download is worth resuming: a
// dropped connection, a timeout, or an answer retryableStatus takes, and never once ctx
// has ended.
func resumable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var ce *transport.ConnectionError
	var te *transport.TimeoutError
	var ae *problem.APIError
	return errors.As(err, &ce) || errors.As(err, &te) || errors.As(err, &ae) && retryableStatus(ae.Status)
}

// escape percent-encodes s as one URL path segment: everything but RFC 3986's
// unreserved characters, "/" and spaces included, as the JSON API expects of an object
// name in a path.
func escape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// File is one file in a Computer's artifacts.
type File struct {
	// Path is the file's path in the Computer's artifacts, such as "reports/q3.pdf".
	Path        string
	Size        int64
	UpdatedAt   time.Time
	ContentType string
	// Note is the note saved with the file, if any.
	Note string
}

// objectWire is the part of a Cloud Storage object resource the SDK reads.
type objectWire struct {
	Name        string            `json:"name"`
	Size        string            `json:"size"`
	Updated     string            `json:"updated"`
	ContentType string            `json:"contentType"`
	Metadata    map[string]string `json:"metadata"`
}

// file is o as a File, its path relative to prefix.
func (o objectWire) file(prefix string) *File {
	size, _ := strconv.ParseInt(o.Size, 10, 64)
	updated, _ := time.Parse(time.RFC3339Nano, o.Updated)
	return &File{
		Path: strings.TrimPrefix(o.Name, prefix), Size: size, UpdatedAt: updated,
		ContentType: o.ContentType, Note: o.Metadata[noteKey],
	}
}

// maxPathBytes is the longest artifact path, in bytes.
const maxPathBytes = 900

// CheckPath applies the one artifact path rule every SDK, pine-artifact and the state
// broker share (contract/artifact-paths.json): 1 to 900 bytes of valid UTF-8, no control
// character (U+0000–U+001F, U+007F–U+009F), and no empty, "." or ".." segment, which
// also rules out a leading, trailing or doubled "/". Anything else is allowed.
func CheckPath(path string) error {
	if path == "" || len(path) > maxPathBytes {
		return fmt.Errorf("pinesandbox: an artifact path is 1 to %d bytes, got %d", maxPathBytes, len(path))
	}
	if !utf8.ValidString(path) {
		return errors.New("pinesandbox: an artifact path must be valid UTF-8")
	}
	for _, r := range path {
		if r <= 0x1f || 0x7f <= r && r <= 0x9f {
			return fmt.Errorf("pinesandbox: artifact path %q has a control character", path)
		}
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("pinesandbox: artifact path %q has an empty, \".\" or \"..\" segment", path)
		}
	}
	return nil
}
