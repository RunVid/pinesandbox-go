package pinesandbox

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"
)

var computerIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Credentials is a Computer's durable identity: a stable id + a 32-byte state key. PERSIST
// both to re-attach later (the key seals/unseals the Computer's checkpointed state). The
// key is never sent in the clear: the attach seals it with HPKE to the runtime's
// single-use attach key.
type Credentials struct {
	ID                   string
	Key                  []byte // 32 bytes
	CaptureKeypair       *CaptureKeypair
	PriorCaptureKeypairs []*CaptureKeypair
	PriorKeys            map[int][]byte
	Ephemeral            bool
}

// String redacts the state key so a struct dump in a log can't leak it; GoString (%#v)
// defers to it.
func (c Credentials) String() string {
	return fmt.Sprintf("Credentials{ID:%s Key:[%d bytes redacted] CaptureKeypair:[redacted]}", c.ID, len(c.Key))
}

// GoString redacts the key under %#v.
func (c Credentials) GoString() string { return c.String() }

// The storage envelope is shared with Ruby. No runtime tokens or binding revision
// belong here. These bytes contain secrets; applications own encrypted storage.
type credentialsJSON struct {
	Schema               int                  `json:"schema"`
	ID                   string               `json:"id"`
	Key                  string               `json:"key"`
	Ephemeral            bool                 `json:"ephemeral"`
	CaptureKeypair       *captureKeypairJSON  `json:"capture_keypair"`
	PriorCaptureKeypairs []captureKeypairJSON `json:"prior_capture_keypairs"`
	PriorKeys            []priorKeyJSON       `json:"prior_keys"`
}
type captureKeypairJSON struct {
	Generation int    `json:"generation"`
	PK         string `json:"pk"`
	SK         string `json:"sk"`
}
type priorKeyJSON struct {
	Version *int   `json:"version"`
	Key     string `json:"key"`
}

func (c Credentials) validate() error {
	if err := validateIdentity(c.ID, c.Key); err != nil {
		return err
	}
	if !c.Ephemeral && c.CaptureKeypair == nil {
		return fmt.Errorf("pinesandbox: persistent credentials require a capture keypair")
	}
	if err := c.CaptureKeypair.validate(); err != nil {
		return err
	}
	seen := map[int]bool{}
	for _, kp := range c.PriorCaptureKeypairs {
		if kp == nil || c.CaptureKeypair == nil {
			return fmt.Errorf("pinesandbox: prior capture keypairs require a current keypair and non-nil entries")
		}
		if err := kp.validate(); err != nil {
			return err
		}
		if seen[kp.Generation] || kp.Generation >= c.CaptureKeypair.Generation {
			return fmt.Errorf("pinesandbox: prior capture generations must be distinct and below current")
		}
		seen[kp.Generation] = true
	}
	for version, key := range c.PriorKeys {
		if version == CurrentKeyVersion || len(key) != 32 {
			return fmt.Errorf("pinesandbox: prior keys require a non-current version and 32 bytes")
		}
	}
	return nil
}

func encodeCaptureKeypair(kp *CaptureKeypair) *captureKeypairJSON {
	if kp == nil {
		return nil
	}
	return &captureKeypairJSON{kp.Generation, base64.StdEncoding.EncodeToString(kp.PK), base64.StdEncoding.EncodeToString(kp.SK)}
}

// MarshalJSON encodes durable credentials, including retained keys needed to
// restore older state. The result contains secrets, not encrypted storage.
func (c Credentials) MarshalJSON() ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	wire := credentialsJSON{Schema: 1, ID: c.ID, Key: base64.StdEncoding.EncodeToString(c.Key), Ephemeral: c.Ephemeral,
		CaptureKeypair: encodeCaptureKeypair(c.CaptureKeypair), PriorCaptureKeypairs: []captureKeypairJSON{}, PriorKeys: []priorKeyJSON{}}
	for _, kp := range c.PriorCaptureKeypairs {
		wire.PriorCaptureKeypairs = append(wire.PriorCaptureKeypairs, *encodeCaptureKeypair(kp))
	}
	sort.Slice(wire.PriorCaptureKeypairs, func(i, j int) bool {
		return wire.PriorCaptureKeypairs[i].Generation < wire.PriorCaptureKeypairs[j].Generation
	})
	for version, key := range c.PriorKeys {
		wire.PriorKeys = append(wire.PriorKeys, priorKeyJSON{&version, base64.StdEncoding.EncodeToString(key)})
	}
	sort.Slice(wire.PriorKeys, func(i, j int) bool { return *wire.PriorKeys[i].Version < *wire.PriorKeys[j].Version })
	return json.Marshal(wire)
}

