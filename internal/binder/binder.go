// Package binder runs the Computer bind handshake: fetch the coord's ephemeral pubkey,
// mint per-attach credentials, HPKE-seal {computer_key, broker_grant}, and POST the bind —
// with the converging retry budgets of the bind decision table. Once Portal commits an
// authorization, all same-pod retries REUSE that envelope byte-for-byte (coord keys
// idempotency on jti+ciphertext). An expired restore challenge restarts only the two-round
// restore transaction with the same authorization. Pod-identity shifts and other terminal
// failures require a fresh sandbox. Domain package (composes coordinator + tokens +
// bindhpke + bind).
package binder

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/base/wait"
	"go.pinesandbox.io/computer/internal/bind"
	"go.pinesandbox.io/computer/internal/bindhpke"
	"go.pinesandbox.io/computer/internal/coordinator"
	"go.pinesandbox.io/computer/internal/statehpke"
	"go.pinesandbox.io/computer/internal/tokens"
)

// CurrentKeyVersion is the version stamped on computer_key_current (rotation only).
const CurrentKeyVersion = 1

const (
	// DefaultMaxBindAttempts bounds restore-transaction attempts after an expired challenge.
	DefaultMaxBindAttempts = 3
	// DefaultReadyTimeout bounds the readiness (convergence) budget.
	DefaultReadyTimeout = 120 * time.Second
)

// Coordinator is the subset of the coordinator client the handshake needs.
type Coordinator interface {
	BindPubkey(ctx context.Context) (*coordinator.BindPubkey, error)
	Bind(ctx context.Context, bindToken, podUID, coordBootID, ciphertext string, extras coordinator.BindExtras) (*coordinator.BindResult, error)
}

// CaptureKeypair is the binder's view of one capture-keypair generation
// (the facade owns the public type; this keeps the dependency direction).
type CaptureKeypair struct {
	Generation int
	PK         []byte
	SK         []byte
}

// maxRestoreRounds bounds the challenge-answer loop client-side. Coord's own
// generation cap (4) refuses first in any legitimate run; this is the local
// backstop against a misbehaving server issuing challenges forever.
const maxRestoreRounds = 6

// Minter mints per-attach credentials (bind_token + broker_grant).
type Minter interface {
	Credentials(ctx context.Context, req tokens.CredentialsRequest) (*tokens.AttachCredentials, error)
}

// Config is the input to Bind.
type Config struct {
	Coord  Coordinator
	Minter Minter

	ComputerID      string
	Key             []byte         // 32-byte computer_key_current
	PriorKeys       map[int][]byte // version → bytes; the max version seeds computer_key_for_restore
	SandboxID       string
	BindingRevision int64

	// Ephemeral binds an access lease ONLY: no persistence. The attach
	// carries no capture keypair, mints without pk_computer/key_generation,
	// forwards no key_assertion, and the HPKE-sealed bind payload carries the
	// broker_grant WITHOUT any computer key material.
	Ephemeral bool

	// CaptureKeypairs (generation → keypair) + CaptureGen opt the attach
	// into asymmetric state encryption: the CURRENT generation's pk is
	// submitted at mint (the portal answers with the key_assertion), and
	// the restore challenge is answered with the matching generation's sk.
	CaptureKeypairs map[int]*CaptureKeypair
	CaptureGen      int

	MaxBindAttempts int           // restore-transaction attempts; default DefaultMaxBindAttempts
	ReadyTimeout    time.Duration // default DefaultReadyTimeout

	// Injectable for deterministic tests; default to time.Now / wait.Sleep. Sleeper
	// returns ctx.Err() when the context is cancelled mid-backoff, which aborts the loop.
	Clock   func() time.Time
	Sleeper func(context.Context, time.Duration) error
	// Called immediately after Portal commits an attach authorization. The
	// caller persists the revision even if the later coordinator bind fails.
	OnAuthorized func(revision int64)
}

func (c *Config) defaults() {
	if c.MaxBindAttempts <= 0 {
		c.MaxBindAttempts = DefaultMaxBindAttempts
	}
	if c.ReadyTimeout <= 0 {
		c.ReadyTimeout = DefaultReadyTimeout
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}
	if c.Sleeper == nil {
		c.Sleeper = wait.Sleep
	}
}

type envelope struct {
	bindToken       string
	podUID          string
	coordBootID     string
	ciphertext      string // base64url, no padding
	keyAssertion    string // portal-signed, forwarded verbatim (v2 attaches)
	bindingRevision int64
	ephemPub        []byte // pod bind pubkey — round 2 seals the secrets to it
	// restoreSecrets is the current round-2 payload; rebuilt per challenge
	// and REUSED verbatim on readiness retries (idempotent replay).
	restoreSecrets string
}

