package pinesandbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/base/wait"
	"go.pinesandbox.io/computer/internal/base/zone"
	"go.pinesandbox.io/computer/internal/binder"
	"go.pinesandbox.io/computer/internal/controlplane"
	"go.pinesandbox.io/computer/internal/coordinator"
	"go.pinesandbox.io/computer/internal/tokens"
)

// Connection is the resolved attach context a Client builds: where the project's
// Computers live and the clients that allocate and authorize them. Computer.Attach
// takes one.
type Connection struct {
	zone         *zone.Zone
	controlPlane *controlplane.Client
	// attachProvider issues attach credentials: Portal with the project key, or the
	// caller's AttachCredentialsProvider.
	attachProvider binder.Minter
	// portal is the project-key Portal client for the Computer registry, location
	// discovery and deletion; nil when the Client has no APIKey.
	portal    *tokens.AttachCredentialsSource
	specMajor int
	// clock is the time every wait and backoff uses; nil means the system clock.
	clock wait.Clock
	// newCoord builds the coordinator client for a runtime's data host. A field so tests
	// can point it at an httptest server instead of the derived gateway host.
	newCoord func(sandboxID string) (*coordinator.Client, error)
}

// requirePortal returns the project-key Portal client, or an error naming what needs it.
func (c *Connection) requirePortal(what string) (*tokens.AttachCredentialsSource, error) {
	if c == nil || c.portal == nil {
		return nil, fmt.Errorf("pinesandbox: %s needs the project key (ClientOptions.APIKey)", what)
	}
	return c.portal, nil
}

// runtimeHandle is the lifecycle handle for one runtime, on this connection's clock.
func (c *Connection) runtimeHandle(runtimeID, status string) *RuntimeHandle {
	h := newRuntimeHandle(c.controlPlane, runtimeID, status)
	clock := wait.Or(c.clock)
	h.clock, h.sleeper = clock.Now, clock.Sleep
	return h
}

// Client is the SDK entry point. It holds the project credential (pk_) + endpoint once and
// returns ready Computers.
type Client struct {
	zone            *zone.Zone
	conn            *Connection
	portalTransport *transport.Client
	projectKey      string
}

// ControlTokenProvider returns the project token (the control-plane JWS) for lifecycle
// requests; forceRefresh asks for a newly minted token after the control plane rejected
// the last one. It lets a backend mint project tokens without this process holding the pk_.
type ControlTokenProvider func(ctx context.Context, forceRefresh bool) (string, error)

// AttachCredentialsProvider issues the credentials for one attach instead of Portal with
// the project key: an integrator acting as its own issuer. The receipt's BindingRevision
// must exceed req.ExpectedBindingRevision, and a persistent attach needs KeyAssertion.
type AttachCredentialsProvider func(ctx context.Context, req AttachCredentialsRequest) (*AttachCredentials, error)

// AttachCredentialsRequest names the runtime incarnation one attach authorizes.
type AttachCredentialsRequest struct {
	ComputerID  string
	PodUID      string
	CoordBootID string
	RuntimeID   string
	// PKComputer is the base64url X25519 capture public key and KeyGeneration its
	// generation; both are empty for an ephemeral attach.
	PKComputer              string
	KeyGeneration           int
	ExpectedBindingRevision int64
	// IdempotencyKey is stable for the runtime incarnation, so a resent request returns
	// the same receipt.
	IdempotencyKey string
	Location       *ComputerLocation
	Ephemeral      bool
}

// AttachCredentials is one attach authorization receipt.
type AttachCredentials struct {
	BindToken            string
	BrokerGrant          string
	BindTokenExpiresAt   string
	BrokerGrantExpiresAt string
	KeyAssertion         string // required for a persistent attach
	BindingRevision      int64
	Location             *ComputerLocation
}

