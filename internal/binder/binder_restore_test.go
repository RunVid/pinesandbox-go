package binder

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudflare/circl/hpke"

	"go.pinesandbox.io/computer/internal/coordinator"
	"go.pinesandbox.io/computer/internal/statehpke"
	"go.pinesandbox.io/computer/internal/tokens"
)

// sealComponentSecret is the test-only COORD-side sealer (the production
// twin lives in coord's crypt_component_v3.go; the interop module pins the
// two against each other). Suite + info must match statehpke exactly.
func sealComponentSecret(t *testing.T, pkRaw, s []byte, info []byte) (enc, sealed []byte) {
	t.Helper()
	suite := hpke.NewSuite(hpke.KEM_X25519_HKDF_SHA256, hpke.KDF_HKDF_SHA256, hpke.AEAD_ChaCha20Poly1305)
	pub, err := hpke.KEM_X25519_HKDF_SHA256.Scheme().UnmarshalBinaryPublicKey(pkRaw)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := suite.NewSender(pub, info)
	if err != nil {
		t.Fatal(err)
	}
	enc, sealer, err := sender.Setup(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err = sealer.Seal(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	return enc, sealed
}

// testKeypair mints a capture keypair for the binder config.
func testKeypair(t *testing.T, generation int) *CaptureKeypair {
	t.Helper()
	pk, sk, err := statehpke.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	return &CaptureKeypair{Generation: generation, PK: pk, SK: sk}
}

// challengeFor seals a fresh secret to kp for one live component and
// returns the wire challenge + the secret.
func challengeFor(t *testing.T, kp *CaptureKeypair, computerID, challengeID string) (*coordinator.RestoreChallenge, []byte) {
	t.Helper()
	s := make([]byte, statehpke.SecretSize)
	if _, err := rand.Read(s); err != nil {
		t.Fatal(err)
	}
	fp := statehpke.Fingerprint(kp.PK)
	info := statehpke.ComponentInfo(computerID, "live", "snap-1", 4, kp.Generation, fp)
	enc, sealed := sealComponentSecret(t, kp.PK, s, info)
	return &coordinator.RestoreChallenge{
		ChallengeID: challengeID,
		Generation:  1,
		Components: []coordinator.ChallengeComponent{{
			Component:           "live",
			ID:                  "snap-1",
			AttachEpoch:         4,
			RecipientKeyVersion: kp.Generation,
			PKFingerprint:       fp,
			HPKEEnc:             base64.RawURLEncoding.EncodeToString(enc),
			HPKESealedS:         base64.RawURLEncoding.EncodeToString(sealed),
		}},
	}, s
}

func v2Config(coord *fakeCoord, minter *fakeMinter, clk *fakeClock, kp *CaptureKeypair) Config {
	cfg := baseConfig(coord, minter, clk)
	cfg.CaptureKeypairs = map[int]*CaptureKeypair{kp.Generation: kp}
	cfg.CaptureGen = kp.Generation
	return cfg
}

// TestBind_TwoRoundRestore: a challenge response drives the second round —
// the secrets the coord challenged with come back HPKE-sealed under the
// RESTORE domain, answering the right challenge_id, and the key assertion
// rides BOTH rounds.
func TestBind_TwoRoundRestore(t *testing.T) {
	kp := testKeypair(t, 3)
	clk := &fakeClock{}
	coordFake := newFakeCoord(t)
	ch, wantSecret := challengeFor(t, kp, "c1", "ch-1")
	coordFake.bindSteps = []bindStep{
		{res: &coordinator.BindResult{RestoreChallenge: ch}},
		{res: &coordinator.BindResult{ComputerToken: "ct_ok", Epoch: 9}},
	}
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "g.r.a", KeyAssertion: "ka-jws"}}

	res, err := Bind(context.Background(), v2Config(coordFake, minter, clk, kp))
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if res.ComputerToken != "ct_ok" {
		t.Fatalf("res = %+v", res)
	}
	if len(coordFake.extras) != 2 {
		t.Fatalf("bind calls = %d, want 2", len(coordFake.extras))
	}
	if coordFake.extras[0].KeyAssertion != "ka-jws" || coordFake.extras[1].KeyAssertion != "ka-jws" {
		t.Fatal("key assertion must ride both rounds")
	}
	if coordFake.extras[0].RestoreSecrets != "" {
		t.Fatal("round 1 must not carry restore secrets")
	}
	// Open the round-2 payload as the pod would: restore-domain HPKE.
	raw, err := base64.RawURLEncoding.DecodeString(coordFake.extras[1].RestoreSecrets)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := coordFake.kp.Open(raw, []byte("pine.bind.restore.v1|pod-1|boot-1"), nil)
	if err != nil {
		t.Fatalf("pod could not open the restore secrets: %v", err)
	}
	var payload struct {
		ChallengeID string            `json:"challenge_id"`
		Secrets     map[string]string `json:"secrets"`
	}
	if err := json.Unmarshal(pt, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ChallengeID != "ch-1" {
		t.Fatalf("challenge_id = %q", payload.ChallengeID)
	}
	got, err := base64.RawURLEncoding.DecodeString(payload.Secrets["live"])
	if err != nil || string(got) != string(wantSecret) {
		t.Fatal("the unwrapped secret must round-trip exactly")
	}
	// The two rounds re-sent the SAME envelope (idempotency contract).
	if coordFake.ciphertexts[0] != coordFake.ciphertexts[1] {
		t.Fatal("round 2 must re-send the same bind envelope byte-for-byte")
	}
}

// TestBind_RechallengeResumes: a second challenge (latest moved) is
// answered on the same loop with the NEW challenge_id.
func TestBind_RechallengeResumes(t *testing.T) {
	kp := testKeypair(t, 3)
	clk := &fakeClock{}
	coordFake := newFakeCoord(t)
	ch1, _ := challengeFor(t, kp, "c1", "ch-1")
	ch2, _ := challengeFor(t, kp, "c1", "ch-2")
	coordFake.bindSteps = []bindStep{
		{res: &coordinator.BindResult{RestoreChallenge: ch1}},
		{res: &coordinator.BindResult{RestoreChallenge: ch2}},
		{res: &coordinator.BindResult{ComputerToken: "ct_ok"}},
	}
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "g.r.a", KeyAssertion: "ka"}}

	res, err := Bind(context.Background(), v2Config(coordFake, minter, clk, kp))
	if err != nil || res.ComputerToken != "ct_ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	open := func(sealed string) string {
		raw, _ := base64.RawURLEncoding.DecodeString(sealed)
		pt, err := coordFake.kp.Open(raw, []byte("pine.bind.restore.v1|pod-1|boot-1"), nil)
		if err != nil {
			t.Fatal(err)
		}
		var p struct {
			ChallengeID string `json:"challenge_id"`
		}
		_ = json.Unmarshal(pt, &p)
		return p.ChallengeID
	}
	if open(coordFake.extras[1].RestoreSecrets) != "ch-1" || open(coordFake.extras[2].RestoreSecrets) != "ch-2" {
		t.Fatal("each round must answer its own challenge generation")
	}
}

