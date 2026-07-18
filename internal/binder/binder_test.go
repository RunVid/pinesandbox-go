package binder

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.pinesandbox.io/computer/internal/base/problem"
	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/bind"
	"go.pinesandbox.io/computer/internal/bindhpke"
	"go.pinesandbox.io/computer/internal/coordinator"
	"go.pinesandbox.io/computer/internal/statehpke"
	"go.pinesandbox.io/computer/internal/tokens"
)

type bindStep struct {
	res *coordinator.BindResult
	err error
}

// fakeCoord holds a REAL HPKE keypair so it can both serve a real ephem pubkey and OPEN the
// envelope the binder seals (proving the plaintext shape). Bind returns a programmed
// sequence of outcomes.
type fakeCoord struct {
	kp           *bindhpke.Keypair
	podUID, boot string
	pubkeyErrs   []error // optional per-call BindPubkey errors
	pubkeyCalls  int
	bindSteps    []bindStep
	bindCalls    int
	ciphertexts  []string                 // ciphertext seen on each Bind call
	extras       []coordinator.BindExtras // extras seen on each Bind call
	mu           sync.Mutex
}

func newFakeCoord(t *testing.T, steps ...bindStep) *fakeCoord {
	t.Helper()
	kp, err := bindhpke.GenerateKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	return &fakeCoord{kp: kp, podUID: "pod-1", boot: "boot-1", bindSteps: steps}
}

func (f *fakeCoord) BindPubkey(_ context.Context) (*coordinator.BindPubkey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.pubkeyCalls
	f.pubkeyCalls++
	if i < len(f.pubkeyErrs) && f.pubkeyErrs[i] != nil {
		return nil, f.pubkeyErrs[i]
	}
	return &coordinator.BindPubkey{PodUID: f.podUID, CoordBootID: f.boot, EphemPub: f.kp.PublicKeyRaw()}, nil
}

func (f *fakeCoord) Bind(_ context.Context, bindToken, podUID, boot, ciphertext string, extras coordinator.BindExtras) (*coordinator.BindResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ciphertexts = append(f.ciphertexts, ciphertext)
	f.extras = append(f.extras, extras)
	i := f.bindCalls
	f.bindCalls++
	if i >= len(f.bindSteps) {
		return nil, errors.New("fakeCoord: unexpected extra Bind call")
	}
	return f.bindSteps[i].res, f.bindSteps[i].err
}

// open decrypts a base64url ciphertext the binder produced, returning the plaintext.
func (f *fakeCoord) open(t *testing.T, ciphertext string) bindPlaintext {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(ciphertext)
	if err != nil {
		t.Fatalf("ciphertext not base64url: %v", err)
	}
	pt, err := f.kp.Open(raw, bindhpke.Info(f.podUID, f.boot), nil)
	if err != nil {
		t.Fatalf("coord could not open the envelope: %v", err)
	}
	var p bindPlaintext
	if err := json.Unmarshal(pt, &p); err != nil {
		t.Fatalf("plaintext not JSON: %v", err)
	}
	return p
}

type fakeMinter struct {
	creds *tokens.AttachCredentials
	err   error
	calls int
	raw   bool
}

