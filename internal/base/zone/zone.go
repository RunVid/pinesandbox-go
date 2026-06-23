// Package zone derives the control-plane host (api.<zone>) and each Computer's
// data-plane host (<sandbox_id>.computer.<zone>) from the single configured endpoint.
// Behavior is pinned by sdks/pine-computer/contract/zone-vectors.json — the conformance
// test loads a module-local copy and asserts every vector (drift fails CI).
//
// This is a generic base primitive (internal/base): it is Computer-agnostic and must
// not import any domain package (the base ↛ domain boundary, design §3).
package zone

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// sandboxIDRE matches a DNS-safe label: lowercase-alphanumeric groups, hyphen-separated,
// no leading/trailing/consecutive hyphen, no underscore, no uppercase.
var sandboxIDRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const maxSandboxIDLen = 63 // DNS label limit

// Zone is a parsed endpoint. The control host and every data host derive from the one
// base zone string (+ a local-dev gateway port for non-secure zones).
type Zone struct {
	zone        string // base zone host, e.g. "staging.pinesandbox.io" or "lvh.me"
	port        string // local-dev gateway port; "" on secure (prod) zones
	secure      bool
	controlHost string
}

// Option configures Parse.
type Option func(*Zone)

// WithControlHost overrides the derived api.<zone> control host. Local/dev only — e.g.
// "api.lvh.me:18080", where one gateway port multiplexes control + data.
func WithControlHost(h string) Option { return func(z *Zone) { z.controlHost = h } }

// Parse derives a Zone from an endpoint (URL, bare domain, or host:port), stripping any
// scheme and path. It rejects: an empty endpoint; a dotless host (bare "localhost" — use
// a *.localhost subdomain); a derived host passed as the base (api.<zone> or
// <id>.computer.<zone>); a non-numeric / out-of-range port; and a local (non-secure)
// zone with no port.
func Parse(endpoint string, opts ...Option) (*Zone, error) {
	s := strings.TrimSpace(endpoint)
	if s == "" {
		return nil, fmt.Errorf("zone: empty endpoint")
	}
	if i := strings.Index(s, "://"); i >= 0 { // drop scheme
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 { // drop path
		s = s[:i]
	}

	host, port := s, ""
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		host, port = s[:i], s[i+1:]
		n, err := strconv.Atoi(port)
		if err != nil {
			return nil, fmt.Errorf("zone: non-numeric port %q", port)
		}
		if n < 1 || n > 65535 {
			return nil, fmt.Errorf("zone: port %d out of range", n)
		}
	}

	if host == "" {
		return nil, fmt.Errorf("zone: empty host")
	}
	if !strings.Contains(host, ".") {
		return nil, fmt.Errorf("zone: host %q has no dot — use a subdomain (e.g. *.localhost)", host)
	}
	if strings.HasPrefix(host, "api.") {
		return nil, fmt.Errorf("zone: %q is a derived control host — pass the base endpoint", host)
	}
	if strings.Contains(host, ".computer.") {
		return nil, fmt.Errorf("zone: %q is a derived Computer host — pass the base endpoint", host)
	}

	z := &Zone{zone: host, port: port, secure: !isLocal(host)}
	if !z.secure && port == "" {
		return nil, fmt.Errorf("zone: local zone %q requires a port", host)
	}
	z.controlHost = "api." + host
	for _, o := range opts {
		o(z)
	}
	return z, nil
}

// isLocal reports whether the zone is a local-dev (non-TLS) zone.
func isLocal(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		host == "lvh.me" || strings.HasSuffix(host, ".lvh.me")
}

// ControlHost is api.<zone> (or the WithControlHost override). All control-plane traffic
// (pk_ issuance, project-token lifecycle) targets it; the gateway routes by path.
func (z *Zone) ControlHost() string { return z.controlHost }

// DataHost is <sandbox_id>.computer.<zone>, port-suffixed on non-secure local zones.
func (z *Zone) DataHost(sandboxID string) (string, error) {
	if len(sandboxID) > maxSandboxIDLen || !sandboxIDRE.MatchString(sandboxID) {
		return "", fmt.Errorf("zone: invalid sandbox id %q", sandboxID)
	}
	h := sandboxID + ".computer." + z.zone
	if !z.secure {
		h += ":" + z.port
	}
	return h, nil
}

// HTTPScheme is https for secure zones, http for local-dev (localhost/lvh.me) zones.
func (z *Zone) HTTPScheme() string {
	if z.secure {
		return "https"
	}
	return "http"
}

// Secure reports whether the zone uses TLS (false only for localhost/*.localhost/
// lvh.me/*.lvh.me).
func (z *Zone) Secure() bool { return z.secure }