// ClientOptions configures NewClient.
type ClientOptions struct {
	// Endpoint is the portal URL or domain the project was given (required), e.g.
	// "https://pinesandbox.io".
	Endpoint string
	// APIKey is the project client key (pk_). Required unless both ControlTokenProvider
	// and AttachCredentialsProvider are set; without it, calls that need Portal with the
	// project key (Start, Stop by id, Delete, registration, location discovery, project
	// policy, diagnostics) return an error.
	APIKey string
	// ControlHost optionally overrides the derived control-plane host.
	ControlHost string
	// SpecVersion pins the Computer-API major (default: the SDK's SpecVersion).
	SpecVersion int
	// ControlTokenProvider supplies project tokens instead of minting them from APIKey.
	ControlTokenProvider ControlTokenProvider
	// AttachCredentialsProvider issues attach credentials instead of Portal with APIKey.
	AttachCredentialsProvider AttachCredentialsProvider
}

// clientHooks are the test seams behind NewClient: the HTTP client every request goes
// through and the clock every wait uses. The zero value is production.
type clientHooks struct {
	httpClient *http.Client
	clock      wait.Clock
}

// NewClient wires the SDK from a pk_ + endpoint.
func NewClient(opts ClientOptions) (*Client, error) {
	return newClient(opts, clientHooks{})
}

func newClient(opts ClientOptions, hooks clientHooks) (*Client, error) {
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("pinesandbox: Endpoint required (e.g. \"https://pinesandbox.io\")")
	}
	if opts.APIKey == "" && (opts.ControlTokenProvider == nil || opts.AttachCredentialsProvider == nil) {
		return nil, fmt.Errorf("pinesandbox: APIKey required (a pk_ client key) unless both ControlTokenProvider and AttachCredentialsProvider are set")
	}
	specMajor := opts.SpecVersion
	if specMajor == 0 {
		specMajor = SpecVersion
	}

	var zopts []zone.Option
	if opts.ControlHost != "" {
		zopts = append(zopts, zone.WithControlHost(opts.ControlHost))
	}
	z, err := zone.Parse(opts.Endpoint, zopts...)
	if err != nil {
		return nil, err
	}

	clock := wait.Or(hooks.clock)
	topts := []transport.Option{transport.WithClock(clock)}
	if hooks.httpClient != nil {
		topts = append(topts, transport.WithHTTPClient(hooks.httpClient))
	}
	controlTransport := transport.New(z.HTTPScheme(), z.ControlHost(), topts...)
	var portal *tokens.AttachCredentialsSource
	if opts.APIKey != "" {
		if portal, err = tokens.NewAttachCredentialsSource(controlTransport, opts.APIKey); err != nil {
			return nil, err
		}
	}
	var tokenSource controlplane.TokenSource
	if opts.ControlTokenProvider != nil {
		tokenSource = tokens.NewCallableTokenSource(opts.ControlTokenProvider)
	} else if tokenSource, err = tokens.NewControlTokenSource(controlTransport, opts.APIKey,
		tokens.WithClock(clock.Now), tokens.WithSleeper(clock.Sleep), tokens.WithJitter(clock.Jitter)); err != nil {
		return nil, err
	}
	var attachProvider binder.Minter
	if opts.AttachCredentialsProvider != nil {
		attachProvider = tokens.NewCallableAttachSource(callableAttach(opts.AttachCredentialsProvider))
	} else {
		attachProvider = portal // non-nil: without the provider, APIKey is required above
	}
	cp := controlplane.NewClient(controlTransport, tokenSource, specMajor, controlplane.WithClock(clock))

	conn := &Connection{
		zone:           z,
		controlPlane:   cp,
		attachProvider: attachProvider,
		portal:         portal,
		specMajor:      specMajor,
		clock:          clock,
		newCoord: func(sandboxID string) (*coordinator.Client, error) {
			host, err := z.DataHost(sandboxID)
			if err != nil {
				return nil, err
			}
			return coordinator.NewClient(transport.New(z.HTTPScheme(), host, topts...), specMajor,
				coordinator.WithClock(clock)), nil
		},
	}
	return &Client{zone: z, conn: conn, portalTransport: controlTransport, projectKey: opts.APIKey}, nil
}

