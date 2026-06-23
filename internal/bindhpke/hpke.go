// Package bindhpke is the SENDER side of the v8.3 bind HPKE handshake — the SDK seals
// the bind plaintext for coord's per-process ephemeral X25519 key. RFC 9180 mode_base,
// suite LOCKED by PERSISTENCE_BIND_CONTRACT §5.2 (mirrors coord's pkg/bindhpke exactly so
// the wire is byte-compatible — the interop/ test module proves SDK-seal/coord-open):
//
//	KEM  = X25519-HKDF-SHA256 (0x0020)
//	KDF  = HKDF-SHA256        (0x0001)
//	AEAD = ChaCha20-Poly1305  (0x0003)
//	info = "pine.bind.v1|" + pod_uid + "|" + coord_boot_id
//	aad  = EMPTY on the production portal-attach path (the payload→pod binding lives in
//	       `info`; the non-empty AAD in coord's legacy crosslang fixture is NOT this path)
//
//	wire ciphertext = enc[32] || aead_ciphertext   (enc = the X25519 encapsulated key)
//
// This is a domain package (under internal/, NOT internal/base): it may import base, but
// base must never import it (the base ↛ domain boundary, §3 — enforced by the
// import-graph test).
package bindhpke

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/cloudflare/circl/hpke"
	"github.com/cloudflare/circl/kem"
)

// encLen is the X25519 encapsulated-key length (RFC 9180 KEM 0x0020).
const encLen = 32

// kemID / suite are the LOCKED bind HPKE primitives (§5.2). Centralized so the sender
// here cannot drift from coord's recipient.
var (
	kemID = hpke.KEM_X25519_HKDF_SHA256
	suite = hpke.NewSuite(kemID, hpke.KDF_HKDF_SHA256, hpke.AEAD_ChaCha20Poly1305)
)

// Info builds the HPKE info string per §5.2 — the process-binding (a ciphertext sealed
// for one pod/boot can't be opened by another). MUST byte-match coord's Info.
func Info(podUID, coordBootID string) []byte {
	return []byte("pine.bind.v1|" + podUID + "|" + coordBootID)
}

// Seal is the production sender path: encrypt `plaintext` to coord's recipient public key
// (the 32-byte X25519 key from GET /v1/coord/bind-pubkey). Returns the wire ciphertext
// (enc[32] || aead_ct). The production bind path passes aad=nil (empty).
func Seal(recipientPubRaw, plaintext, info, aad []byte) ([]byte, error) {
	// circl's UnmarshalBinaryPublicKey silently accepts inputs longer than 32 bytes
	// (truncating), so validate the X25519 key length explicitly — a wrong-length key must
	// error, not seal to a truncated key. (The production path already rejects non-32-byte
	// keys at parseBindPubkey; this is defense-in-depth for the exported primitive.)
	if len(recipientPubRaw) != encLen {
		return nil, fmt.Errorf("bindhpke: recipient pubkey is %d bytes, want %d", len(recipientPubRaw), encLen)
	}
	pub, err := kemID.Scheme().UnmarshalBinaryPublicKey(recipientPubRaw)
	if err != nil {
		return nil, fmt.Errorf("bindhpke: unmarshal recipient pubkey: %w", err)
	}
	sender, err := suite.NewSender(pub, info)
	if err != nil {
		return nil, fmt.Errorf("bindhpke: new sender: %w", err)
	}
	enc, sealer, err := sender.Setup(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("bindhpke: sender setup: %w", err)
	}
	ct, err := sealer.Seal(plaintext, aad)
	if err != nil {
		return nil, fmt.Errorf("bindhpke: seal: %w", err)
	}
	if len(enc) != encLen {
		return nil, fmt.Errorf("bindhpke: unexpected enc length %d, want %d", len(enc), encLen)
	}
	return append(enc, ct...), nil
}

// Keypair is a recipient X25519 keypair. The SDK never holds coord's private key in
// production (coord opens), so this exists for tests + interop: GenerateKeypair + Open
// mirror coord's recipient so the SDK's own round-trip and the cross-impl interop test
// can verify a Seal without standing up a coordinator.
type Keypair struct {
	priv   kem.PrivateKey
	pubRaw []byte
}

// GenerateKeypair makes a fresh recipient keypair (test/interop use).
func GenerateKeypair() (*Keypair, error) {
	pub, priv, err := kemID.Scheme().GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("bindhpke: generate keypair: %w", err)
	}
	pubRaw, err := pub.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("bindhpke: marshal pubkey: %w", err)
	}
	return &Keypair{priv: priv, pubRaw: pubRaw}, nil
}

// PublicKeyRaw returns the 32-byte X25519 public key (what bind-pubkey serves).
func (k *Keypair) PublicKeyRaw() []byte {
	out := make([]byte, len(k.pubRaw))
	copy(out, k.pubRaw)
	return out
}

// PublicKeyHash returns SHA-256(pubRaw) — the value the bind_token commits to.
func (k *Keypair) PublicKeyHash() [32]byte { return sha256.Sum256(k.pubRaw) }

// Open decrypts a wire ciphertext (enc[32] || aead_ct) with the same info/aad used to
// seal. Mirrors coord's Open (recipient side) for the round-trip + interop tests.
func (k *Keypair) Open(ciphertext, info, aad []byte) ([]byte, error) {
	if len(ciphertext) <= encLen {
		return nil, errors.New("bindhpke: ciphertext shorter than encapsulated key")
	}
	enc, ct := ciphertext[:encLen], ciphertext[encLen:]
	recv, err := suite.NewReceiver(k.priv, info)
	if err != nil {
		return nil, fmt.Errorf("bindhpke: new receiver: %w", err)
	}
	opener, err := recv.Setup(enc)
	if err != nil {
		return nil, fmt.Errorf("bindhpke: receiver setup: %w", err)
	}
	pt, err := opener.Open(ct, aad)
	if err != nil {
		return nil, fmt.Errorf("bindhpke: open: %w", err)
	}
	return pt, nil
}
