package pinesandbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectPolicyDiscovery(t *testing.T) {
	status := 200
	body := `{"max_concurrent_computers":null,"lifecycle":null}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/project-policy" || r.Header.Get("Authorization") != "Bearer pk_test" {
			t.Error("policy discovery used wrong route or authority")
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{Endpoint: "http://pine.localhost:8080", ControlHost: strings.TrimPrefix(server.URL, "http://"), APIKey: "pk_test"})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := client.ProjectPolicy(context.Background())
	if err != nil || policy.Lifecycle != nil || policy.MaxConcurrentComputers != nil {
		t.Fatalf("legacy policy: %v %v", policy, err)
	}
	body = `{"max_concurrent_computers":3,"lifecycle":{"max_idle_timeout_seconds":172800,"max_runtime_age_seconds":604800}}`
	policy, err = client.ProjectPolicy(context.Background())
	if err != nil || policy.Lifecycle.MaxIdleTimeoutSeconds != 172800 || *policy.MaxConcurrentComputers != 3 {
		t.Fatalf("enrolled policy: %v %v", policy, err)
	}
	for _, code := range []int{200, 403, 404, 503} {
		status, body = code, `{}`
		if _, err := client.ProjectPolicy(context.Background()); err == nil {
			t.Fatalf("accepted invalid policy/status %d", code)
		}
	}
}
