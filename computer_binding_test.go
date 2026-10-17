package pinesandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestBindingSharedStorageContract(t *testing.T) {
	data, err := os.ReadFile("testdata/binding-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct{ Valid, Invalid []json.RawMessage }
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

func TestBindingRequiresBoundRuntime(t *testing.T) {
	comp := newComputer("0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000", make([]byte, 32))
	if _, err := comp.Binding(); err == nil {
		t.Fatal("unattached Computer exported a binding")
	}
	if _, err := comp.RuntimeDetails(context.Background()); err == nil {
		t.Fatal("unattached Computer returned runtime details")
	}
}
