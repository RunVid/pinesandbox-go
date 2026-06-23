package pinesandbox

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"go.pinesandbox.io/computer/internal/bind"
	"go.pinesandbox.io/computer/internal/binder"
	"go.pinesandbox.io/computer/internal/coordinator"
)

// CurrentKeyVersion is the version stamped on the current state key (rotation only).
const CurrentKeyVersion = binder.CurrentKeyVersion

// DefaultProfile is the profile attach uses when none is given.
const DefaultProfile = "pine_cua_v3"

const defaultAttachTimeout = 300 * time.Second

type profileSpec struct {
	imageEnv     string
	imageDefault string
	pool         string
}

// profiles is the catalog of supported profiles → image + warm pool (mirrors the Ruby
// PROFILES). The image is ENV-overridable at call time.
var profiles = map[string]profileSpec{
	// v3 — the current pool (new-spec wire flip); the default (see DefaultProfile).
	"pine_cua_v3": {"PINE_CUA_V3_IMAGE", "us-central1-docker.pkg.dev/pineai-hk/infra/pine-cua-browser:v3.0.0", "pine-cua-pool-v3"},
	// v2 — previous pool. Kept for existing integrations; set Profile="pine_cua_v2".
	"pine_cua_v2": {"PINE_CUA_V2_IMAGE", "us-central1-docker.pkg.dev/pineai-hk/infra/pine-cua-browser:v2.0.0", "pine-cua-pool-v2"},
	"pine_cua":    {"PINE_CUA_IMAGE", "us-central1-docker.pkg.dev/pineai-hk/infra/pine-cua-browser:latest", "pine-cua-pool"},
}

// defaultResourceLimits / defaultEntrypoint match the pool-cua-v2 chart values (Helm parity).
var defaultResourceLimits = map[string]string{"cpu": "4", "memory": "6Gi", "ephemeral-storage": "5Gi"}
var defaultEntrypoint = []string{"/bin/sh", "-c", "/opt/pine/entrypoint.sh & exec /opt/opensandbox/bin/task-executor -listen-addr=0.0.0.0:5758"}

// Profiles returns the supported profile names (e.g. for a picker).
func Profiles() []string { return []string{"pine_cua_v3", "pine_cua_v2", "pine_cua"} }

// AttachOptions configures CreateComputer / AttachComputer / Computer.Attach.
type AttachOptions struct {
	// Credentials reuses a pre-generated identity (CreateComputer only); empty mints one.
	Credentials Credentials

	Profile        string        // default DefaultProfile
	Pool           string        // override the profile's pool
	Image          string        // override the profile's image
	Timeout        time.Duration // sandbox TTL + readiness wait, default 300s
	ResourceLimits map[string]string
	Entrypoint     []string
	PodEnv         map[string]string
	Metadata       map[string]string
	IdempotencyKey string

	MaxBindAttempts  int           // default binder.DefaultMaxBindAttempts
	BindReadyTimeout time.Duration // default binder.DefaultReadyTimeout
}

// Computer is a persistent Pine CUA Computer. Identity is (ID, Key); a powered-on Computer
// also holds a sandbox handle, a coordinator client, and the pod's ct_.
type Computer struct {
	id        string
	key       []byte
	priorKeys map[int][]byte

	mu            sync.Mutex
	conn          *Connection
	sandbox       *SandboxHandle
	coord         *coordinator.Client
	computerToken string // ct_
	profile       string
	sandboxTTL    time.Duration
}

func newComputer(id string, key []byte) *Computer {
	return &Computer{id: id, key: key, priorKeys: map[int][]byte{}}
}

// ID is the Computer's stable id.
func (c *Computer) ID() string { return c.id }

// Key is the Computer's 32-byte state key (persist it to re-attach).
func (c *Computer) Key() []byte { return c.key }

// SandboxID is the bound pod's sandbox id (empty if not attached).
func (c *Computer) SandboxID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sandbox == nil {
		return ""
	}
	return c.sandbox.ID()
}

// ComputerToken is the pod's ct_ (empty if not attached). @api advanced — prefer the typed
// Session/Computer methods, which attach the right token.
func (c *Computer) ComputerToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.computerToken
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
	return fmt.Sprintf("Computer{id:%s attached:%t sandbox:%s key+ct_ redacted}", c.id, c.sandbox != nil, sid)
}

// GoString redacts secrets under %#v.
func (c *Computer) GoString() string { return c.String() }

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
	c.priorKeys[version] = key
	return nil
}

