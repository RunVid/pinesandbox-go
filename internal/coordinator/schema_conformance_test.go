package coordinator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loadSpecSchemas reads the generated schema→properties artifact (skip on the mirror).
func loadSpecSchemas(t *testing.T) map[string][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "computer-schemas.json"))
	if err != nil {
		t.Skipf("schema artifact not present (mirror build): %v", err)
	}
	var m map[string][]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse schema artifact: %v", err)
	}
	return m
}

// jsonFieldNames returns the wire field names of a struct (the part before the comma in each
// json tag; skips "-" and untagged fields).
func jsonFieldNames(typ reflect.Type) []string {
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" {
			continue
		}
		name := strings.SplitN(tag, ",", 2)[0]
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// assertSubset checks every wire field exists in the named spec schema.
func assertSubset(t *testing.T, label string, fields []string, schemaName string, schemas map[string][]string) {
	t.Helper()
	props, ok := schemas[schemaName]
	if !ok {
		t.Fatalf("%s: spec schema %q not found in artifact (renamed/removed?)", label, schemaName)
	}
	set := make(map[string]bool, len(props))
	for _, p := range props {
		set[p] = true
	}
	for _, f := range fields {
		if !set[f] {
			t.Errorf("%s: json field %q is not a property of spec schema %q (props: %v)", label, f, schemaName, props)
		}
	}
}

// TestSchemaConformance is the wire-axis drift gate (the codegen replacement): each wire DTO's
// JSON fields must be a subset of its spec component schema's properties. A spec field rename
// or removal the SDK doesn't follow — or a typo'd json tag — fails here. Maps the SDK's named
// wire types to their spec schemas (nested/inline shapes like browser are covered transitively
// via the parent's property name).
func TestSchemaConformance(t *testing.T) {
	schemas := loadSpecSchemas(t)
	cases := []struct {
		v      any
		schema string
	}{
		{sessionWire{}, "Session"},
		{bindPubkeyWire{}, "BindPubkey"},
		{fileEntryWire{}, "FileEntry"},
		{artifactWire{}, "Artifact"},
	}
	for _, c := range cases {
		typ := reflect.TypeOf(c.v)
		assertSubset(t, fmt.Sprintf("%T", c.v), jsonFieldNames(typ), c.schema, schemas)
	}
}
