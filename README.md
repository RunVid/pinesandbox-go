# Pine Computer Go SDK

Pine's server-side Go SDK for the **Computer** — provision, bind, and drive a Pine
CUA browser Computer (sessions, agent, drive) from a backend.

```
go get go.pinesandbox.io/computer
```

The import path is a vanity path (decoupled from the backing repo). Pin within a
pool minor — `v0.3.x` targets the project's backend-managed v3 runtime policy.

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

	// Generate + PERSIST every caller-owned secret before provisioning.
	creds, err := pine.GenerateCredentials()
	if err != nil {
		log.Fatal(err)
	}
	capture, err := pine.GenerateCaptureKeypair(1)
	if err != nil {
		log.Fatal(err)
	}
	// persist(creds.ID, creds.Key, capture.Generation, capture.PK, capture.SK)

	// Provision + bind a fresh persistent Computer. AttachComputer later uses
	// the same current keypair plus any retained prior generations.
	comp, err := client.CreateComputer(ctx, pine.AttachOptions{
		Credentials:    creds,
		CaptureKeypair: capture,
	})
	if err != nil {
		log.Fatal(err)
	}
	// Persist the committed authorization winner before returning work to a
	// queue or another backend instance.
	// persistBinding(comp.ID(), comp.BindingRevision(), comp.SandboxID(), comp.ComputerToken())
	defer comp.Stop(ctx) // graceful: persists state on the way out

	sess, err := comp.CreateSession(ctx, pine.CreateSessionOptions{Browser: true})
	if err != nil {
		log.Fatal(err)
	}

	// Delegate mode: one persistent agent task per session; each Run is a turn.
	if _, err := sess.Agent().Run(ctx, "Find the cheapest flight SFO→JFK next Friday", pine.RunOptions{}); err != nil {
		log.Fatal(err)
	}

	// Events is a typed, resuming iterator (Go 1.23 range-over-func). The feed is
	// continuous across turns; a dropped connection auto-resumes from the last event id.
	for ev, err := range sess.Agent().Events(ctx, "") {
		if err != nil {
			log.Fatal(err) // terminal: auth, or the reconnect budget was exhausted
		}
		log.Printf("agent event: type=%s", ev.Type)
		if ev.Terminal { // this turn ended
			break
		}
	}
}
```

## Timeouts

Every call is bounded by the `context` you pass — the SDK applies a 30s fallback
only when your context has no deadline, so you extend (or shorten) any call with
`context.WithTimeout`. Provisioning is special-cased: `CreateComputer` /
`AttachComputer` bound the cold provision (`POST /computer-sandboxes` + readiness)
by `AttachOptions.Timeout` (the readiness budget, default 300s), so a cold pool
doesn't trip the 30s fallback. Raise `AttachOptions.Timeout` for an unusually cold
pool.

## Stateless reuse (multi-instance / restarted backends)

The handle isn't the source of truth — your **persisted credentials** are. A
stateless backend rebuilds the handles per request, no provisioning and no coord
round-trip for the session:

- **Computer** — persist `comp.ID()`, `comp.Key()`, the current and retained
  capture keypairs, `comp.BindingRevision()`, `comp.SandboxID()`, and
  `comp.ComputerToken()` (the `ct_`). Portal is an authorization ledger, not
  the source of truth for this integrator-owned record.
- **Session** — persist `sess.Name()` and `sess.Token()` (the `ps_`). The `ps_` is
  minted once at `CreateSession` and the coordinator redacts it on read, so a
  session re-fetched with `comp.Session(name)` has **no** `ps_` (its drive ops
  `401`); keep the create-time value and rebuild with `AdoptSession`.

```go
// Per request:
comp, err := client.AdoptExisting(ctx, id, key, sandboxID, ct) // rebuild the Computer
if err != nil { /* … */ }
sess, err := comp.AdoptSession(name, ps)                        // rebuild the session
if err != nil { /* … */ }

