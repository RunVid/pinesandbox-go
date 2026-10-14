package pinesandbox

import (
	"context"
	"errors"
	"fmt"

	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/base/zone"
	"go.pinesandbox.io/computer/internal/controlplane"
	"go.pinesandbox.io/computer/internal/coordinator"
	"go.pinesandbox.io/computer/internal/tokens"
)

// Connection is the resolved attach context a Client builds: where the project's
// Computers live and the clients that allocate and authorize them. Computer.Attach
// takes one.
type Connection struct {
	zone           *zone.Zone
	controlPlane   *controlplane.Client
	attachProvider *tokens.AttachCredentialsSource
	specMajor      int
	// newCoord builds the coordinator client for a sandbox's data host. A field so tests
	// can point it at an httptest server instead of the derived gateway host.
	newCoord func(sandboxID string) (*coordinator.Client, error)
}

// Client is the SDK entry point. It holds the project credential (pk_) + endpoint once and
// returns ready Computers.
type Client struct {
	zone            *zone.Zone
	conn            *Connection
	portalTransport *transport.Client
	projectKey      string
}

// ClientOptions configures NewClient.
type ClientOptions struct {
	// Endpoint is the portal URL or domain the project was given (required), e.g.
	// "https://pinesandbox.io".
	Endpoint string
	// APIKey is the project client key (pk_) — required.
	APIKey string
	// ControlHost optionally overrides the derived control-plane host.
	ControlHost string
	// SpecVersion pins the Computer-API major (default: the SDK's SpecVersion).
	SpecVersion int
}

// NewClient wires the SDK from a pk_ + endpoint.
func NewClient(opts ClientOptions) (*Client, error) {
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("pinesandbox: Endpoint required (e.g. \"https://pinesandbox.io\")")
	}
	if opts.APIKey == "" {
		return nil, fmt.Errorf("pinesandbox: APIKey required (a pk_ client key)")
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

	controlTransport := transport.New(z.HTTPScheme(), z.ControlHost())
	tokenSource, err := tokens.NewControlTokenSource(controlTransport, opts.APIKey)
	if err != nil {
		return nil, err
	}
	attachProvider, err := tokens.NewAttachCredentialsSource(controlTransport, opts.APIKey)
	if err != nil {
		return nil, err
	}
	cp := controlplane.NewClient(controlTransport, tokenSource, specMajor)

	conn := &Connection{
		zone:           z,
		controlPlane:   cp,
		attachProvider: attachProvider,
		specMajor:      specMajor,
		newCoord: func(sandboxID string) (*coordinator.Client, error) {
			host, err := z.DataHost(sandboxID)
			if err != nil {
				return nil, err
			}
			return coordinator.NewClient(transport.New(z.HTTPScheme(), host), specMajor), nil
		},
	}
	return &Client{zone: z, conn: conn, portalTransport: controlTransport, projectKey: opts.APIKey}, nil
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
	reference, err := c.conn.attachProvider.CurrentBinding(ctx, id)
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
	return newSandboxHandle(c.conn.controlPlane, reference.SandboxID, "").Stop(ctx)
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
	reference, err := c.conn.attachProvider.CurrentBinding(ctx, id)
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
				return nil, fmt.Errorf("pinesandbox: Computer still has a runtime; Stop it or Resume retained memory before Start")
			}
			handle := newSandboxHandle(c.conn.controlPlane, reference.SandboxID, info.Status)
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
func (c *Client) AttachComputer(ctx context.Context, id string, key []byte, opts AttachOptions) (*Computer, error) {
	return c.startComputer(ctx, id, key, opts, false)
}

// DeleteComputer PERMANENTLY deletes the Computer with this id: its identity and saved
// state end (Stop and Kill keep both). Portal tombstones the id at once, destroys any
// bound sandbox asynchronously, purges saved state after 24 hours, and never accepts the
// id again. Idempotent and non-disclosing: an unknown, already-deleted, or other project's
// id also returns nil. There is no undelete. Use Computer.Delete to kill a live handle
// first. Errors: *ComputerDeletionError (422 malformed id), *ProjectAccessDenied (403),
// *AttachCredentialsError (401, 429, 5xx).
func (c *Client) DeleteComputer(ctx context.Context, id string) error {
	return c.conn.attachProvider.DeleteComputer(ctx, id)
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
// {id, key, sandboxID, computerToken} without allocating or attaching anything. Use it
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
