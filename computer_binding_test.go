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
	"sync/atomic"
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
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/sb-1":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code":"COMPUTER_NOT_FOUND","message":"gone"}`)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	client := &Client{conn: buildTestConnection(t, srv.URL, srv.URL)}
	ctx := context.Background()
	const id = "0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000"
	key := make([]byte, 32)
	attached := func() *Computer {
		comp, err := client.AdoptExisting(ctx, id, key, "sb-1", "ct_x")
		if err != nil {
			t.Fatal(err)
		}
		return comp
	}
	handles := []struct {
		reason string
		comp   func() *Computer
	}{
		{"it was never attached", func() *Computer { return newComputer(id, key) }},
		{"Stop was called on this handle", func() *Computer {
			comp := attached()
			if gone, err := comp.Stop(ctx); !gone || err != nil {
				t.Fatalf("Stop = %v, %v", gone, err)
			}
			return comp
		}},
		{"Kill was called on this handle", func() *Computer {
			comp := attached()
			if !comp.Kill(ctx) {
				t.Fatal("Kill = false")
			}
			return comp
		}},
		{"Delete was called on this handle", func() *Computer {
			comp := attached()
			if err := comp.Delete(ctx); err != nil {
				t.Fatal(err)
			}
			return comp
		}},
	}
	calls := map[string]func(*Computer) error{
		"Binding":        func(c *Computer) error { _, err := c.Binding(); return err },
		"Status":         func(c *Computer) error { _, err := c.Status(ctx); return err },
		"Pause":          func(c *Computer) error { return c.Pause(ctx) },
		"Resume":         func(c *Computer) error { return c.Resume(ctx) },
		"SandboxDetails": func(c *Computer) error { _, err := c.SandboxDetails(ctx); return err },
		"SetIdleTimeout": func(c *Computer) error { _, err := c.SetIdleTimeout(ctx, time.Minute); return err },
		"CreateSession":  func(c *Computer) error { _, err := c.CreateSession(ctx, CreateSessionOptions{}); return err },
		"Session":        func(c *Computer) error { _, err := c.Session(ctx, "s"); return err },
		"AdoptSession":   func(c *Computer) error { _, err := c.AdoptSession("s", "ps_x"); return err },
		"Sessions":       func(c *Computer) error { _, err := c.Sessions(ctx); return err },
		"DestroySession": func(c *Computer) error { return c.DestroySession(ctx, "s", false) },
		"Snapshot":       func(c *Computer) error { _, err := c.Snapshot(ctx); return err },
		"Checkpoint":     func(c *Computer) error { _, err := c.Checkpoint(ctx); return err },
		"OrphanTabs":     func(c *Computer) error { _, err := c.OrphanTabs(ctx); return err },
		"ListPasskeys":   func(c *Computer) error { _, err := c.ListPasskeys(ctx); return err },
		"Capabilities":   func(c *Computer) error { _, err := c.Capabilities(ctx); return err },
		"Skills.Drafts":  func(c *Computer) error { _, err := c.Skills().Drafts(ctx); return err },
		"ArtifactLink":   func(c *Computer) error { _, err := c.ArtifactLink(ctx, "a.pdf", 0); return err },
	}
	for _, h := range handles {
		comp := h.comp()
		sent := requests.Load()
		want := "pinesandbox: Computer " + id + ": this handle has no binding (" + h.reason + ")"
		for name, call := range calls {
			err := call(comp)
			var bindErr *BindError
			if !errors.As(err, &bindErr) || !errors.Is(err, ErrNotBound) || err.Error() != want {
				t.Errorf("%s (%s) = %T %v, want a *BindError %q matching ErrNotBound", name, h.reason, err, err, want)
			}
		}
		if n := requests.Load() - sent; n != 0 {
			t.Errorf("%s: calls without a binding sent %d requests", h.reason, n)
		}
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
