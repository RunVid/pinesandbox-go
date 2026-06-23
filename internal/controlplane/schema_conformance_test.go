package controlplane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestSandboxWireConformsToSpec checks the control-plane sandbox wire type's JSON fields are
// a subset of the sandbox-lifecycle Sandbox schema (the wire-axis drift gate for this layer).
// Skips on the mirror (the schema artifact isn't published with the SDK).
func TestSandboxWireConformsToSpec(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "computer-schemas.json"))
	if err != nil {
		t.Skipf("schema artifact not present (mirror build): %v", err)
	}
	var schemas map[string][]string
	if err := json.Unmarshal(b, &schemas); err != nil {
		t.Fatalf("parse schema artifact: %v", err)
	}
	props, ok := schemas["Sandbox"]
	if !ok {
		t.Fatal("spec schema \"Sandbox\" not found in artifact")
	}
	set := make(map[string]bool, len(props))
	for _, p := range props {
		set[p] = true
	}

	typ := reflect.TypeOf(sandboxWire{})
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" {
			continue
		}
		name := strings.SplitN(tag, ",", 2)[0]
		if name == "" || name == "-" {
			continue
		}
		if !set[name] {
			t.Errorf("sandboxWire: json field %q is not a property of the spec Sandbox schema (props: %v)", name, props)
		}
	}
}
