// Package pinesandbox is the Pine Computer server SDK for Go.
//
// Use it from your backend to create and start Computers, open sessions, give
// Pine's agent a goal or drive a session with your own agent, run shell
// commands, move files, manage control, and hand a browser-safe
// DelegatedConnection to your web app. It holds project and Computer
// credentials, so it never runs in a browser. Its nouns and verbs match the
// Ruby `pinesandbox` gem.
//
// Integration guide: https://pinesandbox.io/docs/integration. API reference:
// https://pinesandbox.io/spec/computer and https://pinesandbox.io/spec/portal.
package pinesandbox

// Version is this SDK's release version. SDK patches ship independently within
// the shared compatibility minor.
const Version = "0.3.17"

// specs/check-version-identity.py reads this constant (SpecVersion = <n>) and
// checks it against the API spec and the other SDKs.

// SpecVersion is the Computer API major version this SDK targets. A Computer
// that serves a different major version fails with SpecVersionMismatch.
const SpecVersion = 1
