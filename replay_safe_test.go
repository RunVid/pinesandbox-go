package pinesandbox

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestReplaySafeOperationsRetryTransportFaults: every POST the contract marks replay-safe
// (operations.json replay_safe, x-pine-replay-safe) is sent again after a transient
// transport fault, on the shared backoff, through the scenario harness. The scenarios
// cover cancelAgentV2 and the not-replay-safe tabsCreate; these cover the rest the
// runner's verbs reach.
func TestReplaySafeOperationsRetryTransportFaults(t *testing.T) {
	const given = `{"computer":{"id":"0190aaaa-bbbb-7ccc-8ddd-eeeeffff7001","key":"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
		"runtime_id":"rt-1","computer_token":"ct_scenario"},"session":{"name":"main","token":"ps_scenario"}}`
	const turn = `{"status":200,"json":{"turn_id":"t1","state":"running","first_event_id":1,"last_event_id":1}}`
	const token = `{"status":200,"json":{"token":"eyJhbGciOiJFZERTQSJ9.eyJleHAiOjQxMDI0NDQ4MDB9.c2ln","expires_in":900}}`
	const fault = `{"transport_error":true}`
	runtime := `{"status":200,"headers":{"ETag":"\"v1\""},"json":{"id":"rt-1","status":{"state":"Running"},"metadata":{}}}`
	cases := []struct{ name, call, exchanges string }{
		{"runAgentV2", `{"verb":"AgentMode.run","args":{"goal":"g","idempotency_key":"k1"}}`,
			`[{"request":{"method":"POST","host":"computer","path":"/v2/sessions/main/agent/run","auth":"computer","headers":{"Idempotency-Key":"k1"}},"response":` + fault + `},
			  {"request":{"method":"POST","host":"computer","path":"/v2/sessions/main/agent/run","auth":"computer","headers":{"Idempotency-Key":"k1"}},"response":` + turn + `}]`},
		{"answerAgentV2", `{"verb":"AgentMode.answer","args":{"turn_id":"t0","answer":"yes"}}`,
			`[{"request":{"method":"POST","host":"computer","path":"/v2/sessions/main/agent/answer","auth":"computer"},"response":` + fault + `},
			  {"request":{"method":"POST","host":"computer","path":"/v2/sessions/main/agent/answer","auth":"computer"},"response":` + turn + `}]`},
		{"mintControlToken", `{"verb":"Computer.status","args":{}}`,
			`[{"request":{"method":"POST","host":"control","path":"/v1/control-token","auth":"project_key"},"response":` + fault + `},
			  {"request":{"method":"POST","host":"control","path":"/v1/control-token","auth":"project_key"},"response":` + token + `},
			  {"request":{"method":"GET","host":"control","path":"/sandboxes/rt-1","auth":"project_token"},"response":` + runtime + `}]`},
		{"stopComputerRuntime", `{"verb":"Computer.stop","args":{}}`,
			`[{"request":{"method":"POST","host":"control","path":"/v1/control-token","auth":"project_key"},"response":` + token + `},
			  {"request":{"method":"GET","host":"control","path":"/sandboxes/rt-1","auth":"project_token"},"response":` + runtime + `},
			  {"request":{"method":"POST","host":"control","path":"/sandboxes/rt-1/stop","auth":"project_token","json":{"id":"$any"}},"response":` + fault + `},
			  {"request":{"method":"POST","host":"control","path":"/sandboxes/rt-1/stop","auth":"project_token","json":{"id":"$same"}},"response":{"status":202,"text":""}},
			  {"request":{"method":"GET","host":"control","path":"/sandboxes/rt-1","auth":"project_token"},"response":{"status":404,"json":{"code":"NOT_FOUND","message":"gone"}}}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s scenario
			doc := `{"name":"` + tc.name + `","given":` + given + `,"call":` + tc.call + `,"exchanges":` + tc.exchanges +
				`,"expect":{},"sleeps_ms":[200]}`
			if err := json.Unmarshal([]byte(strings.ReplaceAll(doc, "\n", "")), &s); err != nil {
				t.Fatal(err)
			}
			runScenario(t, s)
		})
	}
}
