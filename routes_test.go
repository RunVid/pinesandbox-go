package pinesandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sdkRoutes is every (METHOD, path-template) the SDK calls across its layers — control
// plane, portal attach-credentials, and the coordinator data plane. CG-4: each must exist
// in the spec (checked by TestRoutes_ConformToSpec against the generated route set, so a
// spec rename/removal of a route the SDK depends on fails CI). The per-method httptest tests
// pin that the methods actually send these paths; this list pins them to the contract.
var sdkRoutes = []string{
	// control plane (sandbox-lifecycle)
	"POST /sandboxes",
	"GET /sandboxes/{id}",
	"DELETE /sandboxes/{id}",
	"POST /sandboxes/{id}/pause",
	"POST /sandboxes/{id}/resume",
	// portal (control token + attach credentials)
	"POST /v1/control-token",
	"POST /v1/computers",
	"POST /v1/computers/{id}/attach-credentials",
	// coordinator: bind + admin
	"GET /v1/coord/bind-pubkey",
	"POST /v1/coord/bind",
	"POST /v1/coord/capture",
	"GET /health",
	"GET /metrics",
	"GET /state",
	"GET /downloads/orphans",
	"POST /downloads/orphans/{guid}/claim",
	"DELETE /downloads/orphans/{guid}",
	// coordinator: session lifecycle
	"POST /sessions",
	"GET /sessions",
	"GET /sessions/{name}",
	"DELETE /sessions/{name}",
	"POST /sessions/{name}/terminal/recreate",
	"POST /sessions/{name}/focus",
	"GET /sessions/{name}/epoch",
	// coordinator: tabs
	"GET /sessions/{name}/tabs",
	"POST /sessions/{name}/tabs",
	"PATCH /sessions/{name}/tabs/{target_id}",
	"DELETE /sessions/{name}/tabs/{target_id}",
	// coordinator: exec
	"POST /sessions/{name}/exec",
	// coordinator: files + artifacts
	"GET /v1/sessions/{name}/files/list",
	"GET /v1/sessions/{name}/files",
	"PUT /v1/sessions/{name}/files",
	"GET /v1/sessions/{name}/artifacts",
	"GET /v1/sessions/{name}/artifacts/{id}",
	"GET /v1/sessions/{name}/artifacts/zip",
	"POST /v1/sessions/{name}/artifacts",
	// coordinator: control + handoffs
	"GET /v1/sessions/{name}/control",
	"PATCH /v1/sessions/{name}/control",
	"POST /v1/sessions/{name}/control/notify",
	"GET /v1/sessions/{name}/control/events",
	"POST /v1/sessions/{name}/desktop-token",
	"GET /v1/sessions/{name}/handoffs",
	"GET /v1/sessions/{name}/handoffs/{handoff_id}",
	// coordinator: agent
	"POST /v1/sessions/{name}/agent/run",
	"POST /v1/sessions/{name}/agent/steer",
	"POST /v1/sessions/{name}/agent/answer",
	"POST /v1/sessions/{name}/agent/cancel",
	"POST /v1/sessions/{name}/agent/reset",
	"GET /v1/sessions/{name}/agent",
	"GET /v1/sessions/{name}/agent/result",
	"GET /v1/sessions/{name}/agent/events",
	// coordinator: drive
	"POST /v1/sessions/{name}/observe",
	"POST /v1/sessions/{name}/computer-use",
	"POST /v1/sessions/{name}/upload_file",
	// coordinator: skills (served + authoring)
	"GET /v1/skills",
	"GET /v1/skills/{name}",
	"GET /v1/skills/drafts",
	"GET /v1/skills/versions",
	"GET /v1/skills/{name}/versions",
	"GET /v1/skills/{name}/versions/{version}",
	"POST /v1/skills/{name}/activate",
	"POST /v1/skills/{name}/deactivate",
	"DELETE /v1/skills/{name}/versions/{version}",
	"POST /v1/sessions/{name}/learn",
	"POST /v1/sessions/{name}/teach",
	"POST /v1/sessions/{name}/refine",
	"POST /v1/sessions/{name}/skills",
	"GET /v1/sessions/{name}/skills/author/{author_id}/events",
	"POST /v1/sessions/{name}/skills/author/{author_id}/cancel",
}

var routeParamRE = regexp.MustCompile(`\{[^}]+\}`)

func normalizeRoute(r string) string {
	parts := strings.SplitN(r, " ", 2)
	if len(parts) != 2 {
		return r
	}
	return parts[0] + " " + routeParamRE.ReplaceAllString(parts[1], "{}")
}

// TestRoutes_ConformToSpec is the CG-4 route gate: every route the SDK calls must exist in
// the spec-derived route set (specs/gen-computer-routes.py → contract/computer-routes.json,
// param-normalized). A spec that renames or removes a route the SDK depends on fails here.
// Skips on the mirror (the contract artifact isn't published with the SDK).
func TestRoutes_ConformToSpec(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "contract", "computer-routes.json"))
	if err != nil {
		t.Skipf("route artifact not present (mirror build): %v", err)
	}
	var specRoutes []string
	if err := json.Unmarshal(b, &specRoutes); err != nil {
		t.Fatalf("parse route artifact: %v", err)
	}
	spec := make(map[string]bool, len(specRoutes))
	for _, r := range specRoutes {
		spec[r] = true
	}

	var missing []string
	for _, r := range sdkRoutes {
		if norm := normalizeRoute(r); !spec[norm] {
			missing = append(missing, r+"  (normalized "+norm+")")
		}
	}
	if len(missing) > 0 {
		t.Fatalf("CG-4: %d SDK route(s) not found in the spec — regenerate the artifact or fix the drift:\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
}

// TestRoutes_NoDuplicates guards the declared list against accidental copy-paste dupes.
func TestRoutes_NoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range sdkRoutes {
		if seen[r] {
			t.Errorf("duplicate route in sdkRoutes: %s", r)
		}
		seen[r] = true
	}
}
