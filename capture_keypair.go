package pinesandbox

import (
	"fmt"

	"go.pinesandbox.io/computer/internal/binder"
	"go.pinesandbox.io/computer/internal/statehpke"
)

// binderCaptureKeypair is the binder's internal view of a keypair.
type binderCaptureKeypair = binder.CaptureKeypair

// CaptureKeypair is the integrator's asymmetric state-encryption identity
// (asymmetric component envelope v3): state is sealed to PK on the platform —
// which never holds SK — and restoring it requires this SDK to unwrap the
// per-component secrets during the bind (the two-round restore). PERSIST
// both halves; retain SUPERSEDED generations until their state has been
// re-sealed (a restore of pre-rotation state needs the old SK).
type CaptureKeypair struct {
	// Generation is the keypair's monotonic generation. The portal records
	// a per-Computer floor; a rotation submits generation+1.
	Generation int
	PK         []byte // 32-byte X25519 public key
	SK         []byte // 32-byte X25519 private key — never sent anywhere
}

// GenerateCaptureKeypair mints a fresh capture keypair offline.
func GenerateCaptureKeypair(generation int) (*CaptureKeypair, error) {
	if generation <= 0 {
		return nil, fmt.Errorf("pinesandbox: capture keypair generation must be positive")
	}
	pk, sk, err := statehpke.GenerateKeypair()
	if err != nil {
		return nil, err
	}
	return &CaptureKeypair{Generation: generation, PK: pk, SK: sk}, nil
}

// Fingerprint is the canonical public-key fingerprint (hex sha256) the
// platform binds into every envelope sealed to this keypair.
func (k *CaptureKeypair) Fingerprint() string { return statehpke.Fingerprint(k.PK) }

// String redacts the private half; GoString (%#v) defers to it.
func (k *CaptureKeypair) String() string {
	return fmt.Sprintf("CaptureKeypair{Generation:%d PK:%s SK:[redacted]}", k.Generation, k.Fingerprint()[:16])
}

// GoString redacts under %#v.
func (k *CaptureKeypair) GoString() string { return k.String() }

func (k *CaptureKeypair) validate() error {
	if k == nil {
		return nil
	}
	if k.Generation <= 0 {
		return fmt.Errorf("pinesandbox: capture keypair generation must be positive")
	}
	if len(k.PK) != 32 || len(k.SK) != 32 {
		return fmt.Errorf("pinesandbox: capture keypair halves must be 32 bytes (pk=%d sk=%d)", len(k.PK), len(k.SK))
	}
	return nil
}

// clone deep-copies so the Computer owns its material.
func (k *CaptureKeypair) clone() *CaptureKeypair {
	if k == nil {
		return nil
	}
	pk := make([]byte, len(k.PK))
	copy(pk, k.PK)
	sk := make([]byte, len(k.SK))
	copy(sk, k.SK)
	return &CaptureKeypair{Generation: k.Generation, PK: pk, SK: sk}
}

// binderKeypairs maps the facade type to the binder's internal view.
func binderKeypairs(kps map[int]*CaptureKeypair) map[int]*binderCaptureKeypair {
	if len(kps) == 0 {
		return nil
	}
	out := make(map[int]*binderCaptureKeypair, len(kps))
	for g, kp := range kps {
		out[g] = &binderCaptureKeypair{Generation: kp.Generation, PK: kp.PK, SK: kp.SK}
	}
	return out
}
