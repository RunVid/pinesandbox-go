package pinesandbox

import (
	"os/exec"
	"strings"
	"testing"
)

// TestArch_BaseDoesNotImportDomain enforces the §3 boundary: internal/base/* is the
// generic, Computer-agnostic layer (the Ruby-rewrite blueprint) and must NEVER depend on
// a domain package (internal/bindhpke, and later internal/coordinator / controlplane /
// tokens). A violation would couple the base to Computer-specific code and break the
// "base graduates to a published generic module" plan. Driven by the real import graph.
func TestArch_BaseDoesNotImportDomain(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "go.pinesandbox.io/computer/internal/base/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	const prefix = "go.pinesandbox.io/computer/internal/"
	var violations []string
	for _, dep := range strings.Fields(string(out)) {
		if !strings.HasPrefix(dep, prefix) {
			continue // stdlib / third-party
		}
		if !strings.HasPrefix(dep, prefix+"base/") {
			violations = append(violations, dep)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("internal/base imports non-base internal packages (base ↛ domain, §3): %v", violations)
	}
}
