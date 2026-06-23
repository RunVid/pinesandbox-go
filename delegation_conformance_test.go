package pinesandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestDelegation_ConformsToContract is the cross-SDK DelegatedConnection parity gate (beta
// design 0D). The canonical envelope lives in contract/delegation.json — the SAME file the
// Ruby + web SDKs assert against. This pins that the Go server SDK serializes the documented
// `input` to the documented `wire` (computer_host a FULL URI, session reduced to {name}); a
// drift in any SDK's envelope fails its own conformance test here / in the others.
// Skips on the mirror (the contract artifact isn't published with the SDK).
func TestDelegation_ConformsToContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "contract", "delegation.json"))
	if err != nil {
		t.Skipf("delegation artifact not present (mirror build): %v", err)
	}
	var vec struct {
		Input struct {
			ComputerHost          string `json:"computer_host"`
			SessionName           string `json:"session_name"`
			SpecVersion           int    `json:"spec_version"`
			DesktopToken          string `json:"desktop_token"`
			DesktopTokenExpiresAt string `json:"desktop_token_expires_at"`
		} `json:"input"`
		Wire json.RawMessage `json:"wire"`
	}
	if err := json.Unmarshal(b, &vec); err != nil {
		t.Fatalf("parse delegation artifact: %v", err)
	}

	dc := DelegatedConnection{
		ComputerHost:          vec.Input.ComputerHost,
		SessionName:           vec.Input.SessionName,
		SpecVersion:           vec.Input.SpecVersion,
		DesktopToken:          vec.Input.DesktopToken,
		DesktopTokenExpiresAt: vec.Input.DesktopTokenExpiresAt,
	}
	got, err := json.Marshal(dc)
	if err != nil {
		t.Fatalf("marshal DelegatedConnection: %v", err)
	}

	// Compare canonically (re-marshal both through map[string]any so key order / whitespace
	// don't matter — only the shape + values).
	if g, w := canonical(t, got), canonical(t, vec.Wire); g != w {
		t.Fatalf("0D: Go DelegatedConnection.MarshalJSON drifted from contract/delegation.json\n got:  %s\n want: %s", g, w)
	}
}

func canonical(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	out, err := json.Marshal(v) // Go sorts map keys deterministically
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	return string(out)
}
