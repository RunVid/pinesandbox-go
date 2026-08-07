package tokens

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func loadSpecSchemas(t *testing.T) map[string][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "computer-schemas.json"))
	if err != nil {
		t.Skipf("schema artifact not present (mirror build): %v", err)
	}
	var schemas map[string][]string
	if err := json.Unmarshal(b, &schemas); err != nil {
		t.Fatalf("parse schema artifact: %v", err)
	}
	return schemas
}

func loadSpecRequired(t *testing.T) map[string][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "computer-schema-required.json"))
	if err != nil {
		t.Skipf("required-field artifact not present (mirror build): %v", err)
	}
	var schemas map[string][]string
	if err := json.Unmarshal(b, &schemas); err != nil {
		t.Fatalf("parse required-field artifact: %v", err)
	}
	return schemas
}

func jsonFields(typ reflect.Type) []string {
	fields := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		name := strings.SplitN(typ.Field(i).Tag.Get("json"), ",", 2)[0]
		if name != "" && name != "-" {
			fields = append(fields, name)
		}
	}
	slices.Sort(fields)
	return fields
}

func nonOmitEmptyJSONFields(typ reflect.Type) []string {
	fields := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		parts := strings.Split(typ.Field(i).Tag.Get("json"), ",")
		if parts[0] == "" || parts[0] == "-" || slices.Contains(parts[1:], "omitempty") {
			continue
		}
		fields = append(fields, parts[0])
	}
	slices.Sort(fields)
	return fields
}

// The Portal attach transport is SDK-owned end to end, so use equality rather
// than the weaker subset check used by forward-compatible response DTOs. This
// is the gate the initial regional-egress work was missing: adding an optional
// Portal attach field now forces an explicit Go SDK transport decision.
func TestAttachCredentialWireExactlyMatchesPortalSpec(t *testing.T) {
	schemas := loadSpecSchemas(t)
	for _, tc := range []struct {
		wire   any
		schema string
	}{
		{credentialsRequestWire{}, "AttachCredentialsRequest"},
		{attachCredentialsWire{}, "AttachCredentials"},
		{ComputerLocation{}, "ComputerLocation"},
		{AvailableLocations{}, "AvailableLocations"},
	} {
		want, ok := schemas[tc.schema]
		if !ok {
			t.Fatalf("spec schema %q not found in artifact", tc.schema)
		}
		slices.Sort(want)
		got := jsonFields(reflect.TypeOf(tc.wire))
		if !slices.Equal(got, want) {
			t.Errorf("%T fields = %v, want exact %s properties %v", tc.wire, got, tc.schema, want)
		}
	}
}

// Requiredness is a separate contract axis from property names. Deriving
// request requiredness from omitempty makes a spec-only required-list edit fail
// this SDK's CI until the transport decision is updated deliberately.
func TestAttachCredentialRequirednessMatchesPortalSpec(t *testing.T) {
	required := loadSpecRequired(t)
	got := nonOmitEmptyJSONFields(reflect.TypeOf(credentialsRequestWire{}))
	want := required["AttachCredentialsRequest"]
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("credentialsRequestWire required fields = %v, want spec %v", got, want)
	}

	// Response structs are decode-only, so omitempty cannot model what the
	// client expects the server to commit. Keep that decision explicit.
	got = []string{
		"bind_token",
		"bind_token_expires_at",
		"binding_revision",
		"broker_grant",
		"broker_grant_expires_at",
		"location",
	}
	want = required["AttachCredentials"]
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("attach credential response required fields = %v, want spec %v", got, want)
	}

	got = nonOmitEmptyJSONFields(reflect.TypeOf(AvailableLocations{}))
	want = required["AvailableLocations"]
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("available locations required fields = %v, want spec %v", got, want)
	}
}
