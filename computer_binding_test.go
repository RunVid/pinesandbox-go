package pinesandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBindingSharedStorageContract(t *testing.T) {
	data, err := os.ReadFile("testdata/binding-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct{ Valid, Legacy, Invalid []json.RawMessage }
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, wire := range vectors.Valid {
		var binding Binding
		if err := json.Unmarshal(wire, &binding); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(binding)
		if err != nil {
			t.Fatal(err)
		}
		var roundtrip Binding
		if err := json.Unmarshal(encoded, &roundtrip); err != nil || binding != roundtrip {
			t.Fatal("binding changed through storage")
		}
		if !reflect.DeepEqual(jsonObject(t, encoded), jsonObject(t, wire)) {
			t.Fatalf("binding wrote %s, want %s", encoded, wire)
		}
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, binding), binding.ComputerToken) {
				t.Fatal("normal formatting exposed the token")
			}
		}
		for _, value := range []any{binding, &binding} {
			var output bytes.Buffer
			slog.New(slog.NewJSONHandler(&output, nil)).Info("reconnected", "binding", value)
			if strings.Contains(output.String(), binding.ComputerToken) || !strings.Contains(output.String(), "[redacted]") {
				t.Fatal("structured logging failed to redact binding")
			}
		}
	}
	// A schema-1 binding still loads and is written back as schema 2, with its
	// sandbox id under `sandbox_id` instead of `runtime_id`.
	for _, wire := range vectors.Legacy {
		var binding Binding
		if err := json.Unmarshal(wire, &binding); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(binding)
		if err != nil {
			t.Fatal(err)
		}
		want := jsonObject(t, wire)
		want["schema"], want["sandbox_id"] = json.Number("2"), want["runtime_id"]
		delete(want, "runtime_id")
		if got := jsonObject(t, encoded); !reflect.DeepEqual(got, want) {
			t.Fatalf("legacy binding wrote %s, want %v", encoded, want)
		}
	}
	for _, wire := range append(vectors.Invalid, json.RawMessage(`{"schema":`)) {
		original := Binding{ComputerID: "original"}
		binding := original
		if err := json.Unmarshal(wire, &binding); err == nil {
			t.Fatalf("accepted invalid binding: %s", wire)
		}
		if binding != original {
			t.Fatal("invalid import overwrote the saved binding")
		}
	}
}

func TestReconnectPreservesCredentialsWithoutNetworkIO(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		t.Run(fmt.Sprint(ephemeral), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("reconnect made unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(500)
			}))
			defer server.Close()
			client := &Client{conn: buildTestConnection(t, server.URL, server.URL)}
			data, err := os.ReadFile("testdata/credentials-vectors.json")
			if err != nil {
				t.Fatal(err)
			}
			var vectors struct{ Valid []json.RawMessage }
			if err := json.Unmarshal(data, &vectors); err != nil {
				t.Fatal(err)
			}
			var creds Credentials
			if err := json.Unmarshal(vectors.Valid[0], &creds); err != nil {
				t.Fatal(err)
			}
			creds.Ephemeral = ephemeral
			binding := Binding{creds.ID, "saved-runtime", "ct_saved", 7}
			comp, err := client.ReconnectComputer(context.Background(), creds, binding)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(comp.Credentials(), creds) {
				t.Fatal("reconnect lost current or retained keys")
			}
			saved, err := json.Marshal(comp.Credentials())
			if err != nil {
				t.Fatal(err)
			}
			got, err := comp.Binding()
			if err != nil || got != binding {
				t.Fatal("reconnect changed the saved generation")
			}
			creds.Key[0] ^= 1
			creds.PriorKeys[0][0] ^= 1
			creds.CaptureKeypair.SK[0] ^= 1
			after, err := json.Marshal(comp.Credentials())
			if err != nil || string(after) != string(saved) {
				t.Fatal("reconnect retained caller-owned key slices")
			}
			other, _ := GenerateCredentials()
			if _, err := client.ReconnectComputer(context.Background(), other, binding); err == nil {
				t.Fatal("mixed identities accepted")
			}
		})
	}
}

func TestBindingRequiresBoundSandbox(t *testing.T) {
	comp := newComputer("0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000", make([]byte, 32))
	if _, err := comp.Binding(); !errors.Is(err, ErrNotBound) {
		t.Fatalf("unattached Computer exported a binding: %v", err)
	}
	if _, err := comp.SandboxDetails(context.Background()); !errors.Is(err, ErrNotBound) {
		t.Fatalf("unattached Computer returned sandbox details: %v", err)
	}
	if _, err := comp.SetIdleTimeout(context.Background(), time.Minute); !errors.Is(err, ErrNotBound) {
		t.Fatalf("unattached Computer edited its idle timeout: %v", err)
	}
	if _, err := comp.Status(context.Background()); !errors.Is(err, ErrNotBound) {
		t.Fatalf("unattached Computer read its status: %v", err)
	}
	if _, err := comp.CreateSession(context.Background(), CreateSessionOptions{}); !errors.Is(err, ErrNotBound) {
		t.Fatalf("unattached Computer created a session: %v", err)
	}
	if err := comp.Pause(context.Background()); !errors.Is(err, ErrNotBound) {
		t.Fatalf("unattached Computer paused: %v", err)
	}
}

// jsonObject decodes raw as a JSON object, keeping numbers exact.
func jsonObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		t.Fatal(err)
	}
	return object
}
