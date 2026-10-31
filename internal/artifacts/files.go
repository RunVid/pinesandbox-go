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

	"go.pinesandbox.io/computer/internal/base/transport"
)

// ListOptions narrow and page a listing.
type ListOptions struct {
	// Prefix lists the paths that start with it, such as "reports/".
	Prefix string
	// Recursive lists every file under Prefix instead of one folder level.
	Recursive bool
	// PageToken continues from a previous page's NextPageToken.
	PageToken string
	// PageSize caps the page's files and folders together (0: storage's default, 1,000).
	PageSize int
}

// Page is one page of a listing.
type Page struct {
	Files []File
	// Folders are the folders one level below the prefix, each ending in "/"; none when
	// the listing is recursive.
	Folders []string
	// NextPageToken continues the listing as ListOptions.PageToken; "" on the last page.
	NextPageToken string
}

// List returns one page of the files and folders under opts.Prefix.
func (s *Store) List(ctx context.Context, opts ListOptions) (*Page, error) {
	return withToken(ctx, s, "", false, func(t *token) (*Page, error) {
		query := url.Values{"prefix": {t.prefix + opts.Prefix}}
		if !opts.Recursive {
			query.Set("delimiter", "/")
		}
		if opts.PageToken != "" {
			query.Set("pageToken", opts.PageToken)
		}
		if opts.PageSize > 0 {
			query.Set("maxResults", strconv.Itoa(opts.PageSize))
		}
		resp, err := s.do(ctx, t, http.MethodGet, t.objects()+"?"+query.Encode(), transport.Request{})
		if err != nil {
			return nil, err
		}
		var w struct {
			Items         []objectWire `json:"items"`
			Prefixes      []string     `json:"prefixes"`
			NextPageToken string       `json:"nextPageToken"`
		}
		if err := json.Unmarshal(resp.Body, &w); err != nil {
			return nil, fmt.Errorf("pinesandbox: unparseable artifact listing: %w", err)
		}
		out := &Page{Files: make([]File, 0, len(w.Items)), Folders: make([]string, 0, len(w.Prefixes)), NextPageToken: w.NextPageToken}
		for _, item := range w.Items {
			out.Files = append(out.Files, *item.file(t.prefix))
		}
		for _, folder := range w.Prefixes {
			out.Folders = append(out.Folders, strings.TrimPrefix(folder, t.prefix))
		}
		return out, nil
	})
}

// Delete deletes the file at path. A missing file is a *NotFoundError, unless the 404
// answers a resend after a lost response: the first send deleted the file.
func (s *Store) Delete(ctx context.Context, path string) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	_, err := withToken(ctx, s, "", false, func(t *token) (struct{}, error) {
		object, redacted := t.object(t.prefix + path)
		op := "DELETE " + redacted
		resp, err := s.send(ctx, t, http.MethodDelete, object, transport.Request{Op: op})
		switch {
		case err != nil:
			return struct{}{}, err
		case resp.Status >= 200 && resp.Status < 300,
			resp.Status == http.StatusNotFound && resp.Attempt > 1:
			return struct{}{}, nil
		}
		return struct{}{}, notFound(storageError(t.host, op, resp.Status, resp.Body, resp.Headers), path)
	})
	return err
}

// Copy copies the file at fromPath in Computer fromComputerID's artifacts to path in this
// Computer's, inside storage, and returns the copy. The token it uses also reads
// fromComputerID's artifacts. A missing source is a *NotFoundError for fromPath; a
// Computer over its storage limit is a *StorageFullError, as for Upload.
func (s *Store) Copy(ctx context.Context, fromComputerID, fromPath, path string) (*File, error) {
	if err := CheckPath(fromPath); err != nil {
		return nil, err
	}
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	from := fromComputerID
	if from == s.cfg.ComputerID {
		from = "" // a Computer's own token reads its own artifacts
	}
	// A large file takes several rewrite calls. Each gets its own token, so one
	// refreshed partway carries on from rewriteToken instead of starting over.
	rewriteToken := ""
	for {
		step, err := withToken(ctx, s, from, true, func(t *token) (rewriteStep, error) {
			sourcePrefix := t.prefix
			if from != "" {
				sourcePrefix = t.copyFrom[from]
			}
			source, redacted := t.object(sourcePrefix + fromPath)
			target := "/rewriteTo/b/" + escape(t.bucket) + "/o/"
			call := source + target + escape(t.prefix+path)
			if rewriteToken != "" {
				call += "?" + url.Values{"rewriteToken": {rewriteToken}}.Encode()
			}
			// Rewriting the same object to the same name again changes nothing, so a
			// lost response is safe to resend.
			resp, err := s.do(ctx, t, http.MethodPost, call, transport.Request{
				Body: []byte("{}"), ContentType: "application/json", RetryOnTransient: true,
				Op: "POST " + redacted + target + "{object}",
			})
			if err != nil {
				return rewriteStep{}, notFound(err, fromPath)
			}
			step := rewriteStep{prefix: t.prefix}
			if err := json.Unmarshal(resp.Body, &step); err != nil {
				return rewriteStep{}, fmt.Errorf("pinesandbox: unparseable artifact copy: %w", err)
			}
			return step, nil
		})
		switch {
		case err != nil:
			return nil, err
		case step.Done && step.Resource != nil:
			return step.Resource.file(step.prefix), nil
		case step.Done || step.RewriteToken == "":
			return nil, errors.New("pinesandbox: storage ended the artifact copy without the copy")
		}
		rewriteToken = step.RewriteToken
	}
}

// rewriteStep is storage's answer to one rewrite call, with the prefix of the token
// that sent it.
type rewriteStep struct {
	Done         bool        `json:"done"`
	RewriteToken string      `json:"rewriteToken"`
	Resource     *objectWire `json:"resource"`
	prefix       string
}