// callableAttach converts between the public provider types and the internal mint types.
func callableAttach(fn AttachCredentialsProvider) func(context.Context, tokens.CredentialsRequest) (*tokens.AttachCredentials, error) {
	return func(ctx context.Context, req tokens.CredentialsRequest) (*tokens.AttachCredentials, error) {
		pub := AttachCredentialsRequest{
			ComputerID: req.ComputerID, PodUID: req.PodUID, CoordBootID: req.CoordBootID,
			RuntimeID: req.SandboxID, PKComputer: req.PKComputer, KeyGeneration: req.KeyGeneration,
			ExpectedBindingRevision: req.ExpectedBindingRevision, IdempotencyKey: req.IdempotencyKey,
			Ephemeral: req.Ephemeral,
		}
		if req.Location != nil {
			pub.Location = &ComputerLocation{Country: req.Location.Country, Mode: req.Location.Mode}
		}
		creds, err := fn(ctx, pub)
		if err != nil || creds == nil {
			return nil, err
		}
		out := &tokens.AttachCredentials{
			BindToken: creds.BindToken, BrokerGrant: creds.BrokerGrant,
			BindTokenExpiresAt: creds.BindTokenExpiresAt, BrokerGrantExpiresAt: creds.BrokerGrantExpiresAt,
			KeyAssertion: creds.KeyAssertion, BindingRevision: creds.BindingRevision,
			Location: tokenLocation(creds.Location),
		}
		return out, nil
	}
}

// RegisterOptions configures Client.RegisterComputer.
type RegisterOptions struct {
	// Location is where the new Computer's browser reaches the internet from at first.
	Location *ComputerLocation
	// Ephemeral fixes the Computer as access-lease-only: no state is restored or saved.
	Ephemeral bool
}

// RegisterComputer registers a Computer id with the project ahead of its first attach.
// You rarely need it: CreateComputer attaches directly, which registers the Computer on
// first use. Registering an id the project already owns succeeds; a deleted id, another
// project's id, or a different persistence mode is a *ComputerRegistrationError.
func (c *Client) RegisterComputer(ctx context.Context, id string, opts RegisterOptions) error {
	if !computerIDRE.MatchString(id) {
		return fmt.Errorf("pinesandbox: valid Computer ID required")
	}
	if err := validateLocation(opts.Location); err != nil {
		return err
	}
	portal, err := c.conn.requirePortal("registration")
	if err != nil {
		return err
	}
	return portal.RegisterComputer(ctx, id, tokenLocation(opts.Location), opts.Ephemeral)
}

// CreateComputer provisions a NEW persistent Computer: a fresh UUIDv7 id +
// caller-owned state/capture keys, then a Portal-authorized bind. Portal lazily
// creates only its minimal authorization row during that atomic attach. Persist
// the Credentials before Create; Start reads the current binding itself.
func (c *Client) CreateComputer(ctx context.Context, opts AttachOptions) (*Computer, error) {
	creds := opts.Credentials
	if creds.ID == "" {
		generated, err := GenerateCredentials()
		if err != nil {
			return nil, err
		}
		creds = generated
	}
	if err := validateIdentity(creds.ID, creds.Key); err != nil {
		return nil, err
	}
	opts.Ephemeral = opts.Ephemeral || creds.Ephemeral
	comp := newComputer(creds.ID, creds.Key)
	comp.ephemeral = opts.Ephemeral
	for version, key := range creds.PriorKeys {
		if err := comp.AddPriorKey(version, key); err != nil {
			return nil, err
		}
	}
	if opts.PriorCaptureKeypairs == nil {
		opts.PriorCaptureKeypairs = creds.PriorCaptureKeypairs
	}
	// An ephemeral (access-lease-only) Computer has NO capture identity: never
	// adopt a caller-supplied keypair nor auto-generate one.
	if !opts.Ephemeral {
		if opts.CaptureKeypair == nil {
			opts.CaptureKeypair = creds.CaptureKeypair
		}
		if opts.CaptureKeypair == nil {
			var err error
			opts.CaptureKeypair, err = GenerateCaptureKeypair(1)
			if err != nil {
				return nil, err
			}
		}
	}
	if err := comp.configureCaptureKeypairs(opts.CaptureKeypair, opts.PriorCaptureKeypairs); err != nil {
		return nil, err
	}
	// A Portal authorization can commit before the later coordinator bind
	// fails. Keep a deep copy of every durable secret generated or selected by
	// this create call so that error path is recoverable even though there is no
	// Computer result to return.
	recoveryCredentials := comp.Credentials()
	// Already validated/configured above so malformed caller material cannot
	// provision a pod. Clear the value-copy options before the
	// public Attach path applies its direct-caller configuration hook.
	opts.CaptureKeypair = nil
	opts.PriorCaptureKeypairs = nil
	if err := comp.Attach(ctx, c.conn, opts); err != nil {
		return nil, attachClientError(comp, opts.BindingRevision, &recoveryCredentials, err)
	}
	return comp, nil
}

