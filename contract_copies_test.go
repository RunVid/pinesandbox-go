package pinesandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// TestContractCopiesMatchCanonical guards the module-local copies of shared
// contract vectors. The published mirror ships only testdata/ (the canonical
// files live outside the Go module), so this runs in the monorepo and skips in
// a mirror build.
func TestContractCopiesMatchCanonical(t *testing.T) {
	for _, name := range []string{"credentials-vectors.json", "binding-vectors.json", "hpke-vectors.json"} {
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("..", "contract", name))
			if err != nil {
				t.Skipf("canonical artifact not present (mirror build): %v", err)
			}
			got, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatalf("read testdata: %v", err)
			}
			if string(got) != string(want) {
				t.Fatalf("testdata/%s drifted from contract/%s — copy the canonical file", name, name)
			}
		})
	}
}