sess.CreateTab(ctx, "https://example.com", "")     // drive   → ps_
sess.TakeControl(ctx)                               // control → ct_ (WithForce() to override)
sess.Agent().Run(ctx, goal, pine.RunOptions{})      // agent   → ct_
```

Tokens split by tier: **`ct_` is the operator surface** — control lease, agent +
skills-authoring mutations, lifecycle; **`ps_` is the session's own drive + reads**.
The SDK attaches the right one per call.

Skill authoring is asynchronous: `Session.Learn`, `Session.Teach`, and
`Session.Refine` return the accepted author run; consume progress and its terminal
draft through `Session.AuthorEvents`.

When coord rejects a bound token, the SDK surfaces a typed
`*pine.TokenRejectedError` — a report, not an attach instruction (`ct_` is
binding-lifetime, so on a live pod this is `binding_auth_lost`). It never silently
re-mints (that would land a fresh pod and invalidate every `ps_`). Reconcile, and
recover by re-attaching **only when the sandbox is confirmed gone**:
`AttachComputer` mints a fresh `ct_` + sessions/`ps_`; persist the new creds and retry.

## Driving the Computer with an agent

A Computer is a real cloud browser + desktop + shell. There are **three ways** an
agent (or a human) can drive it — pick per task; they share one Computer/Session.

### 1. Delegate mode — Pine's resident agent drives

You hand a goal; Pine's in-Computer agent runs the perceive→act loop, pauses to ask
when it needs input, and reports a typed outcome. One persistent task per session;
each `Run` is a turn (the thread is the memory — `Reset` clears it).

```go
ag := sess.Agent()
if _, err := ag.Run(ctx, "Book the cheapest SFO→JFK flight next Friday", pine.RunOptions{}); err != nil {
	return err // errors.Is(err, pine.ErrSessionBusy) ⇒ a turn is already running
}
for ev, err := range ag.Events(ctx, "") {
	if err != nil {
		return err
	}
	switch ev.Type {
	case pine.EventNeedsInput: // the agent paused on a question
		if ask, ok := ev.Ask(); ok { // ask carries Question / Options / the ids
			_, _ = ag.AnswerAsk(ctx, ask, answer(ask.Question)) // no id plumbing
		}
	case pine.EventResult:
		res, _ := ag.Result(ctx) // TerminalReason, Summary, Usage, Artifacts, Findings
		if res.TerminalReason == pine.TerminalCompleted {
			log.Printf("done: %s", res.Summary)
		}
	}
	if ev.Terminal {
		break
	}
}
```

### 2. BYOA (bring your own agent) — your agent drives

Your own model loop drives the Computer with two primitives: `Observe` (a snapshot
of the active tab — screenshot + the coordinate metadata to map actions) and
`ComputerUse` (one low-level action: click / type / scroll / navigate / key / …).
You own the loop; Pine just executes perception and actions.

```go
drive := sess.Drive()
for {
	obs, err := drive.Observe(ctx) // obs.Screenshot (base64 PNG) + sizes for your model
	if err != nil {
		return err
	}
	action := yourModel.Next(obs) // YOUR agent decides the next step
	if action.Done {
		break
	}
	// params are action-specific, e.g. {"x": 480, "y": 220} or {"text": "hello"}.
	if _, err := drive.ComputerUse(ctx, action.Verb, action.Params); err != nil {
		return err
	}
}
```

For hand-coded steps, typed helpers wrap the common actions —
`drive.Click(ctx, x, y)`, `drive.TypeText(ctx, s)`, `drive.Key(ctx, "ctrl+a")`,
`drive.Scroll(ctx, x, y, "down", 3)`, `drive.Screenshot(ctx)` — so you don't
hand-build params; the raw `ComputerUse(action, params)` remains the escape hatch
for the long tail.

Mix the two freely on the same session — e.g. delegate a sub-goal, then take over
with your own `ComputerUse` calls, or vice-versa.

### 3. Human handoff — the live desktop in a browser

`Session.Delegate(ctx)` mints a browser-safe envelope (the Computer's host + a
short-lived `dt_` desktop token, **nothing privileged**). Hand it to the browser
and the web SDK (`@runvid/computer-web`) renders the live desktop for a human to
watch or take control:

```go
env, _ := sess.Delegate(ctx)         // {ComputerHost (https URI), DesktopToken, …}
json.NewEncoder(w).Encode(env)       // → your frontend
// In the browser: connectionFromDelegation(env) → <pine-desktop url=… token=…/>
```

## Surface

`Client` → `Computer` → `Session`, with `Session.Agent()` (delegate mode) and
`Session.Drive()` (BYOA primitives). The package godoc is the authoritative
surface reference (`go doc go.pinesandbox.io/computer`), and the wire contract
is the published [API reference](https://pinesandbox.io/docs/integration). The
noun/verb set matches the Ruby `pinesandbox` gem (the cross-SDK `FACADE.md`
contract — same verbs, presented idiomatically per language), with Go-idiomatic
conveniences on top: typed constants (`pine.EventNeedsInput`, `Controller*`, …),
option funcs (`TakeControl(ctx, WithForce())`), `AnswerAsk`, and computer-use
helpers (`Click`/`TypeText`/`Scroll`/…). Errors are a flat, `errors.As`-able
vocabulary (`errors.go`).

Tokens are never method parameters — the SDK holds the credential ladder (pk_ →
project JWS → ct_ → ps_) and attaches the right one per call. To hand a browser
the desktop, `session.Delegate(ctx)` mints a short-lived view-only `dt_` envelope
carrying nothing privileged.

## Troubleshooting — reporting an issue

Errors are **self-describing at the resource level**: every `*APIError` off a gateway-fronted
response folds the failing Computer + operation into `Error()` — resource first — so a generic
handler that logs only `err` shows WHICH Computer and WHICH call failed with no extra plumbing.
`Host` / `Op` / `RequestID` are also exported fields (the transport-fault `*TimeoutError` /
`*ConnectionError` and the control-plane / token errors carry the same context). The suffix
grammar is uniform `key=value` across the Go and Ruby SDKs; the Go SDK renders **`host=`**
because its data-plane client holds the gateway host, where the Ruby coordinator uses
**`sandbox=`** because its adapter holds the sandbox id (same Computer, different handle each
layer already has). `op` is always `<METHOD> <path>` with the query string stripped.
Portal token/attach errors additionally expose `Code` (the stable RFC 9457 problem type)
and optional `Reason`; both are spec-defined and safe to branch on. Raw server exceptions
are never part of those fields.

If `CreateComputer` returns `*AttachAuthorizationCommittedError`, Portal advanced the
binding revision before coordinator bind failed. Persist `BindingRevision`, `SandboxID`,
and the non-nil `Credentials` from that error before retrying or adopting the Computer;
their diagnostic formatting redacts both private keys.

```go
if _, err := sess.Agent().Run(ctx, "book the 9am slot"); err != nil {
    log.Printf("agent run failed: %v", err)
    // => "… (host=sbx_abc123.computer.<zone>, op=POST /v1/sessions/main/agent/run, request_id=req_abc123)"
    var apiErr *pinesandbox.APIError
    if errors.As(err, &apiErr) {
        report(computer.ID(), sess.Name(), time.Now().UTC(), apiErr.Host, apiErr.Op, apiErr.RequestID)
    }
}
```

When you file a report, **lead with the resource spine** — the durable key we pivot on across
time via logs / metrics resource attrs / `./pine debug` / the portal:

1. **`computer_id`** (`computer.ID()`) — WHICH Computer. Add the **session name** (`sess.Name()`)
   and, for an agent-lane issue, the **`task_id`**.
2. A **UTC time window**.
3. **`request_id`** *when you have it* — the precision handle for a single failed call
   (`apiErr.RequestID`, also in the message). A read or stream that fails *without* a response —
   a `*pinesandbox.TimeoutError` / `*ConnectionError`, or `ErrStreamLost` after a feed can't
   reconnect — carries no fresh id (a mid-stream `ErrStreamLost` does fold in the last live
   stream's `request_id`); the resource spine above is enough. Every one of these still names the
   failing Computer (`host=…`) + operation (`op=…`) in its message.

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
