// Package e2e is the Go SDK's end-to-end integration suite — it drives a REAL coordinator /
// Computer (the local OrbStack loop or staging), not httptest doubles. The tests are
// build-tagged `e2e` so the default `go test ./...` never compiles them, and env-gated on
// PINE_SANDBOX_ENDPOINT + PINE_SANDBOX_API_KEY (unset → skip; set → run + fail loud).
//
// It implements the shared, language-neutral journeys in
// sdks/pine-computer/contract/E2E_JOURNEYS.md — the SAME ones the Ruby suite runs against
// the SAME loop. Run via `./pine dev e2e-go`, or directly:
//
//	PINE_SANDBOX_ENDPOINT=… PINE_SANDBOX_API_KEY=pk_… go test -tags e2e -count=1 ./e2e/...
//
// This file (no build tag) keeps the package importable so `go test ./...` sees it as an
// empty package rather than erroring on excluded files.
package e2e
