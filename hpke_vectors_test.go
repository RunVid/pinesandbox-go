package pinesandbox

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudflare/circl/hpke"

	"go.pinesandbox.io/computer/internal/binder"
	"go.pinesandbox.io/computer/internal/bindhpke"
	"go.pinesandbox.io/computer/internal/statehpke"
)

// TestHPKEVectors runs the language-neutral attach crypto vectors
// (contract/hpke-vectors.json, copied into testdata so the published module runs them
// too) against this SDK alone: it opens the coordinator-sealed bind and restore
// ciphertexts with circl and the vector's recipient key, checks the SDK's own seal opens
// the same way, and checks the info strings, capture fingerprints, component-secret
// unwrap and attach idempotency keys. go/interop additionally checks the coordinator.
func TestHPKEVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "hpke-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Seal []struct {
			Name, Domain, Info, Plaintext, Ciphertext string
			PodUID                                    string `json:"pod_uid"`
			CoordBootID                               string `json:"coord_boot_id"`
			RecipientSK                               string `json:"recipient_sk"`
			RecipientPK                               string `json:"recipient_pk"`
		} `json:"seal"`
		Components []struct {
			Name, Component, ID, Info, Secret string
			ComputerID                        string `json:"computer_id"`
			AttachEpoch                       int64  `json:"attach_epoch"`
			RecipientKeyVersion               int    `json:"recipient_key_version"`
			PKFingerprint                     string `json:"pk_fingerprint"`
			CaptureSK                         string `json:"capture_sk"`
			CapturePK                         string `json:"capture_pk"`
			HPKEEnc                           string `json:"hpke_enc"`
			HPKESealedS                       string `json:"hpke_sealed_s"`
		} `json:"components"`
		AttachKeys []struct {
			ComputerID  string `json:"computer_id"`
			SandboxID   string `json:"sandbox_id"`
			PodUID      string `json:"pod_uid"`
			CoordBootID string `json:"coord_boot_id"`
			Key         string `json:"idempotency_key"`
		} `json:"attach_idempotency_keys"`
		Fingerprints []struct{ PK, Fingerprint string } `json:"fingerprints"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Seal) == 0 || len(v.Components) == 0 || len(v.AttachKeys) == 0 || len(v.Fingerprints) == 0 {
		t.Fatal("hpke-vectors.json is missing a vector family")
	}
	b64 := func(s string) []byte {
		t.Helper()
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("not base64url: %q", s)
		}
		return b
	}
	suite := hpke.NewSuite(hpke.KEM_X25519_HKDF_SHA256, hpke.KDF_HKDF_SHA256, hpke.AEAD_ChaCha20Poly1305)
	open := func(t *testing.T, skRaw, ciphertext, info []byte) []byte {
		t.Helper()
		sk, err := hpke.KEM_X25519_HKDF_SHA256.Scheme().UnmarshalBinaryPrivateKey(skRaw)
		if err != nil {
			t.Fatal(err)
		}
		receiver, err := suite.NewReceiver(sk, info)
		if err != nil {
			t.Fatal(err)
		}
		opener, err := receiver.Setup(ciphertext[:32])
		if err != nil {
			t.Fatal(err)
		}
		plaintext, err := opener.Open(ciphertext[32:], nil) // production AAD is empty
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return plaintext
	}

	for _, s := range v.Seal {
		t.Run("seal/"+s.Name, func(t *testing.T) {
			info := bindhpke.Info(s.PodUID, s.CoordBootID)
			if s.Domain == "restore" {
				info = bindhpke.RestoreInfo(s.PodUID, s.CoordBootID)
			}
			if string(info) != s.Info {
				t.Fatalf("info = %q, want %q", info, s.Info)
			}
			if got := open(t, b64(s.RecipientSK), b64(s.Ciphertext), info); string(got) != s.Plaintext {
				t.Fatalf("opened %q, want %q", got, s.Plaintext)
			}
			// The SDK's seal is randomized, so its direction is checked by opening it.
			sealed, err := bindhpke.Seal(b64(s.RecipientPK), []byte(s.Plaintext), info, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := open(t, b64(s.RecipientSK), sealed, info); string(got) != s.Plaintext {
				t.Fatal("the SDK's seal does not open with the vector's recipient key")
			}
		})
	}
	for _, c := range v.Components {
		t.Run("component/"+c.Name, func(t *testing.T) {
			info := statehpke.ComponentInfo(c.ComputerID, c.Component, c.ID, c.AttachEpoch, c.RecipientKeyVersion, c.PKFingerprint)
			if string(info) != c.Info {
				t.Fatalf("component info = %q, want %q", info, c.Info)
			}
			if statehpke.Fingerprint(b64(c.CapturePK)) != c.PKFingerprint {
				t.Fatal("capture fingerprint differs from the vector")
			}
			secret, err := statehpke.OpenComponentSecret(b64(c.CaptureSK), b64(c.HPKEEnc), b64(c.HPKESealedS),
				c.ComputerID, c.Component, c.ID, c.AttachEpoch, c.RecipientKeyVersion, c.PKFingerprint)
			if err != nil || !bytes.Equal(secret, b64(c.Secret)) {
				t.Fatalf("component secret open: %v", err)
			}
		})
	}
	for _, k := range v.AttachKeys {
		if got := binder.AttachIdempotencyKey(k.ComputerID, k.SandboxID, k.PodUID, k.CoordBootID); got != k.Key {
			t.Errorf("attach idempotency key for %s = %s, want %s", k.SandboxID, got, k.Key)
		}
	}
	for _, f := range v.Fingerprints {
		if got := statehpke.Fingerprint(b64(f.PK)); got != f.Fingerprint {
			t.Errorf("fingerprint of %s = %s, want %s", f.PK, got, f.Fingerprint)
		}
	}
}
