# Pine Computer Go SDK

Pine's server-side Go SDK for the **Computer** — provision, bind, and drive a Pine
CUA browser Computer (sessions, agent, drive) from a backend.

```
go get go.pinesandbox.io/computer
```

The import path is a stable public vanity path. Pin the current `v0.3.x`
compatibility line.

The vanity module and its backing mirror are public. No `GOPRIVATE` setting,
GitHub account, or package token is required.

## Lifecycle operations

Start restores supported saved state into a fresh runtime and binding. Stop
saves supported state and releases that runtime. Live sessions and their tokens
end at Stop. Pause/Resume is a distinct lifecycle: Resume continues the
retained binding and sessions and never falls back to Start after snapshot loss.

`StartComputer` is the saved-state operation; `AttachComputer` remains its
compatible alias. `Computer.Pause(ctx)` and `Computer.Resume(ctx)` wait for
observed completion, bounded by six minutes or an earlier caller deadline. A
failed operation retains the existing binding for inspection or retry. Stop
resumes a paused runtime before saving. Internal control-plane adapters are not
an integrator API.

Pause requires a project opt-in and a snapshot-capable runtime. It is off by
default; an operator enables it in the project's feature settings. Refresh the
project token after enabling. Removing the grant blocks new pauses while
preserving Resume and Stop of retained memory. Ordinary Stop/Start is unaffected.

Do not turn a Resume error into an automatic `StartComputer` call. A timeout
does not cancel the operation, and unavailability does not establish memory
loss. Retry Resume against the retained handle; use Start as an explicit
saved-state recovery choice with new sessions and possible loss of unsaved
work. The [Computer API](../../../specs/computer-api.yaml) defines the observed
states and recovery choices.

## Quickstart

```go
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"

	pine "go.pinesandbox.io/computer"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()

	client, err := pine.NewClient(pine.ClientOptions{
		Endpoint: "https://pinesandbox.io",
		APIKey:   "pk_…",                            // your project client key
	})
	if err != nil {
		return err
	}

	// Generate + PERSIST every caller-owned secret before provisioning.
	creds, err := pine.GenerateCredentials()
	if err != nil {
		return err
	}
	// json.Marshal(creds) produces one value to save in your encrypted storage.
	// Save it before provisioning; Start uses the same value later.
	// Provision + bind a fresh persistent Computer. AttachComputer later uses
	// the same current keypair plus any retained prior generations.
	comp, err := client.CreateComputer(ctx, pine.AttachOptions{
		Credentials:    creds,
		// Optional on first attach. Country is canonical ISO 3166-1 alpha-2;
		// omit Location to use Portal's server-owned default.
		Location: &pine.ComputerLocation{Country: "US"},
	})
	if err != nil {
		return err
	}
	defer func() {
		if stopped, err := comp.Stop(ctx); err != nil {
			log.Print(err)
		} else if !stopped {
			log.Print("Stop is unconfirmed; retry using the persisted binding")
		}
	}()

	sess, err := comp.CreateSession(ctx, pine.CreateSessionOptions{Browser: true})
	if err != nil {
		return err
	}

	// Delegate mode: one persistent agent task per session; each Run is a turn.
	cursor := "" // fresh session; load the persisted cursor before Run when reusing one
	if _, err := sess.Agent().Run(ctx, "Find the cheapest flight SFO→JFK next Friday", pine.RunOptions{}); err != nil {
		return err
	}

	// Events is a typed, resuming iterator (Go 1.23 range-over-func). The feed is
	// continuous across turns; a dropped connection auto-resumes from the last event id.
	input := bufio.NewScanner(os.Stdin)
	completed := false
	for ev, err := range sess.Agent().Events(ctx, cursor) {
		if err != nil {
			return err // terminal: auth, or the reconnect budget was exhausted
		}
		log.Printf("agent event: type=%s", ev.Type)
		if ask, ok := ev.Ask(); ok {
			fmt.Printf("%s\n> ", ask.Question)
			if !input.Scan() {
				return fmt.Errorf("input closed while the agent was waiting: %v", input.Err())
			}
			if _, err := sess.Agent().AnswerAsk(ctx, ask, input.Text()); err != nil {
				return err
			}
		}
		if ev.Terminal {
			completed = true
			break
		}
	}
	if !completed && ctx.Err() == nil {
		return fmt.Errorf("agent event stream ended before completion")
	}
	return ctx.Err()
}
```

`Stop` admits graceful Stop to the runtime controller and waits up to 390s for
compute release. Caller cancellation does not cancel accepted work. Reconstruct a
saved handle on another backend to observe or join it; no SDK-local save receipt
is needed. `*pine.StopSaveError` retains the binding and reports whether retrying
can help. A coordinator restart may leave the final save unconfirmed.