// Bind runs the handshake to completion, returning the pod's ct_ + epoch, or a typed bind
// error. A sandbox gets exactly one Portal authorization; every safe same-pod retry reuses
// that held envelope.
func Bind(ctx context.Context, cfg Config) (*coordinator.BindResult, error) {
	cfg.defaults()
	if !cfg.Ephemeral && len(cfg.Key) == 0 {
		return nil, fmt.Errorf("pinesandbox: bind requires a computer key")
	}
	if cfg.BindingRevision < 0 {
		return nil, fmt.Errorf("pinesandbox: binding revision cannot be negative")
	}
	if !cfg.Ephemeral && cfg.CaptureGen <= 0 {
		return nil, fmt.Errorf("pinesandbox: v3 attach requires an asymmetric capture keypair")
	}

	deadline := cfg.Clock().Add(cfg.ReadyTimeout)
	restoreAttempts := 1
	convergeWaits := 0
	restoreRounds := 0
	var env *envelope

	for {
		if err := ctx.Err(); err != nil {
			return nil, err // caller cancelled — don't start another attempt
		}
		if env == nil {
			e, err := mintEnvelope(ctx, cfg)
			if err != nil {
				// A mint failure: a transport blip / coord-not-ready is readiness; a portal
				// auth/ownership failure is terminal.
				if class, _ := classify(err); class == bind.ClassReadiness && cfg.Clock().Before(deadline) {
					convergeWaits++
					if serr := cfg.Sleeper(ctx, convergenceBackoff(convergeWaits)); serr != nil {
						return nil, serr
					}
					continue
				} else if class == bind.ClassReadiness {
					return nil, bind.NewBindTimeoutError("Computer pod did not become bindable within the readiness deadline", err)
				}
				return nil, err
			}
			env = e
		}

		res, err := cfg.Coord.Bind(ctx, env.bindToken, env.podUID, env.coordBootID, env.ciphertext,
			coordinator.BindExtras{KeyAssertion: env.keyAssertion, RestoreSecrets: env.restoreSecrets})
		if err == nil {
			if res.RestoreChallenge == nil {
				res.BindingRevision = env.bindingRevision
				return res, nil
			}
			// The two-round v3 restore: unwrap each component's secret with
			// the matching keypair generation, seal the set to THIS pod's
			// bind key under the restore domain, and re-send the SAME
			// envelope with restore_secrets. Every resumable outcome is
			// another challenge on this same loop; readiness retries reuse
			// the built payload verbatim (idempotent replay).
			restoreRounds++
			if restoreRounds > maxRestoreRounds {
				return nil, bind.NewBindError(0, fmt.Sprintf("bind restore did not converge after %d challenges", maxRestoreRounds), nil)
			}
			sealed, aerr := answerChallenge(cfg, env, res.RestoreChallenge)
			if aerr != nil {
				return nil, aerr // terminal: a missing generation / bad material never self-heals
			}
			env.restoreSecrets = sealed
			continue
		}

		if isRestoreChallengeExpired(err) {
			if restoreAttempts >= cfg.MaxBindAttempts {
				return nil, bind.NewBindError(0, fmt.Sprintf("bind restore challenge expired after %d attempts", cfg.MaxBindAttempts), err)
			}
			// Only the coordinator's short-lived challenge expired. The Portal receipt,
			// bind token, JTI, and ciphertext remain the one committed authorization for
			// this sandbox. Restart round one without re-minting any of them.
			restoreAttempts++
			restoreRounds = 0
			env.restoreSecrets = ""
			if serr := cfg.Sleeper(ctx, time.Duration(restoreAttempts-1)*500*time.Millisecond); serr != nil {
				return nil, serr
			}
			continue
		}

		class, termErr := classify(err)
		switch class {
		case bind.ClassReadiness:
			if cfg.Clock().Before(deadline) {
				convergeWaits++
				if serr := cfg.Sleeper(ctx, convergenceBackoff(convergeWaits)); serr != nil { // reuse env
					return nil, serr
				}
				continue
			}
			return nil, bind.NewBindTimeoutError("Computer pod did not become bindable within the readiness deadline", err)
		default: // terminal
			return nil, termErr
		}
	}
}

func isRestoreChallengeExpired(err error) bool {
	var ae *problem.APIError
	return errors.As(err, &ae) && ae.ProblemType == "/errors/bind-restore-no-challenge"
}

