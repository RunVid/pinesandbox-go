package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// FileEntry is a file/directory in a session workdir listing (paths relative to the workdir
// root; absolute container paths are never exposed).
type FileEntry struct {
	RelativePath string
	Name         string
	IsDir        bool
	Size         int64
	Mode         string
	ModifiedAt   *time.Time
}

type fileEntryWire struct {
	RelativePath string `json:"relative_path"`
	Name         string `json:"name"`
	IsDir        bool   `json:"is_dir"`
	Size         int64  `json:"size"`
	Mode         string `json:"mode"`
	ModifiedAt   string `json:"modified_at"`
}

// Artifact is a managed session output (upload / claimed download / agent task output).
type Artifact struct {
	ID           string
	SessionID    string
	TurnID       string
	Root         string
	RelativePath string
	ContentType  string
	Size         int64
	SHA256       string
	CreatedBy    string
	CreatedAt    *time.Time
	Retention    string
}

type artifactWire struct {
	ID           string `json:"id"`
	SessionID    string `json:"session_id"`
	TurnID       string `json:"turn_id"`
	Root         string `json:"root"`
	RelativePath string `json:"relative_path"`
	ContentType  string `json:"content_type"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	CreatedBy    string `json:"created_by"`
	CreatedAt    string `json:"created_at"`
	Retention    string `json:"retention"`
}

func (w artifactWire) toArtifact() *Artifact {
	return &Artifact{
		ID: w.ID, SessionID: w.SessionID, TurnID: w.TurnID, Root: w.Root, RelativePath: w.RelativePath,
		ContentType: w.ContentType, Size: w.Size, SHA256: w.SHA256, CreatedBy: w.CreatedBy,
		CreatedAt: parseTime(w.CreatedAt), Retention: w.Retention,
	}
}

// ListFilesOptions narrows a workdir listing.
type ListFilesOptions struct {
	Path    string // subdirectory (relative)
	Pattern string // glob
}

// ListFiles lists the session workdir (v1/sessions/{name}/files/list).
func (c *Client) ListFiles(ctx context.Context, token, name string, opts ListFilesOptions) ([]*FileEntry, error) {
	q := url.Values{}
	if opts.Path != "" {
		q.Set("path", opts.Path)
	}
	if opts.Pattern != "" {
		q.Set("pattern", opts.Pattern)
	}
	path := "/v1/sessions/" + url.PathEscape(name) + "/files/list"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	resp, err := c.do(ctx, "GET", path, token, nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Entries []fileEntryWire `json:"entries"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable file list: %w", err)
	}
	out := make([]*FileEntry, 0, len(env.Entries))
	for _, w := range env.Entries {
		out = append(out, &FileEntry{
			RelativePath: w.RelativePath, Name: w.Name, IsDir: w.IsDir, Size: w.Size, Mode: w.Mode,
			ModifiedAt: parseTime(w.ModifiedAt),
		})
	}
	return out, nil
}

// ReadFile returns a workdir file's raw bytes.
func (c *Client) ReadFile(ctx context.Context, token, name, path string) ([]byte, error) {
	route := "/v1/sessions/" + url.PathEscape(name) + "/files?" + url.Values{"path": {path}}.Encode()
	resp, err := c.send(ctx, coordReq{method: "GET", path: route, token: token, accept: "application/octet-stream"})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// WriteFile deposits content as a material (an agent input) under the session's
// attachments/ root. path is a single filename (no "/" or "..").
func (c *Client) WriteFile(ctx context.Context, token, name, path string, content []byte) (json.RawMessage, error) {
	route := "/v1/sessions/" + url.PathEscape(name) + "/files?" + url.Values{"path": {path}}.Encode()
	resp, err := c.send(ctx, coordReq{method: "PUT", path: route, token: token, body: content, contentType: "application/octet-stream"})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(resp.Body), nil
}

// ListArtifacts lists the session's artifacts (optionally scoped to a turn).
func (c *Client) ListArtifacts(ctx context.Context, token, name, turnID string) ([]*Artifact, error) {
	route := "/v1/sessions/" + url.PathEscape(name) + "/artifacts"
	if turnID != "" {
		route += "?" + url.Values{"turn_id": {turnID}}.Encode()
	}
	resp, err := c.do(ctx, "GET", route, token, nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Artifacts []artifactWire `json:"artifacts"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable artifact list: %w", err)
	}
	out := make([]*Artifact, 0, len(env.Artifacts))
	for _, w := range env.Artifacts {
		out = append(out, w.toArtifact())
	}
	return out, nil
}

// ReadArtifact returns an artifact's raw bytes.
func (c *Client) ReadArtifact(ctx context.Context, token, name, id string) ([]byte, error) {
	route := "/v1/sessions/" + url.PathEscape(name) + "/artifacts/" + url.PathEscape(id)
	resp, err := c.send(ctx, coordReq{method: "GET", path: route, token: token, accept: "application/octet-stream"})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ZipArtifacts returns a zip of the session's artifacts (optionally turn-scoped).
func (c *Client) ZipArtifacts(ctx context.Context, token, name, turnID string) ([]byte, error) {
	route := "/v1/sessions/" + url.PathEscape(name) + "/artifacts/zip"
	if turnID != "" {
		route += "?" + url.Values{"turn_id": {turnID}}.Encode()
	}
	resp, err := c.send(ctx, coordReq{method: "GET", path: route, token: token, accept: "application/zip"})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// UploadArtifact uploads bytes as a new artifact (created_by: upload).
func (c *Client) UploadArtifact(ctx context.Context, token, name, filename string, content []byte) (*Artifact, error) {
	route := "/v1/sessions/" + url.PathEscape(name) + "/artifacts?" + url.Values{"filename": {filename}}.Encode()
	resp, err := c.send(ctx, coordReq{method: "POST", path: route, token: token, body: content, contentType: "application/octet-stream"})
	if err != nil {
		return nil, err
	}
	var w artifactWire
	if err := json.Unmarshal(resp.Body, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable artifact: %w", err)
	}
	return w.toArtifact(), nil
}
