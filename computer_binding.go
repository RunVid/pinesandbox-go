package pinesandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
)

// Binding reconnects to the running Computer. Store its JSON alongside Credentials in
// encrypted application storage. It contains a live token, not health evidence.
type Binding struct {
	ComputerID      string
	SandboxID       string
	ComputerToken   string
	BindingRevision int64
}

// Match the existing Computer host's DNS-label contract.
var bindingSandboxRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var bindingTokenRE = regexp.MustCompile(`^ct_[!-~]+$`)

func (b Binding) validate() error {
	if !computerIDRE.MatchString(b.ComputerID) || len(b.SandboxID) > 63 || !bindingSandboxRE.MatchString(b.SandboxID) ||
		!bindingTokenRE.MatchString(b.ComputerToken) || b.BindingRevision < 0 {
		return fmt.Errorf("pinesandbox: invalid Computer binding")
	}
	return nil
}

func (b Binding) String() string {
	return fmt.Sprintf("Binding{ComputerID:%s SandboxID:%s Token:[redacted]}", b.ComputerID, b.SandboxID)
}
func (b Binding) GoString() string { return b.String() }

// LogValue keeps structured logging separate from explicit JSON persistence.
func (b Binding) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("computer_id", b.ComputerID),
		slog.String("sandbox_id", b.SandboxID),
		slog.Int64("binding_revision", b.BindingRevision),
		slog.String("computer_token", "[redacted]"),
	)
}

// bindingSchema is the binding JSON this SDK writes. Schema 1, written before the
// sandbox id was named, is still read: it differs only in naming the sandbox id
// `runtime_id`.
const bindingSchema = 2

type bindingJSON struct {
	Schema          int    `json:"schema"`
	ComputerID      string `json:"computer_id"`
	SandboxID       string `json:"sandbox_id,omitempty"`
	LegacySandboxID string `json:"runtime_id,omitempty"`
	ComputerToken   string `json:"computer_token"`
	BindingRevision *int64 `json:"binding_revision"`
}

// MarshalJSON contains secrets for encrypted persistence. String and slog output
// are redacted; callers must not log explicitly marshaled JSON.
func (b Binding) MarshalJSON() ([]byte, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(bindingJSON{Schema: bindingSchema, ComputerID: b.ComputerID, SandboxID: b.SandboxID,
		ComputerToken: b.ComputerToken, BindingRevision: &b.BindingRevision})
}

// UnmarshalJSON reads the binding JSON every SDK shares (schema 2, or schema 1
// with `runtime_id`) and validates it before replacing b.
func (b *Binding) UnmarshalJSON(data []byte) error {
	var wire bindingJSON
	if err := json.Unmarshal(data, &wire); err != nil || wire.BindingRevision == nil {
		return fmt.Errorf("pinesandbox: invalid Computer binding JSON or schema")
	}
	var sandboxID string
	switch wire.Schema {
	case bindingSchema:
		sandboxID = wire.SandboxID
	case 1:
		sandboxID = wire.LegacySandboxID
	default:
		return fmt.Errorf("pinesandbox: invalid Computer binding JSON or schema")
	}
	next := Binding{wire.ComputerID, sandboxID, wire.ComputerToken, *wire.BindingRevision}
	if err := next.validate(); err != nil {
		return err
	}
	*b = next
	return nil
}

// Binding exports how to reach the running Computer. After a successful Stop there
// is no binding; keep Credentials independently for a later Start.
func (c *Computer) Binding() (Binding, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sandbox == nil || c.computerToken == "" {
		return Binding{}, ErrNotBound
	}
	b := Binding{c.id, c.sandbox.id, c.computerToken, c.bindingRevision}
	return b, b.validate()
}

// ReconnectComputer reconstructs exactly the saved binding without network I/O.
// If the Computer stops, it neither starts it again nor follows a newer binding.
func (c *Client) ReconnectComputer(ctx context.Context, creds Credentials, binding Binding) (*Computer, error) {
	if err := creds.validate(); err != nil {
		return nil, err
	}
	if err := binding.validate(); err != nil {
		return nil, err
	}
	if creds.ID != binding.ComputerID {
		return nil, fmt.Errorf("pinesandbox: credentials and binding belong to different Computers")
	}
	comp, err := c.AdoptExisting(ctx, creds.ID, creds.Key, binding.SandboxID, binding.ComputerToken,
		AdoptOptions{Ephemeral: creds.Ephemeral, BindingRevision: binding.BindingRevision})
	if err != nil {
		return nil, err
	}
	if err := comp.configureCaptureKeypairs(creds.CaptureKeypair, creds.PriorCaptureKeypairs); err != nil {
		return nil, err
	}
	for version, key := range creds.PriorKeys {
		if err := comp.AddPriorKey(version, key); err != nil {
			return nil, err
		}
	}
	return comp, nil
}
