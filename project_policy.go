package pinesandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"go.pinesandbox.io/computer/internal/base/transport"
)

// ProjectPolicy describes current admission, not an existing runtime's terms.
type ProjectPolicy struct {
	MaxConcurrentComputers *int                   `json:"max_concurrent_computers"`
	Lifecycle              *ProjectLifetimePolicy `json:"lifecycle"`
}

type ProjectLifetimePolicy struct {
	MaxIdleTimeoutSeconds int `json:"max_idle_timeout_seconds"`
	MaxRuntimeAgeSeconds  int `json:"max_runtime_age_seconds"`
}

// ProjectPolicy is optional configuration discovery before allocation. Normal
// runtime operations never call it; RuntimeDetails contains admitted limits.
func (c *Client) ProjectPolicy(ctx context.Context) (*ProjectPolicy, error) {
	if c.projectKey == "" {
		return nil, fmt.Errorf("pinesandbox: project policy requires a project API key")
	}
	response, err := c.portalTransport.Do(ctx, http.MethodGet, "/v1/project-policy", transport.Request{
		Headers: map[string]string{"Authorization": "Bearer " + c.projectKey},
	})
	if err != nil {
		return nil, err
	}
	var result ProjectPolicy
	var fields map[string]json.RawMessage
	if json.Unmarshal(response.Body, &result) != nil || json.Unmarshal(response.Body, &fields) != nil || fields["lifecycle"] == nil || fields["max_concurrent_computers"] == nil {
		return nil, fmt.Errorf("pinesandbox: invalid project policy response (request_id=%s)", response.Headers.Get("X-Request-Id"))
	}
	return &result, nil
}
