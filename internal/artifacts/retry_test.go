package artifacts

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestStorageRetryFollowsTheSharedRule runs contract/storage-retry.json: which storage
// answers an upload or download resumes from, and the Retryable flag of the error each
// ends with.
func TestStorageRetryFollowsTheSharedRule(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "storage-retry.json"))
	if err != nil {
		t.Skipf("contract artifact not present (mirror build): %v", err)
	}
	var rule struct {
		Retry   []int `json:"retry"`
		NoRetry []int `json:"no_retry"`
	}
	if err := json.Unmarshal(b, &rule); err != nil {
		t.Fatal(err)
	}
	if len(rule.Retry) == 0 || len(rule.NoRetry) == 0 {
		t.Fatalf("rule = %+v", rule)
	}
	for want, statuses := range map[bool][]int{true: rule.Retry, false: rule.NoRetry} {
		for _, status := range statuses {
			e := storageError("storage.googleapis.com", "GET /storage/v1/b/{bucket}/o/{object}", status, nil, http.Header{})
			if got := resumable(context.Background(), e); e.Retryable != want || got != want {
				t.Errorf("storage %d: Retryable %v, resumed %v; want %v", status, e.Retryable, got, want)
			}
		}
	}
}