func (f *fakeMinter) Credentials(_ context.Context, _ tokens.CredentialsRequest) (*tokens.AttachCredentials, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.raw || f.creds == nil {
		return f.creds, nil
	}
	creds := *f.creds
	if creds.KeyAssertion == "" {
		creds.KeyAssertion = "ka"
	}
	if creds.BindingRevision == 0 {
		creds.BindingRevision = 1
	}
	return &creds, nil
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func baseConfig(coord *fakeCoord, minter *fakeMinter, clk *fakeClock) Config {
	pk, sk, err := statehpke.GenerateKeypair()
	if err != nil {
		panic(err)
	}
	return Config{
		Coord: coord, Minter: minter,
		ComputerID: "c1", Key: []byte("0123456789abcdef0123456789abcdef"), SandboxID: "sb-1",
		ReadyTimeout:    5 * time.Second,
		CaptureKeypairs: map[int]*CaptureKeypair{1: {Generation: 1, PK: pk, SK: sk}},
		CaptureGen:      1,
		Clock:           clk.now,
		// The sleeper advances the clock, so readiness retries eventually cross the deadline.
		Sleeper: func(_ context.Context, d time.Duration) error { clk.advance(d); return nil },
	}
}

func apiErr(status int, ptype string) error {
	return &problem.APIError{Status: status, ProblemType: ptype}
}

func TestBind_HappyPath(t *testing.T) {
	coord := newFakeCoord(t, bindStep{res: &coordinator.BindResult{ComputerToken: "ct_ok", Epoch: 3}})
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "bg"}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}

	res, err := Bind(context.Background(), baseConfig(coord, minter, clk))
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if res.ComputerToken != "ct_ok" || res.Epoch != 3 {
		t.Errorf("res = %+v", res)
	}
	if coord.pubkeyCalls != 1 || minter.calls != 1 || coord.bindCalls != 1 {
		t.Errorf("calls: pubkey=%d minter=%d bind=%d, want 1/1/1", coord.pubkeyCalls, minter.calls, coord.bindCalls)
	}
	// The sealed plaintext carries the versioned current key + broker grant.
	p := coord.open(t, coord.ciphertexts[0])
	if p.ComputerKeyCurrent.Version != CurrentKeyVersion || p.BrokerGrant != "bg" || p.ComputerKeyForRestore != nil {
		t.Errorf("plaintext = %+v", p)
	}
	wantKey := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	if p.ComputerKeyCurrent.Bytes != wantKey {
		t.Errorf("current key bytes = %q, want %q", p.ComputerKeyCurrent.Bytes, wantKey)
	}
}

func TestBind_CustomMinterCannotOmitV3Assertion(t *testing.T) {
	coord := newFakeCoord(t)
	minter := &fakeMinter{raw: true, creds: &tokens.AttachCredentials{
		BindToken:       "bt",
		BrokerGrant:     "bg",
		BindingRevision: 1,
	}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	authorizedRevision := int64(0)
	cfg := baseConfig(coord, minter, clk)
	cfg.OnAuthorized = func(revision int64) { authorizedRevision = revision }

	_, err := Bind(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "key_assertion") {
		t.Fatalf("err = %v, want missing key_assertion", err)
	}
	if authorizedRevision != 1 {
		t.Fatalf("authorized revision = %d, want committed revision 1", authorizedRevision)
	}
	if coord.bindCalls != 0 {
		t.Fatal("incomplete v3 authorization must fail before coordinator bind")
	}
}

func TestBind_PriorKeySeedsRestore(t *testing.T) {
	coord := newFakeCoord(t, bindStep{res: &coordinator.BindResult{ComputerToken: "ct"}})
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "bg"}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	cfg := baseConfig(coord, minter, clk)
	cfg.PriorKeys = map[int][]byte{2: []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), 5: []byte("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")}

	if _, err := Bind(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	p := coord.open(t, coord.ciphertexts[0])
	if p.ComputerKeyForRestore == nil || p.ComputerKeyForRestore.Version != 5 {
		t.Fatalf("restore key = %+v, want the max version (5)", p.ComputerKeyForRestore)
	}
}

func TestBind_ReadinessRetryReusesEnvelope(t *testing.T) {
	coord := newFakeCoord(t,
		bindStep{err: apiErr(404, "")},                                    // plain 404 → readiness
		bindStep{err: apiErr(503, "")},                                    // plain 503 → readiness
		bindStep{res: &coordinator.BindResult{ComputerToken: "ct_ready"}}, // success
	)
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "bg"}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}

	res, err := Bind(context.Background(), baseConfig(coord, minter, clk))
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if res.ComputerToken != "ct_ready" {
		t.Errorf("res = %+v", res)
	}
	// Readiness reuses the envelope: minted ONCE, three bind attempts, all same ciphertext.
	if minter.calls != 1 || coord.pubkeyCalls != 1 {
		t.Errorf("mint reused? pubkey=%d minter=%d, want 1/1", coord.pubkeyCalls, minter.calls)
	}
	if coord.bindCalls != 3 {
		t.Errorf("bindCalls = %d, want 3", coord.bindCalls)
	}
	if coord.ciphertexts[0] != coord.ciphertexts[1] || coord.ciphertexts[1] != coord.ciphertexts[2] {
		t.Error("readiness retries must re-send the SAME envelope byte-for-byte")
	}
}