Check both results: `false, nil` means release is unconfirmed; cancellation remains
an error. Keep the saved binding until Stop returns true. Success proves compute
release or authoritative absence, not that a runtime lost to expiry saved its last
changes. Snapshot storage cleanup can remain after compute release. Ephemeral
bindings skip saving based on the runtime's binding record.

If a Start response was lost before saving the runtime token, call
`client.StopComputer(ctx, id)` with project authorization alone, then Start
separately. It resolves one binding and never follows a concurrent newer binding.
Start still requires the private keys; the ID cannot recover those keys.

Save `json.Marshal(creds)` before Create. Load it into `pine.Credentials` with
`json.Unmarshal`, then call `client.StartComputer(ctx, creds, pine.StartOptions{})`
or `client.RecoverComputer(ctx, creds, pine.StartOptions{})`. Only runtime options
belong in that options value. The released `AttachComputer(ctx, id, key, opts)`
form remains available.

`comp.Credentials()` returns an independent copy, including retained keys needed
for older saved state. Save again only after changing keys, not after every
Start. Ruby reads and writes the same JSON format. It contains secret material;
Base64 encoding is not encryption. Complete persistent records from the released
Go format remain readable. Upgrade every reader before writing the shared format:
old Go readers silently omit its capture keypair. Legacy records without a
capture keypair must be completed with the original keys or explicitly marked
ephemeral before using the new format; the SDK cannot infer which was intended.
JSON encoding and decoding now validate credentials, so incomplete values that
previously serialized successfully return an error. Live runtime/session tokens
stay separate.

The expanded `Credentials` struct also requires keyed literals, such as
`Credentials{ID: id, Key: key, CaptureKeypair: pair}`. Existing positional
three-field literals no longer compile.
An adopted handle lacks capture keys; retain its original saved credentials.

`StartComputer` resolves the current binding revision once and refuses an existing
runtime until it is absent or released, waiting for an already accepted deletion
if needed. A conflict does not refresh that revision.
`RecoverComputer` is the explicit saved-state recovery choice for a recorded
failure: conditionally release the observed runtime, wait up to 90s for compute
release, then independently Start. It accepts possible unsaved-state loss and
refuses healthy running/paused runtimes and unresolved capture timeouts. A failed
or timed-out release allocates nothing. `Kill` remains unconditional destruction.
Start restores cookies, learned skills, compatible localStorage and already-durable
passkeys; replace stored session handles after it succeeds. Tabs, page memory,
session files and IndexedDB are outside this contract.

This SDK's retryable allocation and server-owned Stop require the updated backend
and a compatible Computer runtime; neither requires Pause opt-in. Existing
Computer create, capture/Finish and delete calls remain supported for applications
keeping their prior SDK. Complete the backend rollout before upgrading, including
the conditional-release support used by recovery. A pool image update does not
upgrade an already-running Computer: stop old-image bindings with the prior SDK
before upgrading their caller. Integrations using the removed generic
public sandbox operations must migrate to the Computer contract.

`Stop` and `Kill` keep the Computer's identity and saved state. `Computer.Delete`
(or `Client.DeleteComputer(ctx, id)`) ends both permanently: a live sandbox is
killed first, the id is refused for every later attach, saved state is purged
after 24 hours, and the id is never reusable. Deleting an unknown, already-deleted,
or other project's id also succeeds. There is no undelete.

## Timeouts

