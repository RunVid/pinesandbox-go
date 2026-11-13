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

// ListFiles lists the session workdir (the session token, or the Computer token for a handle without one).
func (s *Session) ListFiles(ctx context.Context, opts ListFilesOptions) ([]*FileEntry, error) {
	return sessionCall(s, func(coord *coordinator.Client, ct string) ([]*FileEntry, error) {
		return coord.ListFiles(ctx, s.readToken(ct), s.name, opts)
	})
}

// ReadFile returns a workdir file's raw bytes (the session token, or the Computer token for a handle without one).
func (s *Session) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return sessionCall(s, func(coord *coordinator.Client, ct string) ([]byte, error) {
		return coord.ReadFile(ctx, s.readToken(ct), s.name, path)
	})
}

// WriteFile deposits content as a material (agent input) under attachments/ (the session token, or the Computer token for a handle without one).
func (s *Session) WriteFile(ctx context.Context, path string, content []byte) (json.RawMessage, error) {
	return sessionCall(s, func(coord *coordinator.Client, ct string) (json.RawMessage, error) {
		return coord.WriteFile(ctx, s.readToken(ct), s.name, path, content)
	})
}

// ListArtifacts lists the session's artifacts, optionally scoped to a turn (the session token, or the Computer token for a handle without one).
func (s *Session) ListArtifacts(ctx context.Context, turnID string) ([]*Artifact, error) {
	return sessionCall(s, func(coord *coordinator.Client, ct string) ([]*Artifact, error) {
		return coord.ListArtifacts(ctx, s.readToken(ct), s.name, turnID)
	})
}

// ReadArtifact returns an artifact's raw bytes (the session token, or the Computer token for a handle without one).
func (s *Session) ReadArtifact(ctx context.Context, id string) ([]byte, error) {
	return sessionCall(s, func(coord *coordinator.Client, ct string) ([]byte, error) {
		return coord.ReadArtifact(ctx, s.readToken(ct), s.name, id)
	})
}

// OpenArtifact opens a streaming read of an artifact's bytes — ReadArtifact
// without buffering the whole file, for consumers that copy artifacts onward
// (object storage, an HTTP response) and should hold O(1) memory rather than the
// platform's full per-artifact cap. The caller MUST Close the returned reader.
func (s *Session) OpenArtifact(ctx context.Context, id string) (io.ReadCloser, error) {
	return sessionCall(s, func(coord *coordinator.Client, ct string) (io.ReadCloser, error) {
		return coord.OpenArtifact(ctx, s.readToken(ct), s.name, id)
	})
}

// OpenFile opens a streaming read of a workdir file's bytes — ReadFile without
// buffering the whole file, for a large file a turn's result names, copied onward
// in O(1) memory. The caller MUST Close the returned reader.
func (s *Session) OpenFile(ctx context.Context, path string) (io.ReadCloser, error) {
	return sessionCall(s, func(coord *coordinator.Client, ct string) (io.ReadCloser, error) {
		return coord.OpenFile(ctx, s.readToken(ct), s.name, path)
	})
}

// ZipArtifacts returns a zip of the session's artifacts, optionally turn-scoped (the session token, or the Computer token for a handle without one).
func (s *Session) ZipArtifacts(ctx context.Context, turnID string) ([]byte, error) {
	return sessionCall(s, func(coord *coordinator.Client, ct string) ([]byte, error) {
		return coord.ZipArtifacts(ctx, s.readToken(ct), s.name, turnID)
	})
}

// UploadArtifact uploads bytes as a new artifact (the session token, or the Computer token for a handle without one).
func (s *Session) UploadArtifact(ctx context.Context, filename string, content []byte) (*Artifact, error) {
	return sessionCall(s, func(coord *coordinator.Client, ct string) (*Artifact, error) {
		return coord.UploadArtifact(ctx, s.readToken(ct), s.name, filename, content)
	})
}
