package pinesandbox

import (
	"context"
	"encoding/json"
	"iter"

	"go.pinesandbox.io/computer/internal/coordinator"
)

// CreateSessionOptions configures Computer.CreateSession. Every field is optional; every
// session has its own browser window.
type CreateSessionOptions struct {
	Name  string // empty → the Computer picks a friendly name
	Label string
	// ArtifactAccess is what the session's agent may do with the Computer's artifacts.
	// Empty is ArtifactAccessNone: it can't use them. Your own access with the project
	// key (Client.Artifacts) doesn't depend on it.
	ArtifactAccess ArtifactAccess
}

// ArtifactAccess is what a session's agent may do with the Computer's artifacts, chosen
// at CreateSession (SessionInfo.ArtifactAccess reports it).
type ArtifactAccess = coordinator.ArtifactAccess

// Artifact access for a session.
const (
	// ArtifactAccessNone gives the session's agent no access; it isn't told about the
	// artifacts.
	ArtifactAccessNone = coordinator.ArtifactAccessNone
	// ArtifactAccessReadOnly lets the agent list and read files.
	ArtifactAccessReadOnly = coordinator.ArtifactAccessReadOnly
	// ArtifactAccessReadWrite also lets the agent save and remove files and make public
	// links.
	ArtifactAccessReadWrite = coordinator.ArtifactAccessReadWrite
)

// CreateSession opens a new session on the Computer (with the ct_) and returns it holding
// its own ps_. Save its Name() and Token(), with the Computer's BindingRevision(), when it
// returns: no other call returns the token, and AdoptSession needs both to drive the
// session from another request or process. The session ends when the Computer stops or
// is recovered; delete the record then.
func (c *Computer) CreateSession(ctx context.Context, opts CreateSessionOptions) (*Session, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	s, err := coord.CreateSession(ctx, ct, coordinator.CreateSessionOptions{
		Name: opts.Name, Label: opts.Label, ArtifactAccess: opts.ArtifactAccess,
	})
	if err != nil {
		return nil, err
	}
	return c.wrapSession(s), nil
}

// Session fetches an existing session's metadata by name (with the ct_).
//
// The Computer redacts the session's ps_ on read (it's handed out only at
// CreateSession), so the returned handle has no ps_: its ct_-routed ops (control,
// agent + authoring mutations) and the reads that accept either token (control state
// and events, agent reads, files, artifacts) work with the Computer token, but
// session-only ops (exec, tabs, drive actions, session passkeys) need the ps_. For reuse
// in a stateless backend, persist the ps_ from CreateSession and rebuild with AdoptSession.
func (c *Computer) Session(ctx context.Context, name string) (*Session, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	s, err := coord.GetSession(ctx, ct, name)
	if err != nil {
		return nil, err
	}
	return c.wrapSession(s), nil
}

// AdoptSession rebuilds a drive-capable *Session from a persisted ps_, with no
// request to the Computer — the session analog of Client.AdoptExisting and the reuse path
// for a stateless backend (persist {name, ps_} at CreateSession; per request
// AdoptExisting the Computer, then AdoptSession). The Computer must be attached; a
// wrong/expired ps_ surfaces as a 401 on first use.
func (c *Computer) AdoptSession(name, sessionToken string) (*Session, error) {
	if _, _, err := c.bound(); err != nil {
		return nil, err
	}
	return &Session{parent: c, name: name, token: sessionToken}, nil
}

// Sessions lists all of the Computer's sessions.
func (c *Computer) Sessions(ctx context.Context) ([]*Session, error) {
	coord, ct, err := c.bound()
	if err != nil {
		return nil, err
	}
	list, err := coord.ListSessions(ctx, ct)
	if err != nil {
		return nil, err
	}
	out := make([]*Session, 0, len(list))
	for _, s := range list {
		out = append(out, c.wrapSession(s))
	}
	return out, nil
}

// DestroySession deletes a session. clean:true also GCs its on-disk run dir.
func (c *Computer) DestroySession(ctx context.Context, name string, clean bool) error {
	coord, ct, err := c.bound()
	if err != nil {
		return err
	}
	return coord.DestroySession(ctx, ct, name, clean)
}

