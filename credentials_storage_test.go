package pinesandbox

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestCredentialsSharedStorageVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/credentials-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Valid   []json.RawMessage
		Invalid []json.RawMessage
		Legacy  []struct {
			Stored    json.RawMessage
			Canonical json.RawMessage
		}
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, wire := range vectors.Valid {
		var creds Credentials
		if err := json.Unmarshal(wire, &creds); err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(creds)
		if err != nil {
			t.Fatal(err)
		}
		var wantValue, gotValue any
		_ = json.Unmarshal(wire, &wantValue)
		_ = json.Unmarshal(got, &gotValue)
		if !reflect.DeepEqual(wantValue, gotValue) {
			t.Fatal("storage round trip changed credentials")
		}
	}
	for _, example := range vectors.Legacy {
		var creds Credentials
		if err := json.Unmarshal(example.Stored, &creds); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(creds)
		if err != nil {
			t.Fatal(err)
		}
		var actual, expected any
		_ = json.Unmarshal(encoded, &actual)
		_ = json.Unmarshal(example.Canonical, &expected)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatal("released Go credentials lost material")
		}
	}
	for i, wire := range vectors.Invalid {
		existing, err := GenerateCredentials()
		if err != nil {
			t.Fatal(err)
		}
		before, _ := json.Marshal(existing)
		if err := json.Unmarshal(wire, &existing); err == nil {
			t.Errorf("invalid vector %d accepted", i)
		}
		after, _ := json.Marshal(existing)
		if string(before) != string(after) {
			t.Errorf("invalid vector %d mutated credentials", i)
		}
	}
}

func TestComputerCredentialsOwnsSecretCopies(t *testing.T) {
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	old := creds.CaptureKeypair
	current, err := GenerateCaptureKeypair(2)
	if err != nil {
		t.Fatal(err)
	}
	computer := newComputer(creds.ID, creds.Key)
	if err := computer.configureCaptureKeypairs(current, []*CaptureKeypair{old}); err != nil {
		t.Fatal(err)
	}
	if err := computer.AddPriorKey(0, creds.Key); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(computer.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	copy := computer.Credentials()
	copy.Key[0] ^= 255
	copy.CaptureKeypair.SK[0] ^= 255
	copy.PriorCaptureKeypairs[0].SK[0] ^= 255
	copy.PriorKeys[0][0] ^= 255
	after, err := json.Marshal(computer.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("caller mutated live credentials")
	}
}
