// Package binder runs the Computer bind handshake: fetch the coord's ephemeral pubkey,
// mint per-attach credentials, HPKE-seal {computer_key, broker_grant}, and POST the bind —
// with the two converging retry budgets of the bind decision table. Readiness failures (a
// just-created pod whose coord isn't routable yet) retry DEADLINE-bound, REUSING the same
// envelope byte-for-byte (coord keys idempotency on jti+ciphertext). Race failures (a pod
// identity shift) retry ATTEMPT-bound, RE-MINTING. Terminal failures never retry. The
// readiness/race/terminal decision is internal/bind.Classify; this package is the I/O
// orchestration around it. Domain package (composes coordinator + tokens + bindhpke + bind).
package binder

import (
	"context"
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
	"go.pinesandbox.io/computer/internal/tokens"
)

// CurrentKeyVersion is the version stamped on computer_key_current (rotation only).
const CurrentKeyVersion = 1

const (
	// DefaultMaxBindAttempts bounds the race (re-mint) budget.
	DefaultMaxBindAttempts = 3
	// DefaultReadyTimeout bounds the readiness (convergence) budget.
	DefaultReadyTimeout = 120 * time.Second
)

// Coordinator is the subset of the coordinator client the handshake needs.
type Coordinator interface {
	BindPubkey(ctx context.Context) (*coordinator.BindPubkey, error)
	Bind(ctx context.Context, bindToken, podUID, coordBootID, ciphertext string) (*coordinator.BindResult, error)
}

// Minter mints per-attach credentials (bind_token + broker_grant).
type Minter interface {
	Credentials(ctx context.Context, req tokens.CredentialsRequest) (*tokens.AttachCredentials, error)
}

// Config is the input to Bind.
type Config struct {
	Coord  Coordinator
	Minter Minter

	ComputerID string
	Key        []byte         // 32-byte computer_key_current
	PriorKeys  map[int][]byte // version → bytes; the max version seeds computer_key_for_restore
	SandboxID  string
	Profile    string
	TTLSeconds *int

	MaxBindAttempts int           // default DefaultMaxBindAttempts
	ReadyTimeout    time.Duration // default DefaultReadyTimeout

	// Injectable for deterministic tests; default to time.Now / wait.Sleep. Sleeper
	// returns ctx.Err() when the context is cancelled mid-backoff, which aborts the loop.
	Clock   func() time.Time
	Sleeper func(context.Context, time.Duration) error
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
	bindToken   string
	podUID      string
	coordBootID string
	ciphertext  string // base64url, no padding
}

// Bind runs the handshake to completion, returning the pod's ct_ + epoch, or a typed bind
// error. Readiness retries reuse the held envelope; race retries drop + re-mint it.
func Bind(ctx context.Context, cfg Config) (*coordinator.BindResult, error) {
	cfg.defaults()
	if len(cfg.Key) == 0 {
		return nil, fmt.Errorf("pinesandbox: bind requires a computer key")
	}

	deadline := cfg.Clock().Add(cfg.ReadyTimeout)
	raceAttempts := 0
	convergeWaits := 0
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

		res, err := cfg.Coord.Bind(ctx, env.bindToken, env.podUID, env.coordBootID, env.ciphertext)
		if err == nil {
			return res, nil
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
		case bind.ClassRace:
			raceAttempts++
			if raceAttempts < cfg.MaxBindAttempts {
				env = nil // the pod identity shifted — drop the envelope, re-mint
				if serr := cfg.Sleeper(ctx, time.Duration(raceAttempts)*500*time.Millisecond); serr != nil {
					return nil, serr
				}
				continue
			}
			return nil, bind.NewBindError(0, fmt.Sprintf("bind did not converge after %d attempts (pod identity kept shifting)", cfg.MaxBindAttempts), err)
		default: // terminal
			return nil, termErr
		}
	}
}

// mintEnvelope fetches the pod's bind identity, mints single-use attach credentials, and
// HPKE-seals the bind payload — everything a bind POST needs, held so readiness retries can
// re-send it verbatim.
func mintEnvelope(ctx context.Context, cfg Config) (*envelope, error) {
	pubkey, err := cfg.Coord.BindPubkey(ctx)
	if err != nil {
		return nil, err
	}
	creds, err := cfg.Minter.Credentials(ctx, tokens.CredentialsRequest{
		ComputerID: cfg.ComputerID, PodUID: pubkey.PodUID, CoordBootID: pubkey.CoordBootID,
		SandboxID: cfg.SandboxID, Profile: cfg.Profile, TTLSeconds: cfg.TTLSeconds,
	})
	if err != nil {
		return nil, err
	}

	plaintext, err := json.Marshal(bindPlaintext{
		ComputerKeyCurrent:    wireKey{Version: CurrentKeyVersion, Bytes: b64(cfg.Key)},
		ComputerKeyForRestore: restoreKey(cfg.PriorKeys),
		BrokerGrant:           creds.BrokerGrant,
	})
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: marshal bind plaintext: %w", err)
	}
	ct, err := bindhpke.Seal(pubkey.EphemPub, plaintext, bindhpke.Info(pubkey.PodUID, pubkey.CoordBootID), nil)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: seal bind envelope: %w", err)
	}
	return &envelope{
		bindToken:   creds.BindToken,
		podUID:      pubkey.PodUID,
		coordBootID: pubkey.CoordBootID,
		ciphertext:  b64(ct),
	}, nil
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
	if errors.As(err, &ace) || errors.As(err, &uce) || errors.As(err, &cre) {
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
