package pinesandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// TestContractCopiesMatchCanonical guards the module-local copies of shared
// contract files: the vectors in testdata/ and the tables the SDK embeds. The
// published mirror ships only the module (the canonical files live outside it),
// so this runs in the monorepo and skips in a mirror build.
func TestContractCopiesMatchCanonical(t *testing.T) {
	for _, c := range []struct{ name, local string }{
		{"result-artifacts.json", filepath.Join("internal", "coordinator", "testdata", "result-artifacts.json")},
		{"credentials-vectors.json", filepath.Join("testdata", "credentials-vectors.json")},
		{"binding-vectors.json", filepath.Join("testdata", "binding-vectors.json")},
		{"hpke-vectors.json", filepath.Join("testdata", "hpke-vectors.json")},
		{"artifact-content-types.json", filepath.Join("internal", "artifacts", "artifact-content-types.json")},
	} {
		t.Run(c.name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("..", "contract", c.name))
			if err != nil {
				t.Skipf("canonical artifact not present (mirror build): %v", err)
			}
			got, err := os.ReadFile(c.local)
			if err != nil {
				t.Fatalf("read the copy: %v", err)
			}
			if string(got) != string(want) {
				t.Fatalf("%s drifted from contract/%s — copy the canonical file", c.local, c.name)
			}
		})
	}
}
