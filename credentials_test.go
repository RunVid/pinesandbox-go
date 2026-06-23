package pinesandbox

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var uuidV7RE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestGenerateCredentials(t *testing.T) {
	c, err := GenerateCredentials()
	if err != nil {
		t.Fatalf("GenerateCredentials: %v", err)
	}
	if !uuidV7RE.MatchString(c.ID) {
		t.Errorf("ID = %q, not a valid UUIDv7 (version 7 + variant)", c.ID)
	}
	if len(c.Key) != 32 {
		t.Errorf("Key is %d bytes, want 32", len(c.Key))
	}

	// Distinct ids + keys across calls.
	c2, _ := GenerateCredentials()
	if c2.ID == c.ID {
		t.Error("two GenerateCredentials returned the same ID")
	}
	same := true
	for i := range c.Key {
		if c.Key[i] != c2.Key[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("two GenerateCredentials returned the same Key")
	}
}

func TestUUIDV7_EmbedsTimestamp(t *testing.T) {
	// UUIDv7's first 48 bits are the Unix-ms timestamp (that's what makes ids time-ordered).
	// Decode them and check they're ~now — proves the timestamp encoding, without the
	// same-millisecond flakiness of comparing two ids.
	before := time.Now().UnixMilli()
	id, err := uuidV7()
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().UnixMilli()

	hexTS := strings.ReplaceAll(id[:13], "-", "") // "xxxxxxxx-xxxx" → first 12 hex = 48 bits
	ms, err := strconv.ParseInt(hexTS, 16, 64)
	if err != nil {
		t.Fatalf("parse timestamp prefix: %v", err)
	}
	if ms < before || ms > after {
		t.Errorf("embedded ms %d not in [%d, %d]", ms, before, after)
	}
}