func decodeCredentialKey(s string) ([]byte, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != s {
		return nil, fmt.Errorf("pinesandbox: credential key must be 32 bytes in strict Base64")
	}
	return key, nil
}
func decodeCaptureKeypair(wire *captureKeypairJSON) (*CaptureKeypair, error) {
	if wire == nil {
		return nil, nil
	}
	pk, err := decodeCredentialKey(wire.PK)
	if err != nil {
		return nil, err
	}
	sk, err := decodeCredentialKey(wire.SK)
	if err != nil {
		return nil, err
	}
	return &CaptureKeypair{Generation: wire.Generation, PK: pk, SK: sk}, nil
}

// UnmarshalJSON validates the shared storage format and replaces c only on success.
func (c *Credentials) UnmarshalJSON(data []byte) error {
	// encoding/json treats null as a zero value for scalars and slices. Reject
	// it where the storage contract requires a boolean or collection.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("pinesandbox: malformed credentials JSON")
	}
	// Releases before the shared schema used encoding/json's exported Go
	// field names. Read those saved records; always write the shared format.
	if _, versioned := fields["schema"]; !versioned && fields["ID"] != nil {
		var legacy struct {
			ID             string
			Key            []byte
			CaptureKeypair *CaptureKeypair
		}
		if err := json.Unmarshal(data, &legacy); err != nil {
			return fmt.Errorf("pinesandbox: malformed legacy credentials JSON")
		}
		next := Credentials{ID: legacy.ID, Key: legacy.Key, CaptureKeypair: legacy.CaptureKeypair}
		if err := next.validate(); err != nil {
			return err
		}
		*c = next
		return nil
	}
	for _, name := range []string{"ephemeral", "prior_capture_keypairs", "prior_keys"} {
		if raw, present := fields[name]; present && string(raw) == "null" {
			return fmt.Errorf("pinesandbox: credentials field %s cannot be null", name)
		}
	}
	var wire credentialsJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("pinesandbox: malformed credentials JSON")
	}
	if wire.Schema != 1 {
		return fmt.Errorf("pinesandbox: unsupported credentials schema %d", wire.Schema)
	}
	key, err := decodeCredentialKey(wire.Key)
	if err != nil {
		return err
	}
	kp, err := decodeCaptureKeypair(wire.CaptureKeypair)
	if err != nil {
		return err
	}
	next := Credentials{ID: wire.ID, Key: key, CaptureKeypair: kp, Ephemeral: wire.Ephemeral, PriorKeys: map[int][]byte{}}
	for i := range wire.PriorCaptureKeypairs {
		prior, err := decodeCaptureKeypair(&wire.PriorCaptureKeypairs[i])
		if err != nil {
			return err
		}
		next.PriorCaptureKeypairs = append(next.PriorCaptureKeypairs, prior)
	}
	for _, prior := range wire.PriorKeys {
		if prior.Version == nil {
			return fmt.Errorf("pinesandbox: prior key is missing version")
		}
		if _, exists := next.PriorKeys[*prior.Version]; exists {
			return fmt.Errorf("pinesandbox: duplicate prior key version")
		}
		key, err := decodeCredentialKey(prior.Key)
		if err != nil {
			return err
		}
		next.PriorKeys[*prior.Version] = key
	}
	if err := next.validate(); err != nil {
		return err
	}
	*c = next
	return nil
}

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
	kp, err := GenerateCaptureKeypair(1)
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{ID: id, Key: key, CaptureKeypair: kp}, nil
}

// validateIdentity rejects a malformed Computer identity up front (fail fast) rather than
// allocating a sandbox that Portal cannot attribute, or letting a wrong-length key seal
// state that a later attach cannot decrypt. Computer ids are lowercase UUIDv7 across the
// SDK, Portal, coordinator, and broker; the key must be exactly 32 bytes.
func validateIdentity(id string, key []byte) error {
	if id == "" {
		return fmt.Errorf("pinesandbox: computer id required")
	}
	if !computerIDRE.MatchString(id) {
		return fmt.Errorf("pinesandbox: computer id must be a lowercase UUIDv7")
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
