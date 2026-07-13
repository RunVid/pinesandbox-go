package pinesandbox

import (
	"context"
	"encoding/json"
	"io"

	"go.pinesandbox.io/computer/internal/coordinator"
)

// FileEntry / Artifact are the session file + artifact metadata types.
type (
	FileEntry = coordinator.FileEntry
	Artifact  = coordinator.Artifact
)

// ListFilesOptions narrows a workdir listing.
type ListFilesOptions = coordinator.ListFilesOptions

// ListFiles lists the session workdir (ps_).
func (s *Session) ListFiles(ctx context.Context, opts ListFilesOptions) ([]*FileEntry, error) {
	return s.coord.ListFiles(ctx, s.token, s.name, opts)
}

// ReadFile returns a workdir file's raw bytes (ps_).
func (s *Session) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return s.coord.ReadFile(ctx, s.token, s.name, path)
}

// WriteFile deposits content as a material (agent input) under attachments/ (ps_).
func (s *Session) WriteFile(ctx context.Context, path string, content []byte) (json.RawMessage, error) {
	return s.coord.WriteFile(ctx, s.token, s.name, path, content)
}

// ListArtifacts lists the session's artifacts, optionally scoped to a turn (ps_).
func (s *Session) ListArtifacts(ctx context.Context, turnID string) ([]*Artifact, error) {
	return s.coord.ListArtifacts(ctx, s.token, s.name, turnID)
}

// ReadArtifact returns an artifact's raw bytes (ps_).
func (s *Session) ReadArtifact(ctx context.Context, id string) ([]byte, error) {
	return s.coord.ReadArtifact(ctx, s.token, s.name, id)
}

// OpenArtifact opens a streaming read of an artifact's bytes (ps_) — ReadArtifact
// without buffering the whole file, for consumers that copy artifacts onward
// (object storage, an HTTP response) and should hold O(1) memory rather than the
// platform's full per-artifact cap. The caller MUST Close the returned reader.
func (s *Session) OpenArtifact(ctx context.Context, id string) (io.ReadCloser, error) {
	return s.coord.OpenArtifact(ctx, s.token, s.name, id)
}

// ZipArtifacts returns a zip of the session's artifacts, optionally turn-scoped (ps_).
func (s *Session) ZipArtifacts(ctx context.Context, turnID string) ([]byte, error) {
	return s.coord.ZipArtifacts(ctx, s.token, s.name, turnID)
}

// UploadArtifact uploads bytes as a new artifact (ps_).
func (s *Session) UploadArtifact(ctx context.Context, filename string, content []byte) (*Artifact, error) {
	return s.coord.UploadArtifact(ctx, s.token, s.name, filename, content)
}
