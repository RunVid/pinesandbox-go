package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ArtifactLink is a public link to one of the Computer's artifacts, served by the running
// Computer.
type ArtifactLink struct {
	// URL is the link, on the Computer's host. Anyone with it can download the file.
	URL string
	// ExpiresAt is when the link stops working, when that is before the Computer stops;
	// nil when it works until the Computer stops.
	ExpiresAt *time.Time
}

// CreateArtifactLink makes a public link to the file at path in the Computer's artifacts
// (ct_-only). expiresInSeconds 0 leaves the expiry out: the link works until the Computer
// stops.
func (c *Client) CreateArtifactLink(ctx context.Context, token, path string, expiresInSeconds int) (*ArtifactLink, error) {
	body := map[string]any{"path": path}
	if expiresInSeconds > 0 {
		body["expires_in_seconds"] = expiresInSeconds
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal artifact link: %w", err)
	}
	resp, err := c.do(ctx, "POST", "/v1/artifacts/links", token, b)
	if err != nil {
		return nil, err
	}
	var w struct {
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(resp.Body, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable artifact link: %w", err)
	}
	if w.URL == "" {
		return nil, errors.New("pinesandbox: artifact link response has no url")
	}
	return &ArtifactLink{URL: w.URL, ExpiresAt: parseTime(w.ExpiresAt)}, nil
}
