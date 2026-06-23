package controlplane

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SandboxInfo is the parsed control-plane view of a sandbox (the Computer's pod). Mirrors
// the Ruby OpenSandbox::SandboxInfo surface: id, image URI, normalized status, metadata,
// entrypoint, and the two timestamps. The data-plane URL is intentionally NOT carried here
// — the lifecycle server owns provisioning, not deployment topology (the zone resolver
// owns addressing).
type SandboxInfo struct {
	ID         string
	Image      string // the image URI (present only in GET/LIST responses)
	Status     string // normalized lifecycle state (status.state, snake_cased + lowercased)
	Metadata   map[string]string
	Entrypoint []string
	ExpiresAt  *time.Time
	CreatedAt  *time.Time
}

// sandboxWire is the nested server JSON; SandboxInfo flattens it.
type sandboxWire struct {
	ID    string `json:"id"`
	Image struct {
		URI string `json:"uri"`
	} `json:"image"`
	Status struct {
		State string `json:"state"`
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
		return nil, fmt.Errorf("pinesandbox: control plane returned an unparseable sandbox: %w", err)
	}
	info := &SandboxInfo{
		ID:         w.ID,
		Image:      w.Image.URI,
		Status:     normalizeStatus(w.Status.State),
		Metadata:   w.Metadata,
		Entrypoint: w.Entrypoint,
		ExpiresAt:  parseTime(w.ExpiresAt),
		CreatedAt:  parseTime(w.CreatedAt),
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