// bound returns the live coordinator client + ct_, or an error if the Computer isn't
// attached.
func (c *Computer) bound() (*coordinator.Client, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, coord, ct, err := c.boundLocked()
	return coord, ct, err
}

func (c *Computer) wrapSession(s *coordinator.Session) *Session {
	return &Session{parent: c, name: s.Name, token: s.Token, info: s}
}

// SessionInfo is the parsed session metadata (browser plane, timestamps, etc.). Re-exported
// so the public Session.Info() return type is nameable without importing an internal package
// (the name `Session` is the facade handle, so the metadata alias is `SessionInfo`).
type SessionInfo = coordinator.Session

// Browser is a session's browser-plane metadata (the SessionInfo.Browser field type).
type Browser = coordinator.Browser

// Session is the per-window slice of a Computer. It holds its ps_; agent, control and
// authoring mutations route through the Computer's ct_ (one active driver at a time), and
// reads that accept either token use the ps_ when the handle has one.
type Session struct {
	parent *Computer
	name   string
	token  string // ps_
	info   *coordinator.Session
}

// Name is the session name.
func (s *Session) Name() string { return s.name }

// Token is the session's ps_, or "" for a session read with Session or Sessions. Only
// CreateSession returns it: save it then.
func (s *Session) Token() string { return s.token }

// Info exposes the parsed session metadata (browser plane, timestamps, etc.).
func (s *Session) Info() *SessionInfo { return s.info }

// Agent returns the session's driver for Pine's agent (one persistent Task per session).
func (s *Session) Agent() *AgentMode { return &AgentMode{s: s} }

// Drive returns the primitives for driving the session with your own agent.
func (s *Session) Drive() *DriveMode { return &DriveMode{s: s} }

// Epoch returns the session's current epoch document (raw).
func (s *Session) Epoch(ctx context.Context) (json.RawMessage, error) {
	return sessionCall(s, func(coord *coordinator.Client, _ string) (json.RawMessage, error) {
		return coord.Epoch(ctx, s.token, s.name)
	})
}

// Focus raises the session's window.
func (s *Session) Focus(ctx context.Context) error {
	return sessionDo(s, func(coord *coordinator.Client, _ string) error {
		return coord.Focus(ctx, s.token, s.name)
	})
}

// RecreateTerminal rebuilds the session's bash terminal after a restart lost it.
func (s *Session) RecreateTerminal(ctx context.Context) error {
	return sessionDo(s, func(coord *coordinator.Client, _ string) error {
		return coord.RecreateTerminal(ctx, s.token, s.name)
	})
}

// client is the parent Computer's current coordinator and ct_, read together so a call
// never pairs one binding's ct_ with another's coordinator. After the Computer's Stop or
// Kill the session's calls fail with the Computer's *BindError without sending a request.
func (s *Session) client() (*coordinator.Client, string, error) {
	return s.parent.bound()
}

// sessionCall runs f with the parent Computer's current coordinator and ct_ (see client),
// or returns the Computer's *BindError without calling f.
func sessionCall[T any](s *Session, f func(coord *coordinator.Client, ct string) (T, error)) (T, error) {
	coord, ct, err := s.client()
	if err != nil {
		var zero T
		return zero, err
	}
	return f(coord, ct)
}

// sessionDo is sessionCall for a call that returns only an error.
func sessionDo(s *Session, f func(coord *coordinator.Client, ct string) error) error {
	coord, ct, err := s.client()
	if err != nil {
		return err
	}
	return f(coord, ct)
}

func failedSeq[T any](err error) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		yield(zero, err)
	}
}

// readToken is the token for a route that accepts either token: the session's ps_ when
// this handle has one, else ct, the Computer's ct_ from client() (a handle from
// Computer.Session or Computer.Sessions, whose ps_ the Computer redacts on read).
func (s *Session) readToken(ct string) string {
	if s.token != "" {
		return s.token
	}
	return ct
}
