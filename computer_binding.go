package pinesandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
)

// Binding reconnects to one runtime. Store its JSON alongside Credentials in
// encrypted application storage. It contains a live token, not health evidence.
type Binding struct {
	ComputerID      string
	RuntimeID       string
	ComputerToken   string
	BindingRevision int64
}

// Match the existing Computer host's DNS-label contract.
var bindingRuntimeRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var bindingTokenRE = regexp.MustCompile(`^ct_[!-~]+$`)

func (b Binding) validate() error {
	if !computerIDRE.MatchString(b.ComputerID) || len(b.RuntimeID) > 63 || !bindingRuntimeRE.MatchString(b.RuntimeID) ||
		!bindingTokenRE.MatchString(b.ComputerToken) || b.BindingRevision < 0 {
		return fmt.Errorf("pinesandbox: invalid Computer binding")
	}
	return nil
}

func (b Binding) String() string {
	return fmt.Sprintf("Binding{ComputerID:%s RuntimeID:%s Token:[redacted]}", b.ComputerID, b.RuntimeID)
}
func (b Binding) GoString() string { return b.String() }

// LogValue keeps structured logging separate from explicit JSON persistence.
func (b Binding) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("computer_id", b.ComputerID),
		slog.String("runtime_id", b.RuntimeID),
		slog.Int64("binding_revision", b.BindingRevision),
		slog.String("computer_token", "[redacted]"),
	)
}

type bindingJSON struct {
	Schema          int    `json:"schema"`
	ComputerID      string `json:"computer_id"`
	RuntimeID       string `json:"runtime_id"`
	ComputerToken   string `json:"computer_token"`
	BindingRevision *int64 `json:"binding_revision"`
}

// MarshalJSON contains secrets for encrypted persistence. String and slog output
// are redacted; callers must not log explicitly marshaled JSON.
func (b Binding) MarshalJSON() ([]byte, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(bindingJSON{1, b.ComputerID, b.RuntimeID, b.ComputerToken, &b.BindingRevision})
}

// UnmarshalJSON validates the shared Ruby/Go format before replacing b.
func (b *Binding) UnmarshalJSON(data []byte) error {
	var wire bindingJSON
	if err := json.Unmarshal(data, &wire); err != nil || wire.Schema != 1 || wire.BindingRevision == nil {
		return fmt.Errorf("pinesandbox: invalid Computer binding JSON or schema")
	}
	next := Binding{wire.ComputerID, wire.RuntimeID, wire.ComputerToken, *wire.BindingRevision}
	if err := next.validate(); err != nil {
		return err
	}
	*b = next
	return nil
}

// Binding exports the current runtime connection. After a successful Stop there
// is no binding; keep Credentials independently for a later Start.
func (c *Computer) Binding() (Binding, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sandbox == nil || c.computerToken == "" {
		return Binding{}, fmt.Errorf("pinesandbox: Computer has no runtime binding")
	}
	b := Binding{c.id, c.sandbox.id, c.computerToken, c.bindingRevision}
	return b, b.validate()
}

// ReconnectComputer reconstructs exactly the saved binding without network I/O.
// It neither allocates nor adopts a newer runtime when this one disappears.
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
	comp, err := c.AdoptExisting(ctx, creds.ID, creds.Key, binding.RuntimeID, binding.ComputerToken,
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
