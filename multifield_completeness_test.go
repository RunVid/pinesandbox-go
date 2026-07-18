package pinesandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestMultiFieldResponses_ConsciouslyHandled is the completeness gate the subset
// schema-gate can't be: it checks the SDK doesn't SILENTLY extract one field of a
// multi-field response and drop the rest (the next_before class of bug).
//
// specs/gen-multifield-responses.py discovers every coordinator 200 response with
// ≥2 top-level fields → contract/multifield-responses.json. This test asserts each
// one is CONSCIOUSLY handled by this SDK, recorded in `handled` below as:
//   - modeled:    returned as a typed DTO (the fields are surfaced)
//   - raw:        returned as the FULL json.RawMessage body (nothing dropped)
//   - flattened:  a subset is returned on purpose; the reason names what's dropped
//     and why (a reviewer can challenge it — that's the point)
//
// A NEW multi-field response, or a field added to a single-field one, fails here
// until someone decides — so a silent drop can't ship. (Limitation: it does not
// verify a "modeled" DTO is itself field-complete — that stays a review concern.)
func TestMultiFieldResponses_ConsciouslyHandled(t *testing.T) {
	handled := map[string]string{
		// modeled — typed DTOs that surface the fields
		"getControl":       "modeled: ControlState (all 6 fields, no Raw)",
		"patchControl":     "modeled: ControlState",
		"getSession":       "modeled: SessionInfo",
		"getHandoff":       "modeled: Handoff summary + full forensic body in .Raw",
		"listHandoffs":     "modeled: HandoffList{Handoffs, NextBefore}",
		"mintDesktopToken": "modeled: DesktopToken{Token, ExpiresAt}",
		"observeSession":   "modeled: Observation (typed observe result)",
		"getAgent":         "modeled: AgentTask (typed envelope + .Raw)",
		"resetAgentThread": "modeled: AgentTask",
		"getAgentResult":   "modeled: AgentResult (typed + .Raw)",

		// raw — the method returns the FULL response body as json.RawMessage
		"getSkill":           "raw: skill body json.RawMessage (full body)",
		"getSkillVersion":    "raw: skill-version json.RawMessage (full body)",
		"activateSkill":      "raw: skill-admin json.RawMessage (full body)",
		"deactivateSkill":    "raw: skill-admin json.RawMessage (full body)",
		"deleteSkillVersion": "raw: skill-admin json.RawMessage (full body)",
		"captureCheckpoint":  "raw: Capture json.RawMessage (full body)",
		"uploadFile":         "raw: UploadFile json.RawMessage (full body)",
		"sessionEpoch":       "raw: Epoch json.RawMessage (full body)",

		// not exposed — the endpoint is deliberately absent from this SDK
		"listAgentModelPresets": "not exposed (deliberate): internal testing surface — the " +
			"validated resident model-preset catalog for the portal playground; spec-marked " +
			"not part of the stable Computer contract. Add a typed catalog + " +
			"AgentRunOptions.ModelPreset here if a Go integrator ever needs model selection.",
		"getAgentBrowserFlags": "not exposed (deliberate): internal staff-only playground " +
			"testing surface — the session's humanize/fill-commit browser flags for the portal " +
			"admin playground; spec-marked x-internal, not part of the stable Computer contract. " +
			"No Go integrator needs it.",
		"setAgentBrowserFlags": "not exposed (deliberate): internal staff-only playground " +
			"testing surface — the pre-first-run browser-flags config; see getAgentBrowserFlags.",
		"getFileViewMetadata": "not exposed (deliberate): capability-bound loopback surface " +
			"for the image-owned browser extension, not a public Computer SDK operation",

		// flattened — a subset is returned on purpose (low-value metadata dropped)
		"listSkills": "flattened: returns the skills array; generated_at is manifest " +
			"metadata not worth a wrapper DTO (read it via GetSkill if needed)",
		"tabsList": "flattened: returns []Tab; api_version + *_generation are snapshot-" +
			"staleness metadata — not surfaced (revisit if stale-tab detection is needed)",
	}

	raw, err := os.ReadFile(filepath.Join("..", "contract", "multifield-responses.json"))
	if err != nil {
		t.Skipf("contract artifact not present (mirror-only build): %v", err)
	}
	var discovered map[string][]string
	if err := json.Unmarshal(raw, &discovered); err != nil {
		t.Fatalf("parse multifield-responses.json: %v", err)
	}

	for op, props := range discovered {
		if _, ok := handled[op]; !ok {
			t.Errorf("multi-field response %q %v is NOT accounted for — model it, return "+
				"the full body raw, or add a `flattened: <reason>` note in this test (don't "+
				"silently extract one field and drop the rest)", op, props)
		}
	}
	for op := range handled {
		if _, ok := discovered[op]; !ok {
			t.Errorf("stale `handled` entry %q — no longer a multi-field response; remove it", op)
		}
	}
}
