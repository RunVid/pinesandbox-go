package pinesandbox

import (
	"context"
	"encoding/json"
	"errors"
)

// DelegatedConnection is the browser-safe connection envelope — the ONLY thing a server
// hands a browser to drive a Computer's desktop. It carries NO ct_/ps_/project-JWS: only the
// data-plane base URL, the visible session's name, the pinned spec major, and a freshly-minted
// short-lived dt_ desktop token (+ its expiry). The browser cannot mint a dt_ (that
// needs the server-held ct_), so the integrator re-mints server-side and refreshes the
// envelope before each (re)connect. MarshalJSON emits the web-SDK wire shape.
type DelegatedConnection struct {
	// ComputerHost is the Computer's full data-plane origin — https://<id>.computer.<zone>
	// (a complete URI per computer-api.yaml, NOT a bare host), so the web SDK derives the
	// desktop's ws/wss scheme from it instead of guessing.
	ComputerHost          string
	SessionName           string
	SpecVersion           int
	DesktopToken          string
	DesktopTokenExpiresAt string
}

// MarshalJSON renders the wire shape the web SDK consumes (the session reduced to its name —
// never anything token-bearing).
func (d DelegatedConnection) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"computer_host": d.ComputerHost,
		"session":       map[string]string{"name": d.SessionName},
		"spec_version":  d.SpecVersion,
		"transports": map[string]any{
			"desktop_token": map[string]string{"token": d.DesktopToken, "expires_at": d.DesktopTokenExpiresAt},
		},
	})
}

// Delegate builds the browser-safe DelegatedConnection for this session: the data-plane
// base URL, the session name, the pinned spec major, and a fresh dt_ (ct_-minted). The one call
// a server makes to hand a browser everything it needs — and nothing it shouldn't. Re-call
// server-side to refresh the dt_ before each (re)connect.
func (s *Session) Delegate(ctx context.Context) (*DelegatedConnection, error) {
	dt, err := s.DesktopToken(ctx)
	if err != nil {
		return nil, err
	}
	return &DelegatedConnection{
		ComputerHost:          s.coord.BaseURL(),
		SessionName:           s.name,
		SpecVersion:           SpecVersion,
		DesktopToken:          dt.Token,
		DesktopTokenExpiresAt: dt.ExpiresAt,
	}, nil
}

// DelegateDesktop is the ergonomic one-call desktop delegation: ensure the named browser
// session exists (create:true creates it if absent), then return its DelegatedConnection.
func (c *Computer) DelegateDesktop(ctx context.Context, name string, create, browser bool) (*DelegatedConnection, error) {
	if name == "" {
		name = "main"
	}
	s, err := c.Session(ctx, name)
	if err != nil {
		// A missing session is a coordinator RFC-9457 404 (*APIError), not the control-plane
		// *NotFoundError.
		var ae *APIError
		if !create || !errors.As(err, &ae) || ae.Status != 404 {
			return nil, err
		}
		s, err = c.CreateSession(ctx, CreateSessionOptions{Name: name, Browser: browser})
		if err != nil {
			return nil, err
		}
	}
	return s.Delegate(ctx)
}
