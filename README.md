# Pine Computer Go SDK

Pine's server-side Go SDK for the **Computer** — provision, bind, and drive a Pine
CUA browser Computer (sessions, agent, drive) from a backend.

```
go get go.pinesandbox.io/computer
```

The import path is a vanity path (decoupled from the backing repo). Pin within a
pool minor — `v0.3.x` targets `pine-cua-pool-v3` (the default profile).

### Private module setup

The module publishes to a **private** content-mirror, so `go` must skip the public
proxy + checksum DB and authenticate the git fetch:

```sh
go env -w GOPRIVATE=go.pinesandbox.io
# Authenticate the mirror over your existing GitHub credentials, e.g.:
git config --global url."git@github.com:".insteadOf "https://github.com/"
```

`GOPRIVATE` governs only the module fetch — vanity discovery
(`go.pinesandbox.io/computer?go-get=1`) is public, so no extra setup is needed for
the import-path resolution itself.

## Quickstart

```go
package main

import (
	"context"
	"log"

	pine "go.pinesandbox.io/computer"
)

func main() {
	ctx := context.Background()

	client, err := pine.NewClient(pine.ClientOptions{
		Endpoint: "https://staging.pinesandbox.io", // the domain your project was given
		APIKey:   "pk_…",                            // your project client key
	})
	if err != nil {
		log.Fatal(err)
	}

	// Provision + bind a fresh persistent Computer. PERSIST comp.ID()/comp.Key()
	// to re-attach later (AttachComputer restores its state onto a fresh pod).
	comp, err := client.CreateComputer(ctx, pine.AttachOptions{})
	if err != nil {
		log.Fatal(err)
	}
	defer comp.Stop(ctx) // graceful: persists state on the way out

	sess, err := comp.CreateSession(ctx, pine.CreateSessionOptions{Browser: true})
	if err != nil {
		log.Fatal(err)
	}

	// Delegate mode: one persistent agent task per session; each Run is a turn.
	if _, err := sess.Agent().Run(ctx, "Find the cheapest flight SFO→JFK next Friday", pine.RunOptions{}); err != nil {
		log.Fatal(err)
	}
	_, _ = sess.Agent().Events(ctx, "", func(data []byte) error {
		log.Printf("agent event: %s", data)
		return nil
	})
}
```

## Surface

`Client` → `Computer` → `Session`, with `Session.Agent()` (delegate mode) and
`Session.Drive()` (BYOA primitives). The package godoc is the authoritative
surface reference (`go doc go.pinesandbox.io/computer`), and the wire contract
is the published [API reference](https://pinesandbox.io/docs/integration). The
noun/verb set matches the Ruby `pinesandbox` gem one-for-one (the cross-SDK
`FACADE.md` contract). Errors are a flat, `errors.As`-able vocabulary
(`errors.go`).

Tokens are never method parameters — the SDK holds the credential ladder (pk_ →
project JWS → ct_ → ps_) and attaches the right one per call. To hand a browser
the desktop, `session.Delegate(ctx)` mints a short-lived view-only `dt_` envelope
carrying nothing privileged.

## Layout

- root package `pinesandbox` — the hand-written facade.
- `internal/base/*` — generic, Computer-agnostic primitives (zone, transport,
  SSE, problem, spec-version); enforced to never import a domain package.
- `internal/{bindhpke,bind,binder,tokens,controlplane,coordinator}` — the domain.
- `interop/` — monorepo-only module proving the bind HPKE envelope is byte-
  compatible with the coordinator's real implementation.

## Develop

> Contributor section — these paths are the Pine **monorepo** layout
> (`sdks/pine-computer/go/`), not the published module. The standalone published
> tree builds with `go test ./...` alone; the conformance/interop gates below
> need the monorepo and are skipped when the module is fetched on its own.

```
go test -race ./...      # unit + conformance gates (route + schema drift)
go vet ./... && gofmt -l .
cd interop && go test ./...   # SDK ⇄ coord HPKE interop (needs the monorepo)
```

The route/schema conformance gates read the `contract/computer-*.json` artifacts
beside the SDK in the monorepo, regenerated from the OpenAPI specs by
`specs/gen-computer-{routes,schemas}.py` and kept fresh by CI (single-source each
drift axis; machine-check it).
