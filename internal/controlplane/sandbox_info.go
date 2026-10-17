package controlplane

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Lifetime is the policy admitted to this runtime, independent of current project defaults.
type Lifetime struct {
	IdleTimeoutSeconds    int       `json:"idle_timeout_seconds"`
	MinIdleTimeoutSeconds int       `json:"min_idle_timeout_seconds"`
	MaxIdleTimeoutSeconds int       `json:"max_idle_timeout_seconds"`
	MaxRuntimeAgeSeconds  int       `json:"max_runtime_age_seconds"`
	StopAt                time.Time `json:"stop_at"`
	Deadline              time.Time `json:"deadline"`
}

// SandboxInfo is the parsed lifecycle observation. The zone resolver, rather
// than the lifecycle response, supplies the runtime's data-plane address.
type SandboxInfo struct {
	RuntimeRevision string
	Lifecycle       *Lifetime
	ID              string
	ETag            string
	OperationID     string
	Retryable       *bool
	Reason          string
	Message         string
	Image           string // the image URI (present only in GET/LIST responses)
	Status          string // normalized lifecycle state (status.state, snake_cased + lowercased)
	Metadata        map[string]string
	Entrypoint      []string
	ExpiresAt       *time.Time
	CreatedAt       *time.Time
}

// sandboxWire is the nested server JSON; SandboxInfo flattens it.
type sandboxWire struct {
	RuntimeRevision string    `json:"runtime_revision"`
	Lifecycle       *Lifetime `json:"lifecycle"`
	ID              string    `json:"id"`
	Image           struct {
		URI string `json:"uri"`
	} `json:"image"`
	Status struct {
		State       string `json:"state"`
		OperationID string `json:"operation_id"`
		Retryable   *bool  `json:"retryable"`
		Reason      string `json:"reason"`
		Message     string `json:"message"`
	} `json:"status"`
	Metadata   map[string]string `json:"metadata"`
	Entrypoint []string          `json:"entrypoint"`
	ExpiresAt  string            `json:"expiresAt"`
	CreatedAt  string            `json:"createdAt"`
}

// ParseSandboxInfo builds a SandboxInfo from a create/get response body.
func ParseSandboxInfo(body []byte) (*SandboxInfo, error) {
	var w sandboxWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: control plane returned an unparseable runtime: %w", err)
	}
	info := &SandboxInfo{
		ID:              w.ID,
		Lifecycle:       w.Lifecycle,
		RuntimeRevision: w.RuntimeRevision,
		OperationID:     w.Status.OperationID,
		Retryable:       w.Status.Retryable,
		Reason:          w.Status.Reason,
		Message:         w.Status.Message,
		Image:           w.Image.URI,
		Status:          normalizeStatus(w.Status.State),
		Metadata:        w.Metadata,
		Entrypoint:      w.Entrypoint,
		ExpiresAt:       parseTime(w.ExpiresAt),
		CreatedAt:       parseTime(w.CreatedAt),
	}
	if info.Metadata == nil {
		info.Metadata = map[string]string{}
	}
	return info, nil
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

// normalizeStatus lowercases a camelCase lifecycle state, inserting "_" at each
// lower/digit→upper boundary — equivalent to the Ruby gsub(/([a-z\d])([A-Z])/,'\1_\2').downcase.
// "Running" → "running"; "creatingPod" → "creating_pod"; "PausedByUser" → "paused_by_user".
func normalizeStatus(state string) string {
	if state == "" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(state); i++ {
		c := state[i]
		if i > 0 && c >= 'A' && c <= 'Z' {
			p := state[i-1]
			if (p >= 'a' && p <= 'z') || (p >= '0' && p <= '9') {
				b.WriteByte('_')
			}
		}
		b.WriteByte(c)
	}
	return strings.ToLower(b.String())
}
