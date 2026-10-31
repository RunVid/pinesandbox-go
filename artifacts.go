package pinesandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"go.pinesandbox.io/computer/internal/artifacts"
	"go.pinesandbox.io/computer/internal/coordinator"
)

// Artifact types (re-exported).
type (
	// ArtifactFile is one file in a Computer's artifacts.
	ArtifactFile = artifacts.File
	// ArtifactPage is one page of a listing: its files, the folders one level down, and
	// the token for the next page.
	ArtifactPage = artifacts.Page
	// ArtifactListOptions narrow and page Artifacts.List.
	ArtifactListOptions = artifacts.ListOptions
	// ArtifactUploadOptions describe the file Artifacts.Upload saves.
	ArtifactUploadOptions = artifacts.UploadOptions
	// ArtifactSource is what Artifacts.Upload saves: ArtifactFromFile, ArtifactFromBytes
	// or ArtifactFromReader.
	ArtifactSource = artifacts.Source
	// ArtifactLink is a public link to a file, served by the running Computer.
	ArtifactLink = coordinator.ArtifactLink
)

// ArtifactFromFile uploads the local file at localPath.
func ArtifactFromFile(localPath string) ArtifactSource { return artifacts.FileSource(localPath) }

// ArtifactFromBytes uploads content.
func ArtifactFromBytes(content []byte) ArtifactSource { return artifacts.BytesSource(content) }

// ArtifactFromReader uploads the size bytes r reads from offset 0. A resumed upload reads
// r again from where storage stopped.
func ArtifactFromReader(r io.ReaderAt, size int64) ArtifactSource {
	return artifacts.ReaderSource(r, size)
}

// Artifacts is a Computer's artifacts: the files it keeps beyond its sessions, reachable
// whether or not it is running, with the project key. The handle gets a short-lived
// storage token from Pine on first use and renews it before it lapses; every operation
// then goes to storage directly, never through Pine. Paths are relative to the Computer's
// artifacts, such as "reports/q3.pdf". Safe for concurrent use.
type Artifacts struct {
	store *artifacts.Store
	err   error // why every call fails, for an invalid Computer id
}

// Artifacts returns the artifacts of the Computer with this id. Each handle keeps its own
// storage token, so reuse one for a run of calls. Calls need the project key
// (ClientOptions.APIKey).
func (c *Client) Artifacts(computerID string) *Artifacts {
	a := &Artifacts{store: artifacts.New(artifacts.Config{
		ComputerID: computerID, Portal: c.portalTransport, APIKey: c.projectKey,
		NewTransport: c.newTransport, Clock: c.conn.clock,
	})}
	if !computerIDRE.MatchString(computerID) {
		a.err = errors.New("pinesandbox: valid Computer ID required")
	}
	return a
}

// List returns one page of the files and folders under opts.Prefix: one folder level, or
// every file below it with opts.Recursive. Pass the page's NextPageToken as
// opts.PageToken for the next page.
func (a *Artifacts) List(ctx context.Context, opts ArtifactListOptions) (*ArtifactPage, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.store.List(ctx, opts)
}

// Upload saves source at path, replacing any file there, and returns the saved file. A
// connection that drops mid-upload is resumed from what storage kept. While the Computer
// stores more than its limit, Upload returns *ArtifactStorageFullError and writes nothing.
func (a *Artifacts) Upload(ctx context.Context, path string, source ArtifactSource, opts ArtifactUploadOptions) (*ArtifactFile, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.store.Upload(ctx, path, source, opts)
}

// Download writes the file at path to the local file destination, through a temporary
// file beside it that replaces destination once complete, and returns the bytes written.
// A dropped connection resumes from the bytes already written. A missing file is
// *ArtifactNotFoundError.
func (a *Artifacts) Download(ctx context.Context, path, destination string) (int64, error) {
	if a.err != nil {
		return 0, a.err
	}
	return a.store.Download(ctx, path, destination)
}

// Open streams the file at path. A dropped connection is reopened from the bytes already
// read. The caller MUST Close the returned reader. A missing file is
// *ArtifactNotFoundError.
func (a *Artifacts) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.store.Open(ctx, path)
}

// Copy copies the file at fromPath in the artifacts of fromComputerID, another of the
// project's Computers, to path in this Computer's, inside storage, and returns the copy.
// A missing source is *ArtifactNotFoundError (for fromPath); a Computer over its storage
// limit is *ArtifactStorageFullError.
func (a *Artifacts) Copy(ctx context.Context, fromComputerID, fromPath, path string) (*ArtifactFile, error) {
	if a.err != nil {
		return nil, a.err
	}
	if !computerIDRE.MatchString(fromComputerID) {
		return nil, errors.New("pinesandbox: valid Computer ID required to copy from")
	}
	return a.store.Copy(ctx, fromComputerID, fromPath, path)
}

// Delete deletes the file at path. A missing file is *ArtifactNotFoundError.
func (a *Artifacts) Delete(ctx context.Context, path string) error {
	if a.err != nil {
		return a.err
	}
	return a.store.Delete(ctx, path)
}

// The expiries a link may have, besides 0 for until the Computer stops.
const (
	minArtifactLinkExpiry = time.Minute
	maxArtifactLinkExpiry = 30 * 24 * time.Hour
)

// ArtifactLink makes a link anyone can open to download the file at path in the
// Computer's artifacts, as it is when the link is opened. The running Computer serves it,
// so it stops working when the Computer stops or the file is deleted, and after expiresIn
// (whole seconds, from a minute to 30 days) when that is set; 0 lasts until the Computer
// stops.
func (c *Computer) ArtifactLink(ctx context.Context, path string, expiresIn time.Duration) (*ArtifactLink, error) {
	if err := artifacts.CheckPath(path); err != nil {
		return nil, err
	}
	if expiresIn != 0 && (expiresIn < minArtifactLinkExpiry || expiresIn > maxArtifactLinkExpiry || expiresIn%time.Second != 0) {
		return nil, fmt.Errorf("pinesandbox: link expiry must be whole seconds from 1m to 720h, got %v", expiresIn)
	}
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	return coord.CreateArtifactLink(ctx, ct, path, int(expiresIn/time.Second))
}
