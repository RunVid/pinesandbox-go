package pinesandbox

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

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
	// newCoord builds the coordinator client for a Computer sandbox's data host. A field so tests
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

// sandboxHandle is the lifecycle handle for one Computer sandbox, on this connection's clock.
func (c *Connection) sandboxHandle(sandboxID, status string) *SandboxHandle {
	h := newSandboxHandle(c.controlPlane, sandboxID, status)
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
	// newTransport builds a client for another host on the same HTTP client and clock:
	// the storage endpoint an artifact token names.
	newTransport func(scheme, host string) *transport.Client
}

// ControlTokenProvider returns the project token (the control-plane JWS) for lifecycle
// requests; forceRefresh asks for a newly minted token after the control plane rejected
// the last one. It lets a backend mint project tokens without this process holding the pk_.
type ControlTokenProvider func(ctx context.Context, forceRefresh bool) (string, error)

// AttachCredentialsProvider issues the credentials for one attach instead of Portal with
// the project key: an integrator acting as its own issuer. The receipt's BindingRevision
// must exceed req.ExpectedBindingRevision, and a persistent attach needs KeyAssertion.
type AttachCredentialsProvider func(ctx context.Context, req AttachCredentialsRequest) (*AttachCredentials, error)

// AttachCredentialsRequest names the Computer and the Computer sandbox one attach authorizes.
type AttachCredentialsRequest struct {
	ComputerID  string
	PodUID      string
	CoordBootID string
	SandboxID   string
	// PKComputer is the base64url X25519 capture public key and KeyGeneration its
	// generation; both are empty for an ephemeral attach.
	PKComputer              string
	KeyGeneration           int
	ExpectedBindingRevision int64
	// IdempotencyKey is stable for that Computer sandbox, so a resent request returns
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
	// ConnectTimeout bounds opening one connection: the TCP connect, then the TLS
	// handshake (zero means the default, 10s). A connection that could not be opened sent
	// nothing, so the request is sent again, three attempts in all.
	ConnectTimeout time.Duration
	// RequestTimeout bounds each attempt of a request whose context has no deadline (zero
	// means the default, 30s); a turn wait's read gets the time the Computer holds it on
	// top. A context deadline always applies instead.
	RequestTimeout time.Duration
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

	if opts.ConnectTimeout < 0 || opts.RequestTimeout < 0 {
		return nil, fmt.Errorf("pinesandbox: ConnectTimeout and RequestTimeout must not be negative")
	}
	connectTimeout := cmp.Or(opts.ConnectTimeout, transport.DefaultConnectTimeout)
	requestTimeout := cmp.Or(opts.RequestTimeout, transport.DefaultRequestTimeout)

	clock := wait.Or(hooks.clock)
	httpClient := hooks.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Transport: transport.NewHTTPTransport(connectTimeout)}
	}
	topts := []transport.Option{transport.WithClock(clock), transport.WithHTTPClient(httpClient),
		transport.WithUnaryTimeout(requestTimeout)}
	controlTransport := transport.New(z.HTTPScheme(), z.ControlHost(), topts...)
	var portal *tokens.AttachCredentialsSource
	if opts.APIKey != "" {
		if portal, err = tokens.NewAttachCredentialsSource(controlTransport, opts.APIKey, userAgent); err != nil {
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
	newTransport := func(scheme, host string) *transport.Client { return transport.New(scheme, host, topts...) }
	return &Client{zone: z, conn: conn, portalTransport: controlTransport, projectKey: opts.APIKey, newTransport: newTransport}, nil
}

// callableAttach converts between the public provider types and the internal mint types.
func callableAttach(fn AttachCredentialsProvider) func(context.Context, tokens.CredentialsRequest) (*tokens.AttachCredentials, error) {
	return func(ctx context.Context, req tokens.CredentialsRequest) (*tokens.AttachCredentials, error) {
		pub := AttachCredentialsRequest{
			ComputerID: req.ComputerID, PodUID: req.PodUID, CoordBootID: req.CoordBootID,
			SandboxID: req.SandboxID, PKComputer: req.PKComputer, KeyGeneration: req.KeyGeneration,
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
	if opts.Ephemeral && (opts.CaptureKeypair != nil || len(opts.PriorCaptureKeypairs) > 0) {
		return nil, fmt.Errorf("pinesandbox: an ephemeral Computer takes no capture keypair")
	}
	comp := newComputer(creds.ID, creds.Key)
	comp.ephemeral = opts.Ephemeral
	for version, key := range creds.PriorKeys {
		if err := comp.AddPriorKey(version, key); err != nil {
			return nil, err
		}
	}
	if opts.PriorCaptureKeypairs == nil && !opts.Ephemeral {
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
	if err := comp.attach(ctx, c.conn, opts, wait.Or(c.conn.clock).Now()); err != nil {
		return nil, attachClientError(comp, opts.BindingRevision, &recoveryCredentials, err)
	}
	return comp, nil
}

// StartComputer starts an EXISTING persistent Computer by its stored id + state key.
// It starts the Computer, attaches to it and restores its saved state.
func (c *Client) StartComputer(ctx context.Context, creds Credentials, opts StartOptions) (*Computer, error) {
	return c.startWithCredentials(ctx, creds, opts, false)
}

// StopComputer stops the runtime that the Computer's binding points to when it's called, which
// may be a runtime another worker has just started. Use it only to recover after losing the
// Computer handle, and run the recoveries of one Computer one at a time. It returns true when
// that runtime is no longer running, or when there was nothing to stop. true does not confirm
// that the final save succeeded. It returns (false, nil) when the runtime did not stop within
// its own 390 s wait. When ctx is cancelled or expires, StopComputer returns an error, and the
// Stop may already have been submitted. When Computer.Stop returns false, call Stop on that
// handle again instead.
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
	return c.conn.sandboxHandle(reference.SandboxID, "").Stop(ctx)
}

// RecoverComputer shuts down a Computer that failed, then Starts it from saved state.
// It discards unsaved memory and never replaces a healthy running or paused Computer.
func (c *Client) RecoverComputer(ctx context.Context, creds Credentials, opts StartOptions) (*Computer, error) {
	return c.startWithCredentials(ctx, creds, opts, true)
}

func (c *Client) startWithCredentials(ctx context.Context, creds Credentials, opts StartOptions, recovering bool) (*Computer, error) {
	if err := creds.validate(); err != nil {
		return nil, err
	}
	return c.startComputer(ctx, creds.ID, creds.Key, AttachOptions{
		Credentials: creds, CaptureKeypair: creds.CaptureKeypair,
		PriorCaptureKeypairs: creds.PriorCaptureKeypairs, Ephemeral: creds.Ephemeral,
		Timeout: opts.Timeout, ReadyTimeout: opts.ReadyTimeout, Location: opts.Location,
		Metadata: opts.Metadata, MaxBindAttempts: opts.MaxBindAttempts, BindReadyTimeout: opts.BindReadyTimeout,
	}, recovering)
}

// startComputer is Start and Recover; ReadyTimeout covers the release wait, the create and the start.
func (c *Client) startComputer(ctx context.Context, id string, key []byte, opts AttachOptions, recovering bool) (*Computer, error) {
	if err := validateIdentity(id, key); err != nil {
		return nil, err
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	portal, err := c.conn.requirePortal("start")
	if err != nil {
		return nil, err
	}
	clock := wait.Or(c.conn.clock)
	started := clock.Now()
	// The binding and the current runtime are read within what is left of the budget.
	current := readyWait{target: targetCurrent, started: started}
	readCtx, cancel := context.WithTimeout(ctx, started.Add(opts.readyTimeout()).Sub(clock.Now()))
	defer cancel()
	reference, err := portal.CurrentBinding(readCtx, id)
	if err != nil {
		return nil, currentReadError(ctx, readCtx, err, "", current, clock.Now())
	}
	// Freeze this revision for the entire invocation. Never advance it after
	// an attach conflict; concurrent Starts must meet the same Portal CAS.
	opts.BindingRevision = *reference.BindingRevision
	// Invalid caller material fails here, before Recover can release anything.
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
	if reference.SandboxID != "" {
		info, err := c.conn.controlPlane.Get(readCtx, reference.SandboxID)
		var missing *NotFoundError
		if errors.As(err, &missing) {
			info, err = nil, nil // authoritative project-scoped absence
		}
		if err != nil {
			return nil, currentReadError(ctx, readCtx, err, reference.SandboxID, current, clock.Now())
		}
		column := admissionStart
		if recovering {
			column = admissionRecover
		}
		release := readyWait{target: targetReleased, started: started}
		handle := c.conn.sandboxHandle(reference.SandboxID, "")
		if err := handle.admit(ctx, column, info, release, started.Add(opts.readyTimeout())); err != nil {
			return nil, err
		}
	}
	if err := comp.attach(ctx, c.conn, opts, started); err != nil {
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
// state end (Stop and Kill keep both). Portal tombstones the id at once, shuts the
// Computer down asynchronously if it's running, purges saved state after 25 hours,
// and never accepts the id again. Idempotent and non-disclosing: an unknown, already-deleted, or other project's
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
// {id, key, sandbox id, computerToken} without starting or attaching anything. Use it
// to keep driving a running Computer across requests from a backend that saved its
// ct_. An optional AdoptOptions
// preserves ephemeral mode and binding revision; omission uses persistent mode
// and revision zero. Lifecycle operations observe the Computer's current state.
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
