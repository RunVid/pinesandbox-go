// Package pinesandbox is Pine's server-side Go SDK for the Computer — provision, bind, and
// drive a Pine CUA browser Computer (sessions, agent, drive). It composes the internal
// layers (zone/transport/control-plane/tokens/coordinator/bind handshake) behind a small
// hand-written facade. See sdks/pine-computer/contract/FACADE.md for the cross-SDK surface.
package pinesandbox

import (
	"context"
	"fmt"

	"go.pinesandbox.io/computer/internal/base/transport"
	"go.pinesandbox.io/computer/internal/base/zone"
	"go.pinesandbox.io/computer/internal/controlplane"
	"go.pinesandbox.io/computer/internal/coordinator"
	"go.pinesandbox.io/computer/internal/tokens"
)

// Connection is the resolved attach context: the zone (host derivation), the control-plane
// client, the attach-credential provider, and the coord-client factory. Client builds one
// and Computer.Attach drives through it.
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
	zone *zone.Zone
	conn *Connection
}

// ClientOptions configures NewClient.
type ClientOptions struct {
	// Endpoint is the portal URL or domain the project was given (required), e.g.
	// "https://staging.pinesandbox.io".
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
		return nil, fmt.Errorf("pinesandbox: Endpoint required (e.g. \"https://staging.pinesandbox.io\")")
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
	return &Client{zone: z, conn: conn}, nil
}

// CreateComputer provisions a NEW persistent Computer: a fresh UUIDv7 id +
// caller-owned state/capture keys, then a Portal-authorized bind. Portal lazily
// creates only its minimal authorization row during that atomic attach. Persist
// the identity, capture keypair, and returned BindingRevision before re-attach.
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
	comp := newComputer(creds.ID, creds.Key)
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
	if err := comp.configureCaptureKeypairs(opts.CaptureKeypair, opts.PriorCaptureKeypairs); err != nil {
		return nil, err
	}
	// A Portal authorization can commit before the later coordinator bind
	// fails. Keep a deep copy of every durable secret generated or selected by
	// this create call so that error path is recoverable even though there is no
	// Computer result to return.
	recoveryCredentials := &Credentials{
		ID:             creds.ID,
		Key:            append([]byte(nil), creds.Key...),
		CaptureKeypair: opts.CaptureKeypair.clone(),
	}
	// Already validated/configured above so malformed caller material cannot
	// provision a pod. Clear the value-copy options before the
	// public Attach path applies its direct-caller configuration hook.
	opts.CaptureKeypair = nil
	opts.PriorCaptureKeypairs = nil
	if err := comp.Attach(ctx, c.conn, opts); err != nil {
		return nil, attachClientError(comp, opts.BindingRevision, recoveryCredentials, err)
	}
	return comp, nil
}

// AttachComputer re-attaches an EXISTING persistent Computer by its stored id + state key.
// A fresh pod is provisioned and bound with the Computer's prior state restored.
func (c *Client) AttachComputer(ctx context.Context, id string, key []byte, opts AttachOptions) (*Computer, error) {
	if err := validateIdentity(id, key); err != nil {
		return nil, err
	}
	comp := newComputer(id, key)
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

// AdoptExisting adopts an ALREADY-bound, still-live Computer from its persisted
// {id, key, sandboxID, computerToken} — no new pod, no re-bind. For driving an existing pod
// across requests from a backend that cached the ct_.
func (c *Client) AdoptExisting(ctx context.Context, id string, key []byte, sandboxID, computerToken string) (*Computer, error) {
	if err := validateIdentity(id, key); err != nil {
		return nil, err
	}
	comp := newComputer(id, key)
	if err := comp.adopt(c.conn, sandboxID, computerToken, statusRunning); err != nil {
		return nil, err
	}
	return comp, nil
}