func TestBind_PodIdentityShiftIsTerminalAndDoesNotRemint(t *testing.T) {
	coord := newFakeCoord(t, bindStep{err: apiErr(409, "/errors/stale-coord-boot-id")})
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "bg"}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}

	_, err := Bind(context.Background(), baseConfig(coord, minter, clk))
	var be *bind.BindError
	if !errors.As(err, &be) {
		t.Fatalf("err = %T (%v), want *bind.BindError", err, err)
	}
	if coord.pubkeyCalls != 1 || minter.calls != 1 || coord.bindCalls != 1 {
		t.Errorf("identity shift retried or re-minted: pubkey=%d minter=%d bind=%d, want 1/1/1",
			coord.pubkeyCalls, minter.calls, coord.bindCalls)
	}
}

func TestBind_TransientRestoreFailureReusesCommittedEnvelope(t *testing.T) {
	coord := newFakeCoord(t,
		bindStep{err: &problem.APIError{Status: 503, ProblemType: "/errors/bind-restore-failed", Detail: "broker timeout"}},
		bindStep{res: &coordinator.BindResult{ComputerToken: "ct_restored"}},
	)
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "bg"}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}

	res, err := Bind(context.Background(), baseConfig(coord, minter, clk))
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if res.ComputerToken != "ct_restored" {
		t.Errorf("res = %+v", res)
	}
	if minter.calls != 1 || coord.pubkeyCalls != 1 || coord.bindCalls != 2 {
		t.Errorf("transient restore calls: pubkey=%d minter=%d bind=%d, want 1/1/2",
			coord.pubkeyCalls, minter.calls, coord.bindCalls)
	}
	if coord.ciphertexts[0] != coord.ciphertexts[1] {
		t.Error("transient restore retry must reuse the committed envelope")
	}
}

func TestBind_TerminalNoRetry(t *testing.T) {
	coord := newFakeCoord(t, bindStep{err: apiErr(403, "/errors/bind-rejected")})
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "bg"}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}

	_, err := Bind(context.Background(), baseConfig(coord, minter, clk))
	var ba *bind.BindAuthError
	if !errors.As(err, &ba) {
		t.Fatalf("err = %T (%v), want *bind.BindAuthError", err, err)
	}
	if coord.bindCalls != 1 {
		t.Errorf("terminal was retried (%d bind calls)", coord.bindCalls)
	}
}

func TestBind_ReadinessDeadlineTimeout(t *testing.T) {
	// Always-503: readiness, never succeeds → deadline elapses → BindTimeoutError.
	steps := make([]bindStep, 50)
	for i := range steps {
		steps[i] = bindStep{err: apiErr(503, "")}
	}
	coord := newFakeCoord(t, steps...)
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "bg"}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}

	_, err := Bind(context.Background(), baseConfig(coord, minter, clk))
	var bt *bind.BindTimeoutError
	if !errors.As(err, &bt) {
		t.Fatalf("err = %T (%v), want *bind.BindTimeoutError", err, err)
	}
}

