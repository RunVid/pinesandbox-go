// Package coordinator is the data-plane client for a bound Computer: bind, session
// lifecycle, agent, and drive operations on the coordinator (the per-pod gateway). The
// bearer rides X-Pine-Auth (coord reads it and Authorization); every route is gated on a
// ct_ (computer-tier) or ps_ (per-session) token, passed per call by the caller. Coord
// errors are RFC-9457, mapped to *problem.APIError. Domain package (uses
// internal/base/{transport,spec,problem}). The hand-written wire types match
// specs/computer-api.yaml; the route table is drift-checked against the spec.
package coordinator

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// BindPubkey is the coordinator's per-process ephemeral HPKE public key + pod identity
// (GET v1/coord/bind-pubkey, public). The SDK seals the bind payload to EphemPub and echoes
// PodUID/CoordBootID back on bind.
type BindPubkey struct {
	PodUID      string
	CoordBootID string
	EphemPub    []byte // decoded X25519 public key (32 bytes)
	FetchedAt   *time.Time
}

type bindPubkeyWire struct {
	PodUID         string `json:"pod_uid"`
	CoordBootID    string `json:"coord_boot_id"`
	EphemPubX25519 string `json:"ephem_pub_x25519"` // base64url, 32 bytes
	FetchedAt      int64  `json:"fetched_at"`       // Unix epoch seconds (spec: integer)
}

func parseBindPubkey(body []byte) (*BindPubkey, error) {
	var w bindPubkeyWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable bind-pubkey: %w", err)
	}
	if w.PodUID == "" || w.CoordBootID == "" {
		return nil, fmt.Errorf("pinesandbox: bind-pubkey missing pod identity")
	}
	pub, err := base64.RawURLEncoding.DecodeString(w.EphemPubX25519)
	if err != nil {
		return nil, fmt.Errorf("pinesandbox: bind-pubkey ephem_pub_x25519 is not base64url: %w", err)
	}
	if len(pub) != 32 {
		return nil, fmt.Errorf("pinesandbox: bind-pubkey ephem_pub_x25519 is %d bytes, want 32", len(pub))
	}
	var fetched *time.Time
	if w.FetchedAt != 0 {
		t := time.Unix(w.FetchedAt, 0).UTC()
		fetched = &t
	}
	return &BindPubkey{PodUID: w.PodUID, CoordBootID: w.CoordBootID, EphemPub: pub, FetchedAt: fetched}, nil
}

// BindResult is the pod's computer token + epoch (POST v1/coord/bind).
type BindResult struct {
	ComputerToken string // ct_
	Epoch         int
}

func parseBindResult(body []byte) (*BindResult, error) {
	var w struct {
		ComputerToken string `json:"computer_token"`
		Epoch         int    `json:"epoch"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: unparseable bind result: %w", err)
	}
	if w.ComputerToken == "" {
		return nil, fmt.Errorf("pinesandbox: bind result missing computer_token")
	}
	return &BindResult{ComputerToken: w.ComputerToken, Epoch: w.Epoch}, nil
}

// Session is the per-window slice of a Computer (the create/get response). Token is the
// ps_ used to drive this session; the ct_ is the Computer's, not held here.
type Session struct {
	Name         string
	Label        string
	Token        string // ps_
	SessionDir   string
	Browser      *Browser
	Blind        bool
	CreatedAt    *time.Time
	LastActiveAt *time.Time
}

// Browser is the session's browser plane (present when the session was created with
// browser:true).
type Browser struct {
	PrimaryTabID string
	OwnedTabIDs  []string
	WindowID     int
	ActiveTabID  string
	WSURL        string
}

type sessionWire struct {
	Name         string       `json:"name"`
	Label        string       `json:"label"`
	Token        string       `json:"token"`
	SessionDir   string       `json:"session_dir"`
	Browser      *browserWire `json:"browser"`
	Blind        bool         `json:"blind"`
	CreatedAt    string       `json:"created_at"`
	LastActiveAt string       `json:"last_active_at"`
}

type browserWire struct {
	PrimaryTabID string   `json:"primary_tab_id"`
	OwnedTabIDs  []string `json:"owned_tab_ids"`
	WindowID     int      `json:"window_id"` // spec: integer
	ActiveTabID  string   `json:"active_tab_id"`
	WSURL        string   `json:"ws_url"`
}

func (w *sessionWire) toSession() *Session {
	if w == nil {
		return nil
	}
	s := &Session{
		Name:         w.Name,
		Label:        w.Label,
		Token:        w.Token,
		SessionDir:   w.SessionDir,
		Blind:        w.Blind,
		CreatedAt:    parseTime(w.CreatedAt),
		LastActiveAt: parseTime(w.LastActiveAt),
	}
	if w.Browser != nil {
		owned := w.Browser.OwnedTabIDs
		if owned == nil {
			owned = []string{}
		}
		s.Browser = &Browser{
			PrimaryTabID: w.Browser.PrimaryTabID,
			OwnedTabIDs:  owned,
			WindowID:     w.Browser.WindowID,
			ActiveTabID:  w.Browser.ActiveTabID,
			WSURL:        w.Browser.WSURL,
		}
	}
	return s
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}
