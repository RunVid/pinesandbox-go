package pinesandbox

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Credentials is a Computer's durable identity: a stable id + a 32-byte state key. PERSIST
// both to re-attach later (the key seals/unseals the Computer's checkpointed state). The
// key is never sent in the clear — it rides the bind handshake HPKE-sealed to the coord's
// ephemeral key.
type Credentials struct {
	ID  string
	Key []byte // 32 bytes
}

// String redacts the state key so a struct dump in a log can't leak it; GoString (%#v)
// defers to it.
func (c Credentials) String() string {
	return fmt.Sprintf("Credentials{ID:%s Key:[%d bytes redacted]}", c.ID, len(c.Key))
}

// GoString redacts the key under %#v.
func (c Credentials) GoString() string { return c.String() }

// GenerateCredentials mints a fresh Computer identity offline: a sortable UUIDv7 id + a
// cryptographically random 32-byte state key. Equivalent to the Ruby
// Computer.generate_credentials.
func GenerateCredentials() (Credentials, error) {
	id, err := uuidV7()
	if err != nil {
		return Credentials{}, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return Credentials{}, fmt.Errorf("pinesandbox: generate state key: %w", err)
	}
	return Credentials{ID: id, Key: key}, nil
}

// validateIdentity rejects a malformed Computer identity up front (fail fast) rather than
// letting a wrong-length key seal state that a later attach can't decrypt, or an empty id
// surface late as a bind auth error. The id may be a UUIDv7 or the caller's own stable id,
// so only non-empty is required; the key must be exactly 32 bytes.
func validateIdentity(id string, key []byte) error {
	if id == "" {
		return fmt.Errorf("pinesandbox: computer id required")
	}
	if len(key) != 32 {
		return fmt.Errorf("pinesandbox: computer key must be exactly 32 bytes, got %d", len(key))
	}
	return nil
}

// uuidV7 builds an RFC 9562 UUIDv7 (48-bit Unix-ms timestamp + version/variant + random),
// so ids sort by creation time. Dependency-free (matches the Ruby SecureRandom.uuid_v7).
func uuidV7() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("pinesandbox: generate computer id: %w", err)
	}
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = 0x70 | (b[6] & 0x0f) // version 7
	b[8] = 0x80 | (b[8] & 0x3f) // variant 10
	return formatUUID(b), nil
}

func formatUUID(b [16]byte) string {
	var h [32]byte
	hex.Encode(h[:], b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
