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

// CreateComputer provisions a NEW persistent Computer: a fresh UUIDv7 id + 32-byte state
// key, registered with the portal, then bound. PERSIST computer.ID()/Key() to re-attach
// later. Pass Credentials via opts.Credentials to reuse a pre-generated identity.
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
	if err := c.conn.attachProvider.RegisterComputer(ctx, creds.ID); err != nil {
		return nil, err
	}
	comp := newComputer(creds.ID, creds.Key)
	if err := comp.Attach(ctx, c.conn, opts); err != nil {
		return nil, err
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
	if err := comp.Attach(ctx, c.conn, opts); err != nil {
		return nil, err
	}
	return comp, nil
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