// TestBind_RestoreTerminalFailures: a challenge naming an unregistered
// keypair generation (or the wrong fingerprint for a registered one) is
// TERMINAL with an actionable message — retrying cannot help.
func TestBind_RestoreTerminalFailures(t *testing.T) {
	kp := testKeypair(t, 3)
	clk := &fakeClock{}
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "g.r.a", KeyAssertion: "ka"}}

	t.Run("unregistered generation", func(t *testing.T) {
		coordFake := newFakeCoord(t)
		old := testKeypair(t, 2) // state sealed to generation 2; only 3 registered
		ch, _ := challengeFor(t, old, "c1", "ch-1")
		coordFake.bindSteps = []bindStep{{res: &coordinator.BindResult{RestoreChallenge: ch}}}
		_, err := Bind(context.Background(), v2Config(coordFake, minter, clk, kp))
		if err == nil || !strings.Contains(err.Error(), "generation 2") {
			t.Fatalf("want a missing-generation terminal error, got %v", err)
		}
	})
	t.Run("fingerprint mismatch", func(t *testing.T) {
		coordFake := newFakeCoord(t)
		ch, _ := challengeFor(t, kp, "c1", "ch-1")
		ch.Components[0].PKFingerprint = "deadbeef"
		coordFake.bindSteps = []bindStep{{res: &coordinator.BindResult{RestoreChallenge: ch}}}
		_, err := Bind(context.Background(), v2Config(coordFake, minter, clk, kp))
		if err == nil || !strings.Contains(err.Error(), "wrong keypair") {
			t.Fatalf("want a fingerprint terminal error, got %v", err)
		}
	})
}

// TestBind_RestoreRoundCapIsLocalBackstop: a server that challenges forever
// hits the client-side round cap instead of looping unbounded.
func TestBind_RestoreRoundCapIsLocalBackstop(t *testing.T) {
	kp := testKeypair(t, 3)
	clk := &fakeClock{}
	coordFake := newFakeCoord(t)
	for range maxRestoreRounds + 1 {
		ch, _ := challengeFor(t, kp, "c1", "ch-again")
		coordFake.bindSteps = append(coordFake.bindSteps, bindStep{res: &coordinator.BindResult{RestoreChallenge: ch}})
	}
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "g.r.a", KeyAssertion: "ka"}}
	_, err := Bind(context.Background(), v2Config(coordFake, minter, clk, kp))
	if err == nil || !strings.Contains(err.Error(), "did not converge") {
		t.Fatalf("want the round-cap terminal error, got %v", err)
	}
}