`AttachOptions.Timeout` sets the sandbox lifetime. `ReadyTimeout` separately
bounds allocation plus pod readiness; `BindReadyTimeout` bounds the following
bind. Runtime expiry starts at allocation, independently of those wait budgets.
The caller's context can shorten either wait. Defaults and cleanup
semantics are defined in the [attach timeout contract](https://pinesandbox.io/spec/computer).
For example, `Timeout: 8 * time.Hour` keeps a long-lived Computer while
`ReadyTimeout: 2 * time.Minute` limits waiting for capacity.

Ordinary requests use a 30s fallback when the caller's context has no deadline.
Failed attach cleanup uses its own 30s context so cancellation does not prevent
deleting a known allocation. This cleanup may extend the time until attach
returns; it is best-effort and does not recover allocations after a process crash.

## Stateless reuse (multi-instance / restarted backends)

The handle isn't the source of truth — your **persisted credentials** are. A
stateless backend rebuilds the handles per request, no provisioning and no coord
round-trip for the session:

- **Computer** — persist `comp.ID()`, `comp.Key()`, the current and retained
  capture keypairs, `comp.SandboxID()`, and
  `comp.ComputerToken()` (the `ct_`). Portal is an authorization ledger, not
  the source of truth for this integrator-owned record.
- **Session** — persist `sess.Name()` and `sess.Token()` (the `ps_`). The `ps_` is
  minted once at `CreateSession` and the coordinator redacts it on read, so a
  session re-fetched with `comp.Session(name)` has **no** `ps_` (its drive ops
  `401`); keep the create-time value and rebuild with `AdoptSession`.

```go
// Per request:
comp, err := client.AdoptExisting(ctx, id, key, sandboxID, ct,
    pine.AdoptOptions{BindingRevision: savedRevision}) // rebuild the Computer
if err != nil { /* … */ }
sess, err := comp.AdoptSession(name, ps)                        // rebuild the session
if err != nil { /* … */ }

task, err := sess.Agent().Status(ctx) // observe existing work; do not resubmit its goal
if err != nil { /* … */ }
_ = task
```

On a dropped stream, reconnect with the last successfully processed event ID;
persist it per feed and binding/session with your application's effects, or
deduplicate replays. A fresh backend needs the saved records, not the old SDK
objects. Do not replay exec or agent mutations solely because a response was
lost. Issue a fresh desktop delegation when reconnecting a viewer.

`comp.Status(ctx)` reads lifecycle state for the saved runtime without resuming
or replacing it. It returns a lower-case state such as `running`, `paused` or
`resuming`; lookup errors remain errors. A `*pine.NotFoundError` from this read
confirms runtime-record absence. Transport/authentication errors and `Alive`
returning false do not. Observe pending operations within the caller's context;
Resume and Start require the application's lifecycle intent. The
[integration guide](https://pinesandbox.io/docs/integration#reuse-a-running-computer-or-restore-a-stopped-one)
describes recovery decisions. Retried allocation requests reuse one create ID
without extending expiry. Failed attaches attempt cleanup; a process crash leaves
ordinary runtime expiry as the backstop. Long requested lifetimes permit
longer-lived abandoned allocations. Save returned credentials in your application
database; private keys cannot be recovered from an ID. Allocation does not
acknowledge that database write.

Persist the binding's ephemeral setting too. For an ephemeral Computer, pass
`pine.AdoptOptions{Ephemeral: true}` as the last `AdoptExisting` argument so
`Stop` continues to skip persistence. Omission means persistent.

Agent commands and event consumption are separate lifecycles. `Run`, `Steer`,
`Answer`, and `Cancel` submit input and return the current Task snapshot.
`Events` is the independently resumable, cumulative Task journal; persist its
event cursor wherever your application already stores session progress.

```go
// Before a new Run on a reused session, load its latest completed-turn cursor.
cursor := store.LoadSessionEventCursor(sessionID)
if _, err := sess.Agent().Run(ctx, goal, pine.RunOptions{}); err != nil {
	return err
}

var needsInput, completed bool
for event, err := range sess.Agent().Events(ctx, cursor) {
	if err != nil {
		return err
	}
	if err := store.ApplyAgentEventAndCursor(sessionID, event); err != nil {
		return err
	}
	needsInput, completed = event.Type == pine.EventNeedsInput, event.Terminal
	if needsInput || completed {
		break
	}
}
switch {
case needsInput:
	enqueueAnswerFlow(sessionID)
case completed:
	consumeCompletedTurn(sessionID)
default:
	requeueAgentEventWorker(sessionID)
}
```

`needs_input` pauses the work; `Answer` continues it. A background worker
can save the question and return. An interactive application can answer inline
and keep listening. Commit each event's effects and cursor together, and make
processing safe when an event is replayed.

For a reused session, the pre-submit cursor must have consumed the latest prior
completion boundary while the Task is idle—not merely be the last event a
lagging worker happened to process. Otherwise replay can surface an older
terminal before the newly accepted input. If the caller cannot establish that
invariant, it must retain its start-confirmation logic and ignore historical
terminals until the new submission is confirmed. A fresh session starts with an
empty cursor.

`Run` sends one request and does not retry an ambiguous transport failure.
The same operation works with older coordinators; there is no route probe.

If the process loses the final outcome, reconcile through `Status` and the
durable pre-submit `Events` cursor/baseline before deciding whether to submit
new work; blindly calling `Run` again is a distinct command. Without that
baseline, cumulative v1 history cannot prove that a repeated goal was accepted.
This v1 surface does not promise exactly-once submission across a process
restart.

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

An unavailable gateway route surfaces as
`*pine.SandboxGoneError`; `errors.As` still reaches its underlying `*pine.APIError`
for host, operation, request id, and wire detail. Reconcile it with `comp.Status(ctx)`:
paused/restoring runtimes withdraw their route without deleting the runtime record.
A missing session on a live
Computer instead matches `pine.ErrSessionNotFound`, and a control ownership
conflict matches `pine.ErrControlNotHeld`. Use `pine.IsRetryable(err)` for the
server's data-plane retry judgment; it intentionally does not guess whether an
opaque transport failure or mutation is safe to replay.

## Driving the Computer with an agent

A Computer is a real cloud browser + desktop + shell. There are **three ways** an
agent (or a human) can drive it — pick per task; they share one Computer/Session.

### 1. Delegate mode — Pine's resident agent drives

You hand a goal; Pine's in-Computer agent runs the perceive→act loop, pauses to ask
when it needs input, and reports a typed outcome. One persistent task per session;
each `Run` is a turn (the thread is the memory — `Reset` clears it).

```go
ag := sess.Agent()
cursor := store.LoadSessionEventCursor(sessionID) // latest terminal boundary; "" on a fresh session
if _, err := ag.Run(ctx, "Book the cheapest SFO→JFK flight next Friday", pine.RunOptions{}); err != nil {
	return err // errors.Is(err, pine.ErrSessionBusy) ⇒ a turn is already running
}
for ev, err := range ag.Events(ctx, cursor) {
	if err != nil {
		return err
	}
	if err := store.ApplyAgentEventAndCursor(sessionID, ev); err != nil {
		return err
	}
	switch ev.Type {
	case pine.EventNeedsInput: // the agent paused on a question
		if ask, ok := ev.Ask(); ok { // ask carries Question / Options / the ids
			if _, err := ag.AnswerAsk(ctx, ask, answer(ask.Question)); err != nil {
				return err
			}
		}
	case pine.EventResult:
		if !ev.Terminal {
			continue
		}
		res, err := ag.Result(ctx) // TerminalReason, Summary, Usage, Artifacts; Author on skill-author tasks
		if err != nil {
			return err
		}
		if res.TerminalReason == pine.TerminalCompleted {
			log.Printf("done: %s", res.Summary)
		}
	}
	if ev.Terminal {
		break
	}
}
```

`AgentMode.Cancel` targets whichever turn is current. The coordinator captures
and fences that executor internally, so callers do not carry a run identifier.

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
	// params are action-specific, e.g. {"coordinate": [480, 220]} or {"text": "hello"}.
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
and the web SDK (`@pinesandbox/computer-web`) renders the live desktop for a human to
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
is the published [API reference](https://pinesandbox.io/spec/computer). The
noun/verb set matches the Ruby `pinesandbox` gem (same verbs, presented
idiomatically per language), with Go-idiomatic
conveniences on top: typed constants (`pine.EventNeedsInput`, `Controller*`, …),
option funcs (`TakeControl(ctx, WithForce())`), `AnswerAsk`, and computer-use
helpers (`Click`/`TypeText`/`Scroll`/…). Errors are a flat, `errors.As`-able
vocabulary (`errors.go`).

Tokens are never method parameters — the SDK holds the credential ladder (pk_ →
project JWS → ct_ → ps_) and attaches the right one per call. To hand a browser
the desktop, `session.Delegate(ctx)` mints a short-lived view-only `dt_` envelope
carrying nothing privileged.

## Troubleshooting — reporting an issue

The unreleased SDK adds a saved Computer diagnostic. It requires the diagnostics backend; older deployments do not serve it. No live Computer handle or private keys are needed:

```go
report, err := client.CreateDiagnostic(ctx, pinesandbox.DiagnosticOptions{
    ComputerID: savedID,
    Session: "main", // omit for a lifecycle check only
    IdempotencyKey: diagnosticRequestKey, // preserve across retries of this request
})
if err != nil { return err }
supportReference := report.ID
// Read later. Only poll queued/running reports, with a bounded deadline.
report, err = client.GetDiagnostic(ctx, supportReference)
if err != nil { return err }
if report.Assessment != nil { log.Print(report.Assessment.Message) }
```

Supply a session name to investigate its recorded agent work; omit it for a lifecycle check. The investigation does not operate the application or repeat its work. It produces a private report for Pine support and a separate customer-safe assessment. Optional descriptions provide private context. An unavailable diagnostic does not justify repeating customer work. Requesting a report does not open a human support ticket; share its reference separately when asking Pine for help.

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
binding revision before coordinator bind failed. retain its non-nil `Credentials` and inspect `SandboxID` before deciding whether
to stop or recover the failed runtime;
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