func TestBind_ExpiredRestoreChallengeRestartsWithoutRemint(t *testing.T) {
	steps := []bindStep{
		{err: apiErr(409, "/errors/bind-restore-no-challenge")},
		{err: apiErr(409, "/errors/bind-restore-no-challenge")},
		{res: &coordinator.BindResult{ComputerToken: "ct_restarted"}},
	}
	coord := newFakeCoord(t, steps...)
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "bg"}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	cfg := baseConfig(coord, minter, clk)
	cfg.MaxBindAttempts = 3

	res, err := Bind(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if res.ComputerToken != "ct_restarted" {
		t.Errorf("res = %+v", res)
	}
	if minter.calls != 1 || coord.pubkeyCalls != 1 || coord.bindCalls != 3 {
		t.Errorf("restore restart calls: pubkey=%d minter=%d bind=%d, want 1/1/3",
			coord.pubkeyCalls, minter.calls, coord.bindCalls)
	}
	for i := 1; i < len(coord.ciphertexts); i++ {
		if coord.ciphertexts[i] != coord.ciphertexts[0] {
			t.Fatal("restore restart re-minted the committed attach authorization")
		}
	}
}

func TestBind_ExpiredRestoreChallengeExhausted(t *testing.T) {
	steps := make([]bindStep, 3)
	for i := range steps {
		steps[i] = bindStep{err: apiErr(409, "/errors/bind-restore-no-challenge")}
	}
	coord := newFakeCoord(t, steps...)
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "bg"}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	cfg := baseConfig(coord, minter, clk)
	cfg.MaxBindAttempts = 2

	_, err := Bind(context.Background(), cfg)
	var be *bind.BindError
	if !errors.As(err, &be) {
		t.Fatalf("err = %T (%v), want *bind.BindError", err, err)
	}
	if coord.bindCalls != 2 || minter.calls != 1 {
		t.Errorf("calls = bind %d, mint %d; want 2/1", coord.bindCalls, minter.calls)
	}
}

// TestBind_ContextCancelledDuringBackoff proves a cancelled context aborts the retry loop
// instead of sleeping out the full readiness deadline (the ctx-aware Sleeper fix).
func TestBind_ContextCancelledDuringBackoff(t *testing.T) {
	steps := make([]bindStep, 50)
	for i := range steps {
		steps[i] = bindStep{err: apiErr(503, "")} // readiness forever
	}
	coord := newFakeCoord(t, steps...)
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "bg"}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	cfg := baseConfig(coord, minter, clk)

	ctx, cancel := context.WithCancel(context.Background())
	// The sleeper cancels mid-backoff and reports it, exactly as wait.Sleep would on a
	// cancelled context.
	cfg.Sleeper = func(c context.Context, _ time.Duration) error { cancel(); return c.Err() }

	_, err := Bind(ctx, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if coord.bindCalls > 2 {
		t.Errorf("bind retried %d times after cancel — should stop promptly", coord.bindCalls)
	}
}

func TestBind_MintTerminalError(t *testing.T) {
	coord := newFakeCoord(t) // Bind never reached
	minter := &fakeMinter{err: &tokens.AttachCredentialsError{}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}

	_, err := Bind(context.Background(), baseConfig(coord, minter, clk))
	var ace *tokens.AttachCredentialsError
	if !errors.As(err, &ace) {
		t.Fatalf("err = %T (%v), want *tokens.AttachCredentialsError", err, err)
	}
	if coord.bindCalls != 0 {
		t.Errorf("bind should not be attempted after a terminal mint error")
	}
}

func TestBind_MintReadinessRetry(t *testing.T) {
	// BindPubkey faults transiently (coord not up) → readiness → retry mint → succeeds.
	coord := newFakeCoord(t, bindStep{res: &coordinator.BindResult{ComputerToken: "ct"}})
	coord.pubkeyErrs = []error{&transport.ConnectionError{Op: "GET /v1/coord/bind-pubkey", Msg: "refused"}}
	minter := &fakeMinter{creds: &tokens.AttachCredentials{BindToken: "bt", BrokerGrant: "bg"}}
	clk := &fakeClock{t: time.Unix(1700000000, 0)}

	res, err := Bind(context.Background(), baseConfig(coord, minter, clk))
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if res.ComputerToken != "ct" {
		t.Errorf("res = %+v", res)
	}
	if coord.pubkeyCalls != 2 {
		t.Errorf("pubkeyCalls = %d, want 2 (transient mint fault retried)", coord.pubkeyCalls)
	}
}