// mintEnvelope fetches the pod's bind identity, mints single-use attach credentials, and
// HPKE-seals the bind payload — everything a bind POST needs, held so readiness retries can
// re-send it verbatim.
func mintEnvelope(ctx context.Context, cfg Config) (*envelope, error) {
	pubkey, err := cfg.Coord.BindPubkey(ctx)
	if err != nil {
		return nil, err
	}
	req := tokens.CredentialsRequest{
		ComputerID: cfg.ComputerID, PodUID: pubkey.PodUID, CoordBootID: pubkey.CoordBootID,
		SandboxID: cfg.SandboxID, ExpectedBindingRevision: cfg.BindingRevision,
		IdempotencyKey: attachIdempotencyKey(
			cfg.ComputerID, cfg.SandboxID, pubkey.PodUID, pubkey.CoordBootID,
		),
	}
	if cfg.Ephemeral {
		// Access-lease-only attach: no capture identity is submitted, so the
		// portal mints without advancing a per-Computer key floor.
		req.Ephemeral = true
	} else {
		cur, ok := cfg.CaptureKeypairs[cfg.CaptureGen]
		if !ok {
			return nil, fmt.Errorf("pinesandbox: current capture keypair generation %d not registered", cfg.CaptureGen)
		}
		req.PKComputer = b64(cur.PK)
		req.KeyGeneration = cur.Generation
	}
	creds, err := cfg.Minter.Credentials(ctx, req)
	if err != nil {
		return nil, err
	}
	if creds == nil || creds.BindingRevision <= cfg.BindingRevision {
		return nil, fmt.Errorf("pinesandbox: attach-credentials provider returned a non-advancing binding revision")
	}
	if cfg.OnAuthorized != nil {
		cfg.OnAuthorized(creds.BindingRevision)
	}
	// Preserve the committed revision above even if a custom/in-process issuer
	// returns an incomplete receipt. v3 must never fall through to coord without
	// its assertion and silently behave like an older attach. An ephemeral
	// attach carries no capture identity, so it never receives a key_assertion.
	if creds.BindToken == "" || creds.BrokerGrant == "" || (!cfg.Ephemeral && creds.KeyAssertion == "") {
		return nil, fmt.Errorf("pinesandbox: attach-credentials provider result missing bind_token/broker_grant/key_assertion")
	}

	var plaintext []byte
	if cfg.Ephemeral {
		// Access-lease-only: seal the broker grant WITHOUT any computer key.
		plaintext, err = json.Marshal(ephemeralPlaintext{BrokerGrant: creds.BrokerGrant})
	} else {
		plaintext, err = json.Marshal(bindPlaintext{
			ComputerKeyCurrent:    wireKey{Version: CurrentKeyVersion, Bytes: b64(cfg.Key)},
			ComputerKeyForRestore: restoreKey(cfg.PriorKeys),
			BrokerGrant:           creds.BrokerGrant,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal bind plaintext: %w", err)
	}
	ct, err := bindhpke.Seal(pubkey.EphemPub, plaintext, bindhpke.Info(pubkey.PodUID, pubkey.CoordBootID), nil)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: seal bind envelope: %w", err)
	}
	return &envelope{
		bindToken:       creds.BindToken,
		podUID:          pubkey.PodUID,
		coordBootID:     pubkey.CoordBootID,
		ciphertext:      b64(ct),
		keyAssertion:    creds.KeyAssertion,
		bindingRevision: creds.BindingRevision,
		ephemPub:        pubkey.EphemPub,
	}, nil
}

func attachIdempotencyKey(computerID, sandboxID, podUID, coordBootID string) string {
	sum := sha256.Sum256([]byte(computerID + "\x00" + sandboxID + "\x00" + podUID + "\x00" + coordBootID))
	return "attach-" + fmt.Sprintf("%x", sum[:])
}

// answerChallenge unwraps each challenged component's secret with the
// matching keypair generation and seals the set to the pod's bind key under
// the restore domain. Failures are terminal — a missing superseded
// generation or corrupt challenge material never self-heals by retrying.
func answerChallenge(cfg Config, env *envelope, ch *coordinator.RestoreChallenge) (string, error) {
	secrets := map[string]string{}
	for _, comp := range ch.Components {
		kp, ok := cfg.CaptureKeypairs[comp.RecipientKeyVersion]
		if !ok {
			return "", bind.NewBindError(0, fmt.Sprintf(
				"restore needs capture keypair generation %d for component %s — register the superseded keypair (AddPriorCaptureKeypair) or its state cannot be restored",
				comp.RecipientKeyVersion, comp.Component), nil)
		}
		if fp := statehpke.Fingerprint(kp.PK); fp != comp.PKFingerprint {
			return "", bind.NewBindError(0, fmt.Sprintf(
				"restore challenge for component %s names fingerprint %s but keypair generation %d has %s — wrong keypair registered",
				comp.Component, comp.PKFingerprint, comp.RecipientKeyVersion, fp), nil)
		}
		enc, err := base64.RawURLEncoding.DecodeString(comp.HPKEEnc)
		if err != nil {
			return "", bind.NewBindError(0, "restore challenge hpke_enc is not base64url", err)
		}
		sealedS, err := base64.RawURLEncoding.DecodeString(comp.HPKESealedS)
		if err != nil {
			return "", bind.NewBindError(0, "restore challenge hpke_sealed_s is not base64url", err)
		}
		s, err := statehpke.OpenComponentSecret(kp.SK, enc, sealedS,
			cfg.ComputerID, comp.Component, comp.ID, comp.AttachEpoch, comp.RecipientKeyVersion, comp.PKFingerprint)
		if err != nil {
			return "", bind.NewBindError(0, fmt.Sprintf("restore secret unwrap failed for component %s", comp.Component), err)
		}
		secrets[comp.Component] = b64(s)
		zero(s)
	}
	plaintext, err := json.Marshal(map[string]any{"challenge_id": ch.ChallengeID, "secrets": secrets})
	if err != nil {
		return "", fmt.Errorf("pinesandbox: marshal restore secrets: %w", err)
	}
	// The unwrapped secrets live on only inside the sealed payload —
	// wipe the plaintext (the b64 strings in `secrets` are unreachable
	// after return and Go strings can't be wiped; the raw slices above
	// were zeroed as they were consumed).
	defer zero(plaintext)
	sealed, err := bindhpke.Seal(env.ephemPub, plaintext, bindhpke.RestoreInfo(env.podUID, env.coordBootID), nil)
	if err != nil {
		return "", fmt.Errorf("pinesandbox: seal restore secrets: %w", err)
	}
	return b64(sealed), nil
}

// zero wipes a secret-bearing slice.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

type wireKey struct {
	Version int    `json:"version"`
	Bytes   string `json:"bytes"`
}

type bindPlaintext struct {
	ComputerKeyCurrent    wireKey  `json:"computer_key_current"`
	ComputerKeyForRestore *wireKey `json:"computer_key_for_restore"`
	BrokerGrant           string   `json:"broker_grant"`
}

// ephemeralPlaintext is the bind payload for an access-lease-only (ephemeral)
// attach: it carries the broker grant but NO computer key material, so the pod
// binds a lease without any persistence identity.
type ephemeralPlaintext struct {
	BrokerGrant string `json:"broker_grant"`
}

// restoreKey returns the highest-version prior key as the restore key (so a snapshot sealed
// under the old key still decrypts), or nil when there are none.
func restoreKey(prior map[int][]byte) *wireKey {
	maxV, found := 0, false
	for v := range prior {
		if !found || v > maxV {
			maxV, found = v, true
		}
	}
	if !found {
		return nil
	}
	return &wireKey{Version: maxV, Bytes: b64(prior[maxV])}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// classify maps a handshake error to its retry class. Order matters: transport faults are
// readiness; a portal mint error (which WRAPS an APIError) is terminal and must be caught
// before the generic APIError unwrap; a raw coord *problem.APIError goes through
// bind.Classify; anything else is terminal.
func classify(err error) (bind.Class, error) {
	var te *transport.TimeoutError
	var ce *transport.ConnectionError
	if errors.As(err, &te) || errors.As(err, &ce) {
		return bind.ClassReadiness, err
	}
	// Portal mint failures are terminal (auth/ownership/registration) — not a pod-readiness
	// or coord-race signal — even though they wrap an APIError.
	var ace *tokens.AttachCredentialsError
	var uce *tokens.UnknownComputerError
	var cre *tokens.ComputerRegistrationError
	var brc *tokens.BindingRevisionConflictError
	if errors.As(err, &ace) || errors.As(err, &uce) || errors.As(err, &cre) || errors.As(err, &brc) {
		return bind.ClassTerminal, err
	}
	var ae *problem.APIError
	if errors.As(err, &ae) {
		return bind.Classify(bind.Outcome{Status: ae.Status, ProblemType: ae.ProblemType, Message: ae.Detail})
	}
	return bind.ClassTerminal, err
}

// convergenceBackoff is the capped linear readiness wait: min(0.5·n, 3s).
func convergenceBackoff(waitCount int) time.Duration {
	d := time.Duration(waitCount) * 500 * time.Millisecond
	if d > 3*time.Second {
		return 3 * time.Second
	}
	return d
}
