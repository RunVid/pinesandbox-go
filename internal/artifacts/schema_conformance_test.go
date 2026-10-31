package artifacts

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func loadContract(t *testing.T, name string) map[string][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", name))
	if err != nil {
		t.Skipf("contract artifact not present (mirror build): %v", err)
	}
	var schemas map[string][]string
	if err := json.Unmarshal(b, &schemas); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return schemas
}

// TestTokenWireMatchesPortalSpec: the token wire struct has exactly the ArtifactToken
// properties, so a Portal field added or renamed is an explicit SDK decision.
func TestTokenWireMatchesPortalSpec(t *testing.T) {
	want := loadContract(t, "computer-schemas.json")["ArtifactToken"]
	if want == nil {
		t.Fatal("spec schema ArtifactToken not found in artifact")
	}
	var got []string
	typ := reflect.TypeOf(tokenWire{})
	for i := 0; i < typ.NumField(); i++ {
		got = append(got, strings.SplitN(typ.Field(i).Tag.Get("json"), ",", 2)[0])
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("tokenWire fields = %v, want exact ArtifactToken properties %v", got, want)
	}
}

// TestTokenRequiredFieldsMatchPortalSpec: parseToken refuses a response missing exactly
// the fields the spec requires, and accepts one missing only the optional copy_from.
func TestTokenRequiredFieldsMatchPortalSpec(t *testing.T) {
	required := loadContract(t, "computer-schema-required.json")["ArtifactToken"]
	full := map[string]any{
		"access_token": "ya29.x", "expires_at": "2026-10-22T12:30:00Z", "endpoint": "https://storage.googleapis.com",
		"bucket": "bkt", "prefix": "c/artifacts/", "writable": true, "used_bytes": 0, "limit_bytes": 1,
		"copy_from": map[string]string{"c2": "c2/artifacts/"},
	}
	for field := range full {
		body := map[string]any{}
		for k, v := range full {
			if k != field {
				body[k] = v
			}
		}
		b, _ := json.Marshal(body)
		_, err := parseToken(b)
		if isRequired := slices.Contains(required, field); (err != nil) != isRequired {
			t.Errorf("without %s: err = %v, want refused = %v", field, err, isRequired)
		}
	}
}

func TestEscapeEncodesEverythingButUnreserved(t *testing.T) {
	for in, want := range map[string]string{
		"c/artifacts/reports/q3 final.txt": "c%2Fartifacts%2Freports%2Fq3%20final.txt",
		"a+b:c@d=e&f?g#h%i":                "a%2Bb%3Ac%40d%3De%26f%3Fg%23h%25i",
		"Az09-._~":                         "Az09-._~",
		"é":                                "%C3%A9",
	} {
		if got := escape(in); got != want {
			t.Errorf("escape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResumeHeaders(t *testing.T) {
	for header, want := range map[string]int64{"bytes=0-3": 4, "": 0, "bytes=0-0": 1, "nonsense": 0} {
		if got := keptBytes(header); got != want {
			t.Errorf("keptBytes(%q) = %d, want %d", header, got, want)
		}
	}
	if start, total, ok := contentRange("bytes 5-10/11"); !ok || start != 5 || total != 11 {
		t.Errorf("contentRange = %d %d %v", start, total, ok)
	}
	if start, total, ok := contentRange("bytes 5-10/*"); !ok || start != 5 || total != -1 {
		t.Errorf("contentRange unknown total = %d %d %v", start, total, ok)
	}
	if _, _, ok := contentRange("5-10"); ok {
		t.Error("contentRange accepted a header without a total")
	}
}

// TestCheckPathFollowsTheSharedRule runs every vector of contract/artifact-paths.json,
// the path rule every SDK, pine-artifact and the state broker apply.
func TestCheckPathFollowsTheSharedRule(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "artifact-paths.json"))
	if err != nil {
		t.Skipf("contract artifact not present (mirror build): %v", err)
	}
	var vectors struct {
		MaxBytes     int      `json:"max_bytes"`
		Valid        []string `json:"valid"`
		Invalid      []string `json:"invalid"`
		InvalidBytes []struct {
			Hex string `json:"hex"`
			Why string `json:"why"`
		} `json:"invalid_bytes"`
		Length struct {
			ValidBytes   int `json:"valid_bytes"`
			InvalidBytes int `json:"invalid_bytes"`
		} `json:"length"`
	}
	if err := json.Unmarshal(b, &vectors); err != nil {
		t.Fatal(err)
	}
	if vectors.MaxBytes != maxPathBytes || len(vectors.Valid) == 0 || len(vectors.Invalid) == 0 || len(vectors.InvalidBytes) == 0 {
		t.Fatalf("vectors max_bytes %d (SDK %d), %d valid, %d invalid, %d invalid_bytes",
			vectors.MaxBytes, maxPathBytes, len(vectors.Valid), len(vectors.Invalid), len(vectors.InvalidBytes))
	}
	valid := append(vectors.Valid, strings.Repeat("a", vectors.Length.ValidBytes))
	invalid := append(vectors.Invalid, strings.Repeat("a", vectors.Length.InvalidBytes))
	for _, c := range vectors.InvalidBytes {
		raw, err := hex.DecodeString(c.Hex)
		if err != nil {
			t.Fatalf("invalid_bytes %q: %v", c.Hex, err)
		}
		invalid = append(invalid, string(raw))
	}
	for _, p := range valid {
		if err := CheckPath(p); err != nil {
			t.Errorf("CheckPath(%q) = %v, want it allowed", p, err)
		}
	}
	for _, p := range invalid {
		if err := CheckPath(p); err == nil {
			t.Errorf("CheckPath(%q) allowed it, want it refused", p)
		}
	}
}

// TestDefaultContentTypePassesTheSharedCases runs every case of the embedded
// artifact-content-types.json, which the contract-copy test holds equal to the canonical
// table every server SDK carries.
func TestDefaultContentTypePassesTheSharedCases(t *testing.T) {
	var table struct {
		Cases []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(contentTypesJSON, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Cases) == 0 || len(contentTypes.Types) == 0 || contentTypes.Default == "" {
		t.Fatalf("table has %d cases, %d types and default %q", len(table.Cases), len(contentTypes.Types), contentTypes.Default)
	}
	for _, c := range table.Cases {
		if got := defaultContentType(c.Path); got != c.Type {
			t.Errorf("defaultContentType(%q) = %q, want %q", c.Path, got, c.Type)
		}
	}
}
