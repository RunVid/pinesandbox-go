// Package statehpke is the integrator side of the v3 (asymmetric) state
// envelope: opening a component's sealed per-snapshot secret with the
// capture PRIVATE key during the two-round bind restore, and generating the
// capture keypair itself. Locked suite (RFC 9180 mode_base,
// X25519-HKDF-SHA256 + ChaCha20-Poly1305 — the bind channel's family) and
// the component info domain MUST byte-match coord's crypt_component_v3.go;
// the monorepo interop module pins both directions against coord's real
// implementation.
package statehpke

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/cloudflare/circl/hpke"
)

var (
	kemID = hpke.KEM_X25519_HKDF_SHA256
	suite = hpke.NewSuite(kemID, hpke.KDF_HKDF_SHA256, hpke.AEAD_ChaCha20Poly1305)
)

// SecretSize is the per-component secret length.
const SecretSize = 32

// ComponentInfo builds the domain-separated HPKE info for one component's
// sealed secret. MUST byte-match coord's componentHPKEInfo.
func ComponentInfo(cid, component, id string, attachEpoch int64, keyVersion int, fingerprint string) []byte {
	return fmt.Appendf(nil, "pine.state.component.v2|%s|%s|%s|%d|%d|%s",
		cid, component, id, attachEpoch, keyVersion, fingerprint)
}

// Fingerprint is the canonical public-key fingerprint: lowercase hex of
// SHA-256(raw 32-byte X25519 public key).
func Fingerprint(pkRaw []byte) string {
	sum := sha256.Sum256(pkRaw)
	return hex.EncodeToString(sum[:])
}

// GenerateKeypair mints a capture keypair, returning (pk, sk) as raw
// 32-byte values. The private half never leaves the integrator.
func GenerateKeypair() (pk, sk []byte, err error) {
	pub, priv, err := kemID.Scheme().GenerateKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("pinesandbox: generate capture keypair: %w", err)
	}
	pkRaw, err := pub.MarshalBinary()
	if err != nil {
		return nil, nil, fmt.Errorf("pinesandbox: marshal capture pk: %w", err)
	}
	skRaw, err := priv.MarshalBinary()
	if err != nil {
		return nil, nil, fmt.Errorf("pinesandbox: marshal capture sk: %w", err)
	}
	return pkRaw, skRaw, nil
}

// OpenComponentSecret unwraps one restore-challenge component's sealed
// secret with the capture private key. Every info input is authenticated
// into the seal — a challenge lifted onto another Computer, component,
// object, epoch, or key generation yields no secret.
func OpenComponentSecret(skRaw, hpkeEnc, hpkeSealedS []byte, cid, component, id string, attachEpoch int64, keyVersion int, fingerprint string) ([]byte, error) {
	priv, err := kemID.Scheme().UnmarshalBinaryPrivateKey(skRaw)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: capture sk unmarshal: %w", err)
	}
	recv, err := suite.NewReceiver(priv, ComponentInfo(cid, component, id, attachEpoch, keyVersion, fingerprint))
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: state hpke receiver: %w", err)
	}
	opener, err := recv.Setup(hpkeEnc)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: state hpke setup: %w", err)
	}
	s, err := opener.Open(hpkeSealedS, nil)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: state hpke open: %w", err)
	}
	if len(s) != SecretSize {
		return nil, fmt.Errorf("pinesandbox: component secret wrong size (got %d, want %d)", len(s), SecretSize)
	}
	return s, nil
}
