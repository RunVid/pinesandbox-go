package pinesandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.pinesandbox.io/computer/internal/base/wait"
	"go.pinesandbox.io/computer/internal/bind"
	"go.pinesandbox.io/computer/internal/binder"
	"go.pinesandbox.io/computer/internal/coordinator"
	"go.pinesandbox.io/computer/internal/tokens"
)

// CurrentKeyVersion is the version stamped on the current state key (rotation only).
const CurrentKeyVersion = binder.CurrentKeyVersion

const (
	defaultSandboxLifetime = 300 * time.Second
	defaultReadyTimeout    = 300 * time.Second
	attachCleanupTimeout   = 30 * time.Second
)

// ComputerLocation is the browser egress policy for one Computer binding.
// Country is canonical ISO 3166-1 alpha-2 uppercase (for example "US").
// Mode may be "direct" instead of Country to bypass regional upstream proxies.
// Portal owns the supported-country catalog; the SDK only validates the fixed
// standard and forwards it during attach authorization.
type ComputerLocation struct {
	Country string `json:"country,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

// StartOptions configures a fresh runtime for saved credentials. Identity, keys
// and ephemeral mode belong to Credentials; Start reads the current binding.
type StartOptions struct {
	Timeout          time.Duration // runtime lifetime, default 300s
	ReadyTimeout     time.Duration // allocation readiness budget, default 300s
	Location         *ComputerLocation
	Metadata         map[string]string
	MaxBindAttempts  int
	BindReadyTimeout time.Duration
}

// AttachOptions configures CreateComputer / AttachComputer / Computer.Attach.
type AttachOptions struct {
	// Credentials reuses a pre-generated identity (CreateComputer only); empty mints one.
	Credentials Credentials
	// CaptureKeypair opts the attach into asymmetric state encryption. Persist
	// both halves before attaching. PriorCaptureKeypairs keeps superseded
	// generations available to restore components that have not been re-sealed.
	CaptureKeypair       *CaptureKeypair
	PriorCaptureKeypairs []*CaptureKeypair

	Timeout      time.Duration // runtime lifetime, default 300s
	ReadyTimeout time.Duration // allocation readiness budget, default 300s
	// Deprecated: Computer attach does not support environment overrides.
	// Nonempty maps are rejected before provisioning; leave nil or empty.
	PodEnv   map[string]string
	Metadata map[string]string
	// Location is optional. Portal applies its server-owned default for a new
	// Computer; later attaches retain the last location when omitted and may
	// replace it after Stop or Kill.
	Location *ComputerLocation
	// BindingRevision is used by low-level Computer.Attach. Client.AttachComputer
	// reads the current binding and ignores this value.
	BindingRevision int64

	// Ephemeral attaches without saved state: nothing is restored, no state key
	// or capture keypair is sent, and Stop saves nothing. Default (false) is the
	// persistent attach.
	Ephemeral bool

	MaxBindAttempts  int           // attach attempts, default 3
	BindReadyTimeout time.Duration // attach readiness budget, default 120s
}

// Computer is a persistent Pine Computer. Its identity is (ID, Key); while attached to a
// runtime it also holds the runtime's handle and its Computer token (ct_).
type Computer struct {
	id        string
	key       []byte
	priorKeys map[int][]byte
	// captureKeypairs holds the asymmetric capture identity by generation
	// (asymmetric component envelope v3); captureGen is the CURRENT generation submitted at
	// attach. Older generations stay registered so a restore of
	// pre-rotation state can still unwrap its secrets.
	captureKeypairs map[int]*CaptureKeypair
	captureGen      int
	bindingRevision int64
	location        *ComputerLocation
	// lastAuthorizedSandboxID is set only after Portal commits an attach
	// authorization. Client uses it to add recovery context when the later
	// coordinator bind fails and the Computer itself cannot be returned.
	lastAuthorizedSandboxID string

	mu            sync.Mutex
	conn          *Connection
	sandbox       *RuntimeHandle
	coord         *coordinator.Client
	computerToken string // ct_
	// ephemeral records that this Computer was bound access-lease-only; Stop
	// reads it to skip the pre-terminate checkpoint.
	ephemeral bool
	// persistenceMode is the coord-reported bound persistence mode from the
	// bind response (surfaced via PersistenceMode()).
	persistenceMode string
}

func newComputer(id string, key []byte) *Computer {
	// Copy the caller's key — it's a durable secret; the Computer owns its own
	// copy so a caller mutating their slice can't corrupt the bound state key.
	return &Computer{id: id, key: cloneKey(key), priorKeys: map[int][]byte{}, captureKeypairs: map[int]*CaptureKeypair{}}
}

// cloneKey returns a defensive copy of a state-key slice (nil-safe).
func cloneKey(k []byte) []byte {
	if k == nil {
		return nil
	}
	return append([]byte(nil), k...)
}

func cloneLocation(location *ComputerLocation) *ComputerLocation {
	if location == nil {
		return nil
	}
	copy := *location
	return &copy
}

func validateLocation(location *ComputerLocation) error {
	if location == nil {
		return nil
	}
	if location.Mode == "direct" && location.Country == "" {
		return nil
	}
	country := location.Country
	if location.Mode != "" || len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
		return fmt.Errorf("pinesandbox: Location.Country must be canonical ISO 3166-1 alpha-2 uppercase, or set Location.Mode to direct without a country")
	}
	return nil
}

// ID is the Computer's stable id.
func (c *Computer) ID() string { return c.id }

// Key is the Computer's 32-byte state key (persist it to re-attach). Returns a
// copy — mutating it does not affect the Computer's bound key.
func (c *Computer) Key() []byte { return cloneKey(c.key) }

// BindingRevision is diagnostic metadata for the latest attach authorization.
// Client.StartComputer reads the current revision; applications need not save it.
func (c *Computer) BindingRevision() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bindingRevision
}

// Location returns the effective location committed by Portal for the latest
// binding, or nil until an attach receipt reports it. The returned value is a copy.
func (c *Computer) Location() *ComputerLocation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneLocation(c.location)
}

// CaptureKeypair returns a defensive copy of the current asymmetric keypair.
// Persist it; Portal sees only its public half.
func (c *Computer) CaptureKeypair() *CaptureKeypair {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.captureGen == 0 {
		return nil
	}
	return c.captureKeypairs[c.captureGen].clone()
}

// Credentials returns a copy of the caller-owned secrets. Save them before
// Create, and again only after changing keys. It contains no live runtime tokens.
func (c *Computer) Credentials() Credentials {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := Credentials{ID: c.id, Key: cloneKey(c.key), Ephemeral: c.ephemeral,
		CaptureKeypair: c.captureKeypairs[c.captureGen].clone(), PriorKeys: map[int][]byte{}}
	for generation, kp := range c.captureKeypairs {
		if generation != c.captureGen {
			result.PriorCaptureKeypairs = append(result.PriorCaptureKeypairs, kp.clone())
		}
	}
	sort.Slice(result.PriorCaptureKeypairs, func(i, j int) bool {
		return result.PriorCaptureKeypairs[i].Generation < result.PriorCaptureKeypairs[j].Generation
	})
	for version, key := range c.priorKeys {
		result.PriorKeys[version] = cloneKey(key)
	}
	return result
}

// RuntimeID is the id of the runtime the Computer is attached to (empty if not attached).
func (c *Computer) RuntimeID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sandbox == nil {
		return ""
	}
	return c.sandbox.ID()
}

// SandboxID is the former name of RuntimeID.
//
// Deprecated: use RuntimeID.
func (c *Computer) SandboxID() string { return c.RuntimeID() }

// ComputerToken is the Computer token (ct_) for the attached runtime (empty if not
// attached). @api advanced — prefer the typed Session/Computer methods, which send the
// right token.
func (c *Computer) ComputerToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.computerToken
}

// PersistenceMode is the persistence mode the runtime reported on the last
// successful attach (e.g. "persistent" or "ephemeral"); empty if not attached
// or none was reported.
func (c *Computer) PersistenceMode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.persistenceMode
}

// String redacts the secrets (the 32-byte state key + the ct_) so a struct dump in a log
// can't leak them; GoString (%#v) defers to it.
func (c *Computer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	sid := ""
	if c.sandbox != nil {
		sid = c.sandbox.ID()
	}
	return fmt.Sprintf("Computer{id:%s attached:%t runtime:%s key+ct_ redacted}", c.id, c.sandbox != nil, sid)
}

// GoString redacts secrets under %#v.
func (c *Computer) GoString() string { return c.String() }

// SetCaptureKeypair opts this Computer into asymmetric state encryption
// (asymmetric component envelope v3): kp.PK is submitted at the next attach, the platform seals
// every capture to it, and the bind runs the two-round restore (this SDK
// unwraps the per-component secrets with kp.SK). kp becomes the CURRENT
// generation; previously set generations stay registered for restores of
// pre-rotation state.
func (c *Computer) SetCaptureKeypair(kp *CaptureKeypair) error {
	if kp == nil {
		return fmt.Errorf("pinesandbox: capture keypair required")
	}
	if err := kp.validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if kp.Generation < c.captureGen {
		return fmt.Errorf("pinesandbox: capture keypair generation %d below the current generation %d (rotations only move forward)", kp.Generation, c.captureGen)
	}
	if existing := c.captureKeypairs[kp.Generation]; existing != nil &&
		(!bytes.Equal(existing.PK, kp.PK) || !bytes.Equal(existing.SK, kp.SK)) {
		return fmt.Errorf("pinesandbox: capture keypair generation %d is already registered with different key material", kp.Generation)
	}
	c.captureKeypairs[kp.Generation] = kp.clone()
	c.captureGen = kp.Generation
	return nil
}

// AddPriorCaptureKeypair registers a SUPERSEDED capture keypair so a
// restore of state sealed under it can still unwrap its secrets. It never
// changes the current generation.
func (c *Computer) AddPriorCaptureKeypair(kp *CaptureKeypair) error {
	if kp == nil {
		return fmt.Errorf("pinesandbox: capture keypair required")
	}
	if err := kp.validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.captureGen != 0 && kp.Generation >= c.captureGen {
		return fmt.Errorf("pinesandbox: prior capture keypair generation %d must be below the current generation %d", kp.Generation, c.captureGen)
	}
	if existing := c.captureKeypairs[kp.Generation]; existing != nil &&
		(!bytes.Equal(existing.PK, kp.PK) || !bytes.Equal(existing.SK, kp.SK)) {
		return fmt.Errorf("pinesandbox: capture keypair generation %d is already registered with different key material", kp.Generation)
	}
	c.captureKeypairs[kp.Generation] = kp.clone()
	return nil
}

// captureKeypairsCopy snapshots the registered keypairs for the binder.
func (c *Computer) captureKeypairsCopy() (map[int]*CaptureKeypair, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.captureKeypairs) == 0 {
		return nil, 0
	}
	cp := make(map[int]*CaptureKeypair, len(c.captureKeypairs))
	for g, kp := range c.captureKeypairs {
		cp[g] = kp.clone()
	}
	return cp, c.captureGen
}

func (c *Computer) configureCaptureKeypairs(current *CaptureKeypair, prior []*CaptureKeypair) error {
	// Validate and clone every caller-owned key before taking the Computer
	// lock. Nothing below mutates c until the complete candidate set passes.
	var preparedCurrent *CaptureKeypair
	if current != nil {
		if err := current.validate(); err != nil {
			return err
		}
		preparedCurrent = current.clone()
	}
	preparedPrior := make([]*CaptureKeypair, 0, len(prior))
	seenPrior := make(map[int]struct{}, len(prior))
	for _, kp := range prior {
		if kp == nil {
			return fmt.Errorf("pinesandbox: prior capture keypair required")
		}
		if err := kp.validate(); err != nil {
			return err
		}
		if _, duplicate := seenPrior[kp.Generation]; duplicate {
			return fmt.Errorf("pinesandbox: duplicate prior capture keypair generation %d", kp.Generation)
		}
		seenPrior[kp.Generation] = struct{}{}
		preparedPrior = append(preparedPrior, kp.clone())
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	nextGeneration := c.captureGen
	nextKeypairs := make(map[int]*CaptureKeypair, len(c.captureKeypairs)+len(preparedPrior)+1)
	for generation, kp := range c.captureKeypairs {
		nextKeypairs[generation] = kp.clone()
	}
	if preparedCurrent != nil {
		if preparedCurrent.Generation < nextGeneration {
			return fmt.Errorf("pinesandbox: capture keypair generation %d below the current generation %d (rotations only move forward)", preparedCurrent.Generation, nextGeneration)
		}
		if existing := nextKeypairs[preparedCurrent.Generation]; existing != nil &&
			(!bytes.Equal(existing.PK, preparedCurrent.PK) || !bytes.Equal(existing.SK, preparedCurrent.SK)) {
			return fmt.Errorf("pinesandbox: capture keypair generation %d is already registered with different key material", preparedCurrent.Generation)
		}
		nextKeypairs[preparedCurrent.Generation] = preparedCurrent
		nextGeneration = preparedCurrent.Generation
	}
	if len(preparedPrior) > 0 && nextGeneration == 0 {
		return fmt.Errorf("pinesandbox: prior capture keypairs require a current capture keypair")
	}
	for _, kp := range preparedPrior {
		if kp.Generation >= nextGeneration {
			return fmt.Errorf("pinesandbox: prior capture keypair generation %d must be below the current generation %d", kp.Generation, nextGeneration)
		}
		if existing := nextKeypairs[kp.Generation]; existing != nil &&
			(!bytes.Equal(existing.PK, kp.PK) || !bytes.Equal(existing.SK, kp.SK)) {
			return fmt.Errorf("pinesandbox: capture keypair generation %d is already registered with different key material", kp.Generation)
		}
		nextKeypairs[kp.Generation] = kp
	}
	c.captureKeypairs = nextKeypairs
	c.captureGen = nextGeneration
	return nil
}

// AddPriorKey registers a pre-rotation key so a restore can decrypt a snapshot sealed under
// the old key (carried as computer_key_for_restore on the next bind).
func (c *Computer) AddPriorKey(version int, key []byte) error {
	if version == CurrentKeyVersion {
		return fmt.Errorf("pinesandbox: prior key version must differ from the current key version (%d)", CurrentKeyVersion)
	}
	if len(key) == 0 {
		return fmt.Errorf("pinesandbox: prior key bytes required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.priorKeys[version] = cloneKey(key)
	return nil
}

// Attach allocates a fresh runtime, waits for it to run, and attaches the Computer to it,
// leaving the Computer ready to create sessions. It refuses to attach a Computer that is
// already attached (call Stop or Kill first).
func (c *Computer) Attach(ctx context.Context, conn *Connection, opts AttachOptions) error {
	c.mu.Lock()
	if c.sandbox != nil {
		c.mu.Unlock()
		return bind.NewBindError(0, "Computer already attached — call Stop (graceful) or Kill first", nil)
	}
	c.mu.Unlock()
	if opts.BindingRevision < 0 {
		return fmt.Errorf("pinesandbox: BindingRevision must be non-negative")
	}
	if len(opts.PodEnv) != 0 {
		return fmt.Errorf("pinesandbox: PodEnv is unsupported for Computer attach; leave it nil or empty")
	}
	if opts.ReadyTimeout < 0 {
		return fmt.Errorf("pinesandbox: ReadyTimeout must be non-negative")
	}
	if err := validateLocation(opts.Location); err != nil {
		return err
	}
	if err := c.configureCaptureKeypairs(opts.CaptureKeypair, opts.PriorCaptureKeypairs); err != nil {
		return err
	}
	_, captureGeneration := c.captureKeypairsCopy()
	if !opts.Ephemeral && captureGeneration == 0 {
		return fmt.Errorf("pinesandbox: v3 attach requires CaptureKeypair (persist both halves)")
	}
	c.mu.Lock()
	if c.bindingRevision == 0 {
		c.bindingRevision = opts.BindingRevision
	} else if opts.BindingRevision != 0 && opts.BindingRevision != c.bindingRevision {
		c.mu.Unlock()
		return fmt.Errorf("pinesandbox: binding revision %d conflicts with Computer revision %d", opts.BindingRevision, c.bindingRevision)
	}
	bindingRevision := c.bindingRevision
	requestedLocation := cloneLocation(opts.Location)
	c.mu.Unlock()
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultSandboxLifetime
	}
	readyTimeout := opts.ReadyTimeout
	if readyTimeout == 0 {
		readyTimeout = defaultReadyTimeout
	}
	// Allocation and readiness share a budget independent of sandbox lifetime.
	// Bind has a separate budget, also bounded by the caller's context.
	readyCtx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()

	createID, err := uuidV7()
	if err != nil {
		return err
	}
	body := buildComputerCreateBody(timeout, opts)
	body["create_id"] = createID
	info, err := conn.controlPlane.CreateComputer(readyCtx, body)
	if err != nil {
		return err
	}
	sandbox := conn.runtimeHandle(info.ID, info.Status)
	attached := false
	defer func() {
		if !attached {
			// The failed operation's deadline/cancellation must not prevent DELETE.
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), attachCleanupTimeout)
			defer cleanupCancel()
			_ = sandbox.Kill(cleanupCtx)
		}
	}()
	if err := sandbox.WaitUntilRunning(readyCtx, readyTimeout, 0); err != nil {
		return readinessBudgetError(ctx, readyCtx, err, sandbox)
	}
	cancel()

	coord, err := conn.newCoord(info.ID)
	if err != nil {
		return err
	}
	captureKPs, captureGen := c.captureKeypairsCopy()
	clock := wait.Or(conn.clock)
	res, err := binder.Bind(ctx, binder.Config{
		Clock:           clock.Now,
		Sleeper:         clock.Sleep,
		Coord:           coord,
		Minter:          conn.attachProvider,
		ComputerID:      c.id,
		Key:             c.key,
		PriorKeys:       c.priorKeysCopy(),
		SandboxID:       info.ID,
		Location:        tokenLocation(requestedLocation),
		MaxBindAttempts: opts.MaxBindAttempts,
		ReadyTimeout:    opts.BindReadyTimeout,
		Ephemeral:       opts.Ephemeral,
		CaptureKeypairs: binderKeypairs(captureKPs),
		CaptureGen:      captureGen,
		BindingRevision: bindingRevision,
		OnAuthorized: func(revision int64) {
			c.mu.Lock()
			c.bindingRevision = revision
			c.lastAuthorizedSandboxID = info.ID
			c.mu.Unlock()
		},
		OnLocation: func(location tokens.ComputerLocation) {
			c.mu.Lock()
			c.location = &ComputerLocation{Country: location.Country, Mode: location.Mode}
			c.mu.Unlock()
		},
	})
	if err != nil {
		return err
	}

	c.mu.Lock()
	coord.QualifyWith(res.ComputerToken)
	c.conn, c.sandbox, c.coord, c.computerToken = conn, sandbox, coord, res.ComputerToken
	c.ephemeral = opts.Ephemeral
	c.persistenceMode = res.PersistenceMode
	c.mu.Unlock()
	attached = true
	return nil
}

// readinessBudgetError reports expiry of the SDK-owned readiness budget as the
// documented *ReadyTimeoutError. The readiness context and the poll loop's own
// deadline expire together, and the sleep between polls observes the context
// first, so without this translation callers would see context.DeadlineExceeded
// or a transport timeout instead. Caller cancellation stays distinguishable.
func readinessBudgetError(callerCtx, readyCtx context.Context, err error, sandbox *RuntimeHandle) error {
	var rt *ReadyTimeoutError
	if errors.As(err, &rt) {
		return err
	}
	if callerCtx.Err() == nil && errors.Is(readyCtx.Err(), context.DeadlineExceeded) {
		return &ReadyTimeoutError{RuntimeID: sandbox.id, SandboxID: sandbox.id, LastState: sandbox.status}
	}
	return err
}

func tokenLocation(location *ComputerLocation) *tokens.ComputerLocation {
	if location == nil {
		return nil
	}
	return &tokens.ComputerLocation{Country: location.Country, Mode: location.Mode}
}

// adopt wires an already-bound, still-live pod (no provision, no bind).
func (c *Computer) adopt(conn *Connection, sandboxID, computerToken, status string) error {
	coord, err := conn.newCoord(sandboxID)
	if err != nil {
		return err
	}
	coord.QualifyWith(computerToken)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = conn
	c.sandbox = conn.runtimeHandle(sandboxID, status)
	c.coord = coord
	c.computerToken = computerToken
	return nil
}

// Status observes this binding through the lifecycle service. Unlike Alive,
// it preserves errors: NotFound confirms runtime-record absence; a transport
// or authorization error does not. It never resumes or replaces the runtime.
func (c *Computer) Status(ctx context.Context) (string, error) {
	c.mu.Lock()
	sandbox := c.sandbox
	c.mu.Unlock()
	if sandbox == nil {
		return "", fmt.Errorf("pinesandbox: Computer %s has no runtime binding", c.id)
	}
	info, err := sandbox.cp.Get(ctx, sandbox.id)
	if err != nil {
		return "", err
	}
	return info.Status, nil
}

// Pause retains live memory, the binding and session credentials. It waits for
// Paused, bounded by six minutes or the caller's earlier context deadline.
func (c *Computer) Pause(ctx context.Context) error {
	return c.transitionMemory(ctx, "paused")
}

// Resume restores retained memory and waits for Running with the same budget
// as Pause. It never creates a replacement binding or falls back to Start.
func (c *Computer) Resume(ctx context.Context) error {
	return c.transitionMemory(ctx, statusRunning)
}

func (c *Computer) transitionMemory(ctx context.Context, target string) error {
	c.mu.Lock()
	sandbox := c.sandbox
	c.mu.Unlock()
	if sandbox == nil {
		return fmt.Errorf("pinesandbox: Computer %s has no retained runtime", c.id)
	}
	return sandbox.transitionMemory(ctx, target)
}

// Stop admits server-owned save-before-release, then waits for compute release.
// A timeout only ends this wait. Another backend can continue from a saved handle.
// After the runtime record is gone its final save outcome is unknown.
func (c *Computer) Stop(ctx context.Context) (bool, error) {
	c.mu.Lock()
	sandbox := c.sandbox
	c.mu.Unlock()
	if sandbox == nil {
		return true, nil
	}
	gone, err := sandbox.Stop(ctx)
	if gone {
		c.reset()
	}
	return gone, err
}

// Kill ungracefully force-recovers (best-effort delete, no wait) and drops the binding.
func (c *Computer) Kill(ctx context.Context) bool {
	c.mu.Lock()
	sandbox := c.sandbox
	c.mu.Unlock()
	if sandbox == nil {
		return true
	}
	ok := sandbox.Kill(ctx)
	if ok {
		c.reset()
	}
	return ok
}

// Delete PERMANENTLY deletes this Computer (see Client.DeleteComputer): a live runtime is
// force-killed first — no final capture, the state is being deleted — then the id is
// deleted through the Client connection that attached it. A failed kill does not block
// the delete; Portal destroys any runtime still bound to a deleted Computer. On success
// the local binding is dropped.
func (c *Computer) Delete(ctx context.Context) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("pinesandbox: Computer %s has no Client connection; use Client.DeleteComputer", c.id)
	}
	portal, err := conn.requirePortal("delete")
	if err != nil {
		return err
	}
	c.Kill(ctx)
	if err := portal.DeleteComputer(ctx, c.id); err != nil {
		return err
	}
	c.reset()
	return nil
}

// Alive reports whether the attached runtime is still live.
func (c *Computer) Alive(ctx context.Context) bool {
	c.mu.Lock()
	sandbox := c.sandbox
	c.mu.Unlock()
	return sandbox != nil && sandbox.Alive(ctx)
}

func (c *Computer) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sandbox, c.coord, c.computerToken = nil, nil, ""
}

func (c *Computer) priorKeysCopy() map[int][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.priorKeys) == 0 {
		return nil
	}
	out := make(map[int][]byte, len(c.priorKeys))
	for k, v := range c.priorKeys {
		out[k] = cloneKey(v)
	}
	return out
}

func buildComputerCreateBody(timeout time.Duration, opts AttachOptions) map[string]any {
	body := map[string]any{
		"timeout": int(timeout.Seconds()),
	}
	if opts.Metadata != nil {
		body["metadata"] = opts.Metadata
	}
	return body
}

func ttlSecondsPtr(d time.Duration) *int {
	s := int(d.Seconds())
	return &s
}

// StopSaveError means stop did not confirm persistence and retained its binding.
// Unwrap exposes the underlying API problem, including its retry classification.
type StopSaveError struct {
	Err       error
	Retryable bool
}

func (e *StopSaveError) Error() string {
	return "Computer stop could not confirm saved state: " + e.Err.Error()
}
func (e *StopSaveError) Unwrap() error { return e.Err }
