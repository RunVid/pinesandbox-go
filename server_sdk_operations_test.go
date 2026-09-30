package pinesandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// implementedServerSDKOperations is the Go facade's explicit OpenAPI coverage
// catalog. The generated artifact owns which operations are required; this
// catalog must advance with the actual implementation in the same change.
var implementedServerSDKOperations = map[string]any{
	"decidePasskeyCeremony":        (*Computer).ApprovePasskeyCeremony,
	"decideSessionPasskeyCeremony": (*Session).ApprovePasskeyCeremony,
	"declareSessionPasskeyIntent":  (*Session).DeclarePasskeyIntent,
	"deleteComputer":               (*Client).DeleteComputer,
	"deletePasskey":                (*Computer).DeletePasskey,
	"listPasskeyCeremonies":        (*Computer).PendingPasskeyCeremonies,
	"listSessionPasskeyCeremonies": (*Session).PendingPasskeyCeremonies,
	"listPasskeys":                 (*Computer).ListPasskeys,
}

func TestServerSDKOperationsConformToSpec(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "contract", "server-sdk-operations.json"))
	if err != nil {
		t.Skipf("server SDK operation artifact not present (mirror build): %v", err)
	}
	var required map[string][]struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(b, &required); err != nil {
		t.Fatalf("parse server SDK operation artifact: %v", err)
	}
	for _, operation := range required["go"] {
		if implementedServerSDKOperations[operation.OperationID] == nil {
			t.Errorf("OpenAPI requires Go server SDK operation %q, but its facade coverage is missing", operation.OperationID)
		}
	}
}