// Attach provisions a fresh pod via the control plane, waits for Running, and runs the bind
// handshake — leaving the Computer ready to create sessions. Refuses to re-attach a live
// Computer (call Stop or Kill first).
func (c *Computer) Attach(ctx context.Context, conn *Connection, opts AttachOptions) error {
	c.mu.Lock()
	if c.sandbox != nil {
		c.mu.Unlock()
		return bind.NewBindError(0, "Computer already attached — call Stop (graceful) or Kill first", nil)
	}
	c.mu.Unlock()

	profile := opts.Profile
	if profile == "" {
		profile = DefaultProfile
	}
	spec, ok := profiles[profile]
	if !ok {
		return fmt.Errorf("pinesandbox: unknown profile %q (known: %v)", profile, Profiles())
	}
	image := opts.Image
	if image == "" {
		if env := os.Getenv(spec.imageEnv); env != "" {
			image = env
		} else {
			image = spec.imageDefault
		}
	}
	pool := opts.Pool
	if pool == "" {
		pool = spec.pool
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultAttachTimeout
	}

	info, err := conn.controlPlane.Create(ctx, buildCreateBody(image, timeout, opts, pool), opts.IdempotencyKey)
	if err != nil {
		return err
	}
	sandbox := newSandboxHandle(conn.controlPlane, info.ID, info.Status)
	if err := sandbox.WaitUntilRunning(ctx, timeout, 0); err != nil {
		_ = sandbox.Kill(ctx) // best-effort cleanup of a pod that never came up
		return err
	}

	coord, err := conn.newCoord(info.ID)
	if err != nil {
		_ = sandbox.Kill(ctx)
		return err
	}
	res, err := binder.Bind(ctx, binder.Config{
		Coord:           coord,
		Minter:          conn.attachProvider,
		ComputerID:      c.id,
		Key:             c.key,
		PriorKeys:       c.priorKeysCopy(),
		SandboxID:       info.ID,
		Profile:         profile,
		TTLSeconds:      ttlSecondsPtr(timeout),
		MaxBindAttempts: opts.MaxBindAttempts,
		ReadyTimeout:    opts.BindReadyTimeout,
	})
	if err != nil {
		_ = sandbox.Kill(ctx) // single-use pod — recovery is a fresh attach
		return err
	}

	c.mu.Lock()
	c.conn, c.sandbox, c.coord, c.computerToken = conn, sandbox, coord, res.ComputerToken
	c.profile, c.sandboxTTL = profile, timeout
	c.mu.Unlock()
	return nil
}

// adopt wires an already-bound, still-live pod (no provision, no bind).
func (c *Computer) adopt(conn *Connection, sandboxID, computerToken, status string) error {
	coord, err := conn.newCoord(sandboxID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = conn
	c.sandbox = newSandboxHandle(conn.controlPlane, sandboxID, status)
	c.coord = coord
	c.computerToken = computerToken
	return nil
}

// Stop gracefully terminates the pod (persisting state on the way out) and drops the local
// binding. Returns true once the Sandbox record is confirmed gone.
func (c *Computer) Stop(ctx context.Context) (bool, error) {
	c.mu.Lock()
	sandbox, coord, ct := c.sandbox, c.coord, c.computerToken
	c.mu.Unlock()
	if sandbox == nil {
		return true, nil
	}
	// Best-effort durable checkpoint BEFORE terminate, under the current epoch — closes the
	// race where a fast re-attach's higher epoch fences out the old pod's SIGTERM final
	// capture (silent state loss). The SIGTERM capture stays the net if this fails (Ruby parity).
	if coord != nil && ct != "" {
		_, _ = coord.Capture(ctx, ct)
	}
	gone, err := sandbox.Terminate(ctx, defaultTerminateWait, defaultPollInterval)
	c.reset()
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
	c.reset()
	return ok
}

// Alive reports whether the bound pod is still live.
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
		out[k] = v
	}
	return out
}

func buildCreateBody(image string, timeout time.Duration, opts AttachOptions, pool string) map[string]any {
	rl := opts.ResourceLimits
	if rl == nil {
		rl = defaultResourceLimits
	}
	ep := opts.Entrypoint
	if ep == nil {
		ep = defaultEntrypoint
	}
	body := map[string]any{
		"image":          map[string]string{"uri": image},
		"timeout":        int(timeout.Seconds()),
		"resourceLimits": rl,
		"entrypoint":     ep,
	}
	if opts.PodEnv != nil {
		body["env"] = opts.PodEnv
	}
	if opts.Metadata != nil {
		body["metadata"] = opts.Metadata
	}
	if pool != "" {
		body["extensions"] = map[string]any{"poolRef": pool}
	}
	return body
}

func ttlSecondsPtr(d time.Duration) *int {
	s := int(d.Seconds())
	return &s
}
