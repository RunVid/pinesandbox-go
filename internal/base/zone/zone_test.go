package zone

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// zoneVectors mirrors sdks/pine-computer/contract/zone-vectors.json — the language-
// neutral contract the Ruby zone spec and this SDK both assert against.
type zoneVectors struct {
	Valid []struct {
		Input       string            `json:"input"`
		ControlHost string            `json:"control_host"`
		HTTPScheme  string            `json:"http_scheme"`
		DataHosts   map[string]string `json:"data_hosts"`
	} `json:"valid"`
	InvalidEndpoints []struct {
		Input  string `json:"input"`
		Reason string `json:"reason"`
	} `json:"invalid_endpoints"`
	InvalidSandboxIDs []string `json:"invalid_sandbox_ids"`
}

func loadVectors(t *testing.T) zoneVectors {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "zone-vectors.json"))
	if err != nil {
		t.Fatalf("read testdata zone-vectors: %v", err)
	}
	var v zoneVectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode zone-vectors: %v", err)
	}
	if len(v.Valid) == 0 || len(v.InvalidEndpoints) == 0 || len(v.InvalidSandboxIDs) == 0 {
		t.Fatal("zone-vectors empty — testdata may be stale")
	}
	return v
}

// TestZone_Vectors drives the full zone-vectors.json contract: valid endpoints derive
// the documented control/data hosts + scheme; invalid endpoints and invalid sandbox ids
// are rejected. A drift here is a host-derivation bug that would point the SDK at the
// wrong gateway.
func TestZone_Vectors(t *testing.T) {
	v := loadVectors(t)

	for _, c := range v.Valid {
		t.Run("valid/"+c.Input, func(t *testing.T) {
			z, err := Parse(c.Input)
			if err != nil {
				t.Fatalf("Parse(%q) errored: %v", c.Input, err)
			}
			if got := z.ControlHost(); got != c.ControlHost {
				t.Errorf("ControlHost = %q, want %q", got, c.ControlHost)
			}
			if got := z.HTTPScheme(); got != c.HTTPScheme {
				t.Errorf("HTTPScheme = %q, want %q", got, c.HTTPScheme)
			}
			for id, want := range c.DataHosts {
				got, err := z.DataHost(id)
				if err != nil {
					t.Fatalf("DataHost(%q) errored: %v", id, err)
				}
				if got != want {
					t.Errorf("DataHost(%q) = %q, want %q", id, got, want)
				}
			}
		})
	}

	for _, c := range v.InvalidEndpoints {
		t.Run("invalid_endpoint/"+c.Input, func(t *testing.T) {
			if _, err := Parse(c.Input); err == nil {
				t.Errorf("Parse(%q) should be rejected (%s), got nil", c.Input, c.Reason)
			}
		})
	}

	// A valid zone must reject every invalid sandbox id.
	z, err := Parse("staging.pinesandbox.io")
	if err != nil {
		t.Fatalf("setup zone: %v", err)
	}
	for _, id := range v.InvalidSandboxIDs {
		if _, err := z.DataHost(id); err == nil {
			t.Errorf("DataHost(%q) should be rejected (invalid id), got nil", id)
		}
	}
}

// TestZone_VectorsMatchCanonical guards the module-local testdata against drift from the
// canonical contract artifact. The published mirror ships only the local copy (the
// canonical lives outside the Go subtree), so this runs only in the monorepo and skips
// elsewhere — the contract-discipline backstop, per design §9.1.
func TestZone_VectorsMatchCanonical(t *testing.T) {
	canonical := filepath.Join("..", "..", "..", "..", "contract", "zone-vectors.json")
	want, err := os.ReadFile(canonical)
	if err != nil {
		t.Skipf("canonical artifact not present (mirror build): %v", err)
	}
	got, err := os.ReadFile(filepath.Join("testdata", "zone-vectors.json"))
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("testdata/zone-vectors.json drifted from %s — re-copy the canonical artifact", canonical)
	}
}
