package bindhpke

import (
	"bytes"
	"testing"
)

// TestSeal_RoundTrip: a sealed envelope opens back to the plaintext under the same
// recipient key + info + empty AAD (the production path). Proves the locked suite works
// end-to-end.
func TestSeal_RoundTrip(t *testing.T) {
	kp, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	info := Info("pod-uid-1", "boot-id-1")
	pt := []byte(`{"computer_key_current":{"version":1,"bytes":"AAAA"},"broker_grant":"g"}`)

	wire, err := Seal(kp.PublicKeyRaw(), pt, info, nil) // production path: empty AAD
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := kp.Open(wire, info, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("round-trip plaintext mismatch: got %q", got)
	}
}

// TestSeal_WireShape: the wire is enc[32] || aead_ct (X25519 encapsulated key prefix),
// and a fresh seal is non-deterministic (random ephemeral key + nonce).
func TestSeal_WireShape(t *testing.T) {
	kp, _ := GenerateKeypair()
	info := Info("p", "b")
	pt := []byte("hello")
	w1, err := Seal(kp.PublicKeyRaw(), pt, info, nil)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(w1) <= encLen {
		t.Fatalf("wire len %d, want > enc(%d) + tag", len(w1), encLen)
	}
	// ciphertext = enc(32) + aead(plaintext + 16-byte Poly1305 tag).
	if want := encLen + len(pt) + 16; len(w1) != want {
		t.Fatalf("wire len = %d, want %d (enc + pt + tag)", len(w1), want)
	}
	w2, _ := Seal(kp.PublicKeyRaw(), pt, info, nil)
	if bytes.Equal(w1, w2) {
		t.Fatal("two seals of the same plaintext are identical — ephemeral randomness missing")
	}
}

// TestSeal_RejectsBadPubkeyLength: a non-32-byte recipient key must error, not silently
// seal to a truncated key (circl's unmarshal accepts >32 bytes).
func TestSeal_RejectsBadPubkeyLength(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := Seal(make([]byte, n), []byte("x"), Info("p", "b"), nil); err == nil {
			t.Errorf("Seal accepted a %d-byte pubkey, want an error", n)
		}
	}
	kp, _ := GenerateKeypair()
	if _, err := Seal(kp.PublicKeyRaw(), []byte("x"), Info("p", "b"), nil); err != nil {
		t.Errorf("Seal rejected a valid 32-byte pubkey: %v", err)
	}
}

// TestInfo_Format pins the byte-exact info string (a drift here breaks coord interop —
// info is the process-binding both sides must agree on).
func TestInfo_Format(t *testing.T) {
	if got := string(Info("pod-X", "boot-Y")); got != "pine.bind.v1|pod-X|boot-Y" {
		t.Fatalf("Info = %q", got)
	}
}

// TestOpen_Rejects: a wrong recipient key, a wrong info, or a wrong AAD must fail Open —
// the negative guarantees (process-binding + tamper-evidence).
func TestOpen_Rejects(t *testing.T) {
	kp, _ := GenerateKeypair()
	other, _ := GenerateKeypair()
	info := Info("pod", "boot")
	pt := []byte("secret")
	wire, _ := Seal(kp.PublicKeyRaw(), pt, info, nil)

	if _, err := other.Open(wire, info, nil); err == nil {
		t.Error("open with the WRONG recipient key must fail")
	}
	if _, err := kp.Open(wire, Info("pod", "OTHER-boot"), nil); err == nil {
		t.Error("open with the WRONG info (different boot id) must fail")
	}
	if _, err := kp.Open(wire, info, []byte("unexpected-aad")); err == nil {
		t.Error("open with a non-empty AAD (sealed with empty) must fail")
	}
	if _, err := kp.Open(wire[:encLen], info, nil); err == nil {
		t.Error("open of a truncated ciphertext (no AEAD body) must fail")
	}
}