// StartComputer starts an EXISTING persistent Computer by its stored id + state key.
// It allocates a fresh runtime, attaches the Computer and restores its saved state.
func (c *Client) StartComputer(ctx context.Context, creds Credentials, opts StartOptions) (*Computer, error) {
	return c.startWithCredentials(ctx, creds, opts, false)
}

// StopComputer gracefully stops the binding observed for this Computer ID.
// It needs project authorization, but no private key or saved runtime token.
// A concurrent newer binding is never followed by this invocation.
func (c *Client) StopComputer(ctx context.Context, id string) (bool, error) {
	if !computerIDRE.MatchString(id) {
		return false, fmt.Errorf("pinesandbox: valid Computer ID required")
	}
	portal, err := c.conn.requirePortal("stop")
	if err != nil {
		return false, err
	}
	reference, err := portal.CurrentBinding(ctx, id)
	if err != nil {
		var missing *UnknownComputerError
		if errors.As(err, &missing) {
			return true, nil
		}
		return false, err
	}
	if reference.SandboxID == "" {
		return true, nil
	}
	return c.conn.runtimeHandle(reference.SandboxID, "").Stop(ctx)
}

// RecoverComputer explicitly releases a failed runtime before saved-state Start.
// It discards unsaved memory and never replaces a healthy paused or running runtime.
func (c *Client) RecoverComputer(ctx context.Context, creds Credentials, opts StartOptions) (*Computer, error) {
	return c.startWithCredentials(ctx, creds, opts, true)
}

func (c *Client) startWithCredentials(ctx context.Context, creds Credentials, opts StartOptions, recoverRuntime bool) (*Computer, error) {
	if err := creds.validate(); err != nil {
		return nil, err
	}
	return c.startComputer(ctx, creds.ID, creds.Key, AttachOptions{
		Credentials: creds, CaptureKeypair: creds.CaptureKeypair,
		PriorCaptureKeypairs: creds.PriorCaptureKeypairs, Ephemeral: creds.Ephemeral,
		Timeout: opts.Timeout, ReadyTimeout: opts.ReadyTimeout, Location: opts.Location,
		Metadata: opts.Metadata, MaxBindAttempts: opts.MaxBindAttempts, BindReadyTimeout: opts.BindReadyTimeout,
	}, recoverRuntime)
}

func (c *Client) startComputer(ctx context.Context, id string, key []byte, opts AttachOptions, recoverRuntime bool) (*Computer, error) {
	if err := validateIdentity(id, key); err != nil {
		return nil, err
	}
	portal, err := c.conn.requirePortal("start")
	if err != nil {
		return nil, err
	}
	reference, err := portal.CurrentBinding(ctx, id)
	if err != nil {
		return nil, err
	}
	// Freeze this revision for the entire invocation. Never advance it after
	// an attach conflict; concurrent Starts must meet the same Portal CAS.
	opts.BindingRevision = *reference.BindingRevision
	if reference.SandboxID != "" {
		info, err := c.conn.controlPlane.Get(ctx, reference.SandboxID)
		var missing *NotFoundError
		if err != nil && !errors.As(err, &missing) {
			return nil, err
		}
		if err == nil && !computeReleased(info) {
			if !recoverRuntime && info.Status != "stopping" {
				return nil, &RuntimeRetainedError{RuntimeID: reference.SandboxID, State: info.Status}
			}
			handle := c.conn.runtimeHandle(reference.SandboxID, info.Status)
			released, err := handle.releaseFailedRuntime(ctx, info)
			if err != nil {
				return nil, err
			}
			if !released {
				return nil, fmt.Errorf("pinesandbox: old runtime release unconfirmed; no replacement was allocated")
			}
		}
	}
	comp := newComputer(id, key)
	for version, key := range opts.Credentials.PriorKeys {
		if err := comp.AddPriorKey(version, key); err != nil {
			return nil, err
		}
	}
	comp.bindingRevision = opts.BindingRevision
	if err := comp.configureCaptureKeypairs(opts.CaptureKeypair, opts.PriorCaptureKeypairs); err != nil {
		return nil, err
	}
	opts.CaptureKeypair = nil
	opts.PriorCaptureKeypairs = nil
	if err := comp.Attach(ctx, c.conn, opts); err != nil {
		return nil, attachClientError(comp, opts.BindingRevision, nil, err)
	}
	return comp, nil
}

