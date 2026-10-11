// Package pinesandbox is the Pine Computer server SDK for Go.
//
// It presents the same nouns/verbs as the Ruby `pinesandbox` gem for integrator
// backends: provision/attach a Computer, drive sessions (agent / BYOA-drive / shell /
// files / control), and hand a browser-safe DelegatedConnection to a web app.
// Same audience as the Ruby gem — never a browser.
//
// Integration guide: https://pinesandbox.io/docs/integration. The wire contract is the
// published OpenAPI reference: https://pinesandbox.io/spec/computer and
// https://pinesandbox.io/spec/coordinator.
package pinesandbox

// Version is this SDK's release version. SDK patches ship independently within
// the shared compatibility minor.
const Version = "0.3.15"

// SpecVersion is the Computer wire-major this SDK targets — the CG-1 version-identity
// source. It MUST equal computer-api.yaml's x-pine-spec-version, the gateway's
// specVersion const, and Ruby's DelegatedConnection::SPEC_VERSION;
// specs/check-version-identity.py greps this constant (regex SpecVersion = <n>).
const SpecVersion = 1