// AttachComputer is the compatibility name for saved-state Start.
//
// Deprecated: use StartComputer with the saved Credentials.
func (c *Client) AttachComputer(ctx context.Context, id string, key []byte, opts AttachOptions) (*Computer, error) {
	return c.startComputer(ctx, id, key, opts, false)
}

// DeleteComputer PERMANENTLY deletes the Computer with this id: its identity and saved
// state end (Stop and Kill keep both). Portal tombstones the id at once, destroys any
// bound runtime asynchronously, purges saved state after 24 hours, and never accepts the
// id again. Idempotent and non-disclosing: an unknown, already-deleted, or other project's
// id also returns nil. There is no undelete. Use Computer.Delete to kill a live handle
// first. Errors: *ComputerDeletionError (422 malformed id), *ProjectAccessDenied (403),
// *AttachCredentialsError (401, 429, 5xx).
func (c *Client) DeleteComputer(ctx context.Context, id string) error {
	portal, err := c.conn.requirePortal("delete")
	if err != nil {
		return err
	}
	return portal.DeleteComputer(ctx, id)
}

// attachClientError preserves Portal CAS state when a high-level Client call
// cannot return its temporary Computer. Direct Computer.Attach callers retain
// the object and can read BindingRevision themselves.
func attachClientError(comp *Computer, initialRevision int64, credentials *Credentials, err error) error {
	comp.mu.Lock()
	revision := comp.bindingRevision
	sandboxID := comp.lastAuthorizedSandboxID
	comp.mu.Unlock()
	if revision <= initialRevision || sandboxID == "" {
		return err
	}
	return &AttachAuthorizationCommittedError{
		BindingRevision: revision,
		RuntimeID:       sandboxID,
		SandboxID:       sandboxID,
		Credentials:     credentials,
		Err:             err,
	}
}

// AdoptOptions carries the binding mode and revision persisted by the integrator.
type AdoptOptions struct {
	Ephemeral       bool
	BindingRevision int64
}

// AdoptExisting reconstructs an attached Computer from its persisted
// {id, key, runtime id, computerToken} without allocating or attaching anything. Use it
// to keep driving a running Computer across requests from a backend that saved its
// ct_. An optional AdoptOptions
// preserves ephemeral mode and binding revision; omission uses persistent mode
// and revision zero. Lifecycle operations observe the runtime's current state.
func (c *Client) AdoptExisting(ctx context.Context, id string, key []byte, sandboxID, computerToken string, options ...AdoptOptions) (*Computer, error) {
	if len(options) > 1 {
		return nil, fmt.Errorf("pinesandbox: AdoptExisting accepts at most one AdoptOptions")
	}
	if err := validateIdentity(id, key); err != nil {
		return nil, err
	}
	comp := newComputer(id, key)
	if len(options) == 1 {
		if options[0].BindingRevision < 0 {
			return nil, fmt.Errorf("pinesandbox: BindingRevision must be non-negative")
		}
		comp.ephemeral = options[0].Ephemeral
		comp.bindingRevision = options[0].BindingRevision
	}
	if err := comp.adopt(c.conn, sandboxID, computerToken, statusRunning); err != nil {
		return nil, err
	}
	return comp, nil
}

// String keeps project credentials out of diagnostic logging.
func (c *Client) String() string   { return "PineSandbox.Client [credentials redacted]" }
func (c *Client) GoString() string { return c.String() }
