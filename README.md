# Pine Computer Go SDK

Pine's server-side Go SDK for the **Computer** — create, start, and drive a Pine
Computer (sessions, Pine's agent, your own agent, shell, files) from a backend.

```
go get go.pinesandbox.io/computer
```

The import path is a stable public vanity path. Pin the current `v0.3.x`
compatibility line.

The vanity module and its backing mirror are public. No `GOPRIVATE` setting,
GitHub account, or package token is required.

## Create a Computer

A **Computer** is the identity and saved browser state you keep. A **runtime** is
one running allocation of it. A **session** is work inside that runtime.

Import `go.pinesandbox.io/computer` as `pine`. In these examples, `store` is your
application's encrypted database, not an SDK object. Its save methods serialize
with `json.Marshal` and return an error if storage fails. These are steps inside
your handler or worker, using its `ctx`:

```go
client, err := pine.NewClient(pine.ClientOptions{
    Endpoint: os.Getenv("PINE_SANDBOX_ENDPOINT"),
    APIKey: os.Getenv("PINE_SANDBOX_API_KEY"),
})
if err != nil { return err }

creds, err := pine.GenerateCredentials()
if err != nil { return err }
if err := store.SaveCredentials(creds.ID, creds); err != nil { return err }

comp, err := client.CreateComputer(ctx, pine.AttachOptions{Credentials: creds})
if err != nil { return err }
binding, err := comp.Binding()
if err != nil { return err }
if err := store.SaveBinding(creds.ID, binding); err != nil { return err }

sess, err := comp.CreateSession(ctx, pine.CreateSessionOptions{Browser: true})
if err != nil { return err }
fmt.Println(sess.Name())
// Use sess to drive the browser or run Pine's agent; examples below.
```

| Save | When | Used for |
|---|---|---|
| `Credentials` | Before Create; again if you change keys | Starting this Computer again and decrypting saved state |
| `Binding` | After each successful Create or Start | Reconnecting to that exact runtime from any backend |

Both JSON records contain secrets. Keep them encrypted on your backend, never in
a browser. Your project API key also stays backend-only.

## Reuse it from another backend

```go
var creds pine.Credentials
var binding pine.Binding
if err := json.Unmarshal(savedCredentials, &creds); err != nil { return err }
if err := json.Unmarshal(savedBinding, &binding); err != nil { return err }
comp, err := client.ReconnectComputer(ctx, creds, binding)
if err != nil { return err }
state, err := comp.Status(ctx) // e.g. "running", "paused", "resuming"
if err != nil { return err }
fmt.Println(state)
```

Reconnect reconstructs a handle locally. It makes no request, creates nothing,
and does not replay work. Its next call targets the saved runtime, even if another
backend has replaced it. Store the binding revision alongside its JSON in your
existing Computer record. Save a replacement only if its revision is newer (or it
is the identical binding). After confirmed Stop, clear it only if the stored
revision and runtime still match the handle stopped. Keep that revision and the
durable credentials, so delayed replies cannot revive a stale binding. Session
records belong to a binding revision and cannot be reused after Start.

Also save each session's name and token when it is created. Reconstruct it with
`comp.AdoptSession(savedName, savedToken)`; session lookup redacts that token.
Save agent turn IDs and event cursors with the session. See
[Continuing session work](#continuing-session-work) to wait on an existing turn.

## Stop, Start, Pause or Resume

| Intent | Call | Result |
|---|---|---|
| Finish using compute | `comp.Stop(ctx)` | Attempts a final save, releases the runtime and ends its sessions |
| Start a stopped Computer | `client.StartComputer(ctx, creds, opts)` | Restores saved state into a new runtime; create new sessions |
| Keep live memory for later | `comp.Pause(ctx)` | Retains the runtime and sessions; requires project opt-in |
| Continue paused work | `comp.Resume(ctx)` | Restores the retained runtime and sessions |
| Observe after a connection failure | `comp.Status(ctx)` | Reads state without changing or renewing it |

```go
stopped, err := comp.Stop(ctx)
if err != nil { return err }
if !stopped { return fmt.Errorf("Stop is unconfirmed") }

// Later, at a safe boundary chosen by your application:
comp, err = client.StartComputer(ctx, creds, pine.StartOptions{})
if err != nil { return err }
binding, err = comp.Binding()
if err != nil { return err }
if err := store.SaveBinding(creds.ID, binding); err != nil { return err }
```

Stop waits up to 390 seconds, or the caller's earlier deadline. Cancellation ends
the wait, not accepted work. Keep the binding until release is confirmed. A true
result proves release or authoritative absence, not a successful final save.
Saving is best effort: a failed or unconfirmed save still releases the runtime,
and the next Start restores the last saved state. Only older servers retain the
runtime and return `StopSaveError` when the final save fails or is unconfirmed.

Stop/Start is also how an application replaces an old runtime. An SDK upgrade or
Pine image rollout does not replace a running Computer. Start can receive a
compatible older warm revision during rollout; it does not guarantee a build.
`AttachComputer` remains the field-based Start API. Use Reconnect to reuse a runtime.

Pause requires project opt-in and a snapshot-capable runtime. Pause/Resume wait up
to six minutes or the caller's earlier deadline. A timeout does not cancel work;
inspect or retry the same operation. A failed Resume never silently Starts a
replacement. Stop can restore a paused runtime before saving it.

Stop/Start restores supported saved browser state, not live tabs, page memory or
session files. Export files first. `Kill` releases compute without an acknowledged
save. `Delete` permanently retires the Computer ID and purges its saved state
after 24 hours; there is no undelete. Stop and Kill retain the identity and saved state.

## Idle timeout and maximum age

`AttachOptions.Timeout` is the inactivity window (default five minutes), not an
HTTP request timeout. Work renews it; observation does not. It starts at allocation,
so a one-minute window can expire during slow setup.

Projects can opt into a lifetime policy. The standard allowance is one minute to
24 hours of idle time and seven days of maximum runtime age; approved project
limits can be higher. Existing allocations retain their admitted terms.
Enrollment applies to new allocations from Ruby 0.3.13 / Go 0.3.15 and later,
including integrations that do not use the new policy methods. A requested
timeout above the allowance, including the SDK default, is rejected. Before
enrolling a project, check its integrations' requested timeouts and handling of
maximum age. Deployment alone does not enroll projects or alter existing runtimes.

```go
details, err := comp.RuntimeDetails(ctx)
if err != nil { return err }
if details.Lifecycle != nil {
    updated, err := comp.SetIdleTimeout(ctx, time.Hour)
    if err != nil { return err }
    fmt.Println(updated.Lifecycle.IdleTimeoutSeconds, updated.ExpiresAt)
}
```

For an enrolled runtime, changing idle timeout takes effect now while running,
or on the next Resume while paused. It cannot extend maximum age or snapshot
retention. Legacy runtimes keep their previous expiry clock while paused; enrolled
runtimes use snapshot retention and maximum age. An identical value does nothing.
After a conflict or lost response,
read RuntimeDetails before deciding whether to edit again.

Maximum age includes paused time. Graceful Stop begins twelve minutes before the
hard deadline; expiry is not proof of a final save. Plan Stop/Start at a safe
point before that deadline when you need uninterrupted work.

`RuntimeDetails` describes this allocation, including optional `Lifecycle` terms
and its opaque `RuntimeRevision`. Nil lifecycle means no admitted project lifetime
policy. `client.ProjectPolicy(ctx)` optionally reads allowances for future
allocations; ordinary operations do not require that call. Neither read renews the
runtime. `Retryable` is a pointer: nil means no verdict, not permission to retry.

## Handle failures without replacing live work

- A connection error or `Alive` returning false does not prove the runtime is
  gone. Read `Status`; its `NotFoundError` confirms runtime-record absence.
  A data-plane `SandboxGoneError` alone does not: paused/restoring Computers can
  temporarily have no route.
- `RuntimeRetainedError` names the existing `RuntimeID` and `State` when Start
  cannot replace it. Match the ID to your saved binding, or reload the latest
  binding from your database. Reconnect, Resume or finish Stop as appropriate;
  an ID alone cannot reconstruct a missing token.
- A Stop timeout is unconfirmed, not canceled. Keep the binding and observe or
  retry Stop. Older servers return `StopSaveError` and retain the source after a
  failed save; current servers still release the runtime.
- Serialize Create/Start per Computer. On a binding-revision conflict, reload the
  winner's saved binding instead of retrying with a higher revision.
- If a registered Computer's Start reply is lost before saving its binding,
  serialize recovery, call `client.StopComputer(ctx, id)`, then Start separately
  with saved credentials. This releases the runtime whose token was lost.
- An interrupted first Create can fail before registering the Computer. Stop-by-ID
  then returns success, but Start raises `UnknownComputerError`. For that initial
  creation, retry Create with your saved credentials after confirming the project
  and ID are correct and the identity was not deleted. Do not treat every unknown
  ID as permission to create. An allocation abandoned before registration may
  remain until expiry; Stop-by-ID cannot discover it.
- On `AttachAuthorizationCommittedError`, registration has succeeded. Retain its
  credentials when provided and reconcile the registered Computer before Start.
  Private keys cannot be recovered from an ID.

`RecoverComputer(ctx, creds, opts)` is an explicit choice for a recorded failed
runtime: it releases that observed runtime, then Starts from saved state. It
accepts possible unsaved-state loss and refuses healthy running/paused runtimes.
A release timeout allocates no replacement. It never follows a concurrent newer binding.

Allocation retries within one call reuse its create ID. A fresh call is a new
attempt. Failed attach attempts clean up known allocations best-effort; process
crashes rely on expiry. Longer requested timeouts can leave abandoned compute
running longer.

## Timeouts

`AttachOptions.Timeout` sets the inactivity window. `ReadyTimeout` separately
bounds allocation and runtime readiness; `BindReadyTimeout` bounds the following
attach. Runtime expiry starts at allocation, independently of those wait budgets.
The caller's context can shorten either wait. Defaults and cleanup
semantics are defined in the [attach timeout contract](https://pinesandbox.io/spec/computer).
For example, `Timeout: 8 * time.Hour` keeps a long-lived Computer while
`ReadyTimeout: 2 * time.Minute` limits waiting for capacity.

Ordinary requests use a 30s fallback when the caller's context has no deadline.
Failed attach cleanup uses its own 30s context so cancellation does not prevent
deleting a known allocation. This cleanup may extend the time until attach
returns; it is best-effort and does not recover allocations after a process crash.

## Continuing session work

Save the session name/token with its runtime binding. Any backend can load the two
Computer records, call `ReconnectComputer`, then `AdoptSession`. A handle is not
a source of truth; the records in your database let another process continue.

Agent work is addressed by turn. A turn starts when a run or an answer is
accepted and ends when the agent finishes, fails, is canceled or asks a
question. Every ending is the turn's `Result`, with its usage; a question is
the result's summary (`turn.Question()`), and the answer starts the next turn,
which continues the same goal. Save the turn ID `Run` and `Answer` return in
the record you already keep for the request and wait for that turn. The SDK
does not require the submitting process to stay alive. Any process with the saved
session and turn ID can continue:

```go
// Submit: your saved message ID makes a crashed submitter's retry return the same turn.
turn, err := sess.Agent().Run(ctx, goal, pine.RunOptions{IdempotencyKey: msg.ID})
if err != nil {
	return err
}
store.SaveTurn(msg.ID, turn.TurnID)

// Any worker, any process: wait for the saved turn.
turn, err = sess.Agent().Wait(ctx, msg.TurnID, 30*time.Second)
if err != nil {
	return err
}
switch turn.State {
case pine.TurnRunning, pine.TurnCanceling:
	requeueWait(msg.ID) // the wait expired; the turn is still working
case pine.TurnNeedsInput:
	store.SaveQuestion(msg.ID, turn.TurnID, turn.Question()) // dedupe by turn ID
case pine.TurnAnswered:
	store.SaveTurn(msg.ID, turn.NextTurnID) // already answered elsewhere
default:
	store.SaveResult(msg.ID, turn.State, turn.Result) // summary, files and usage
}

// Answer with the turn ID stored with the question, never the session's latest turn.
next, err := sess.Agent().Answer(ctx, question.TurnID, reply, pine.AnswerOptions{})
if err != nil {
	return err
}
store.SaveTurn(msg.ID, next.TurnID)
```

`Wait` returns once the turn ends, or after the timeout with the turn still
running; it never cancels the turn or follows its successor. A read whose
response was lost, or whose problem is marked retryable, is read again within
the timeout. Only the session's latest turn can be answered, so starting new
work leaves an open question unanswered. Repeating the same answer returns the
same next turn, and a different answer to an answered turn is a
`/errors/stale-turn` conflict. So `Answer` and `Cancel` resend a lost response
themselves for up to about 30 seconds, honoring `Retry-After`: a network fault,
a `408`, or a gateway `502`, `503` or `504`, which cannot say whether the
Computer acted. `Run` resends only with an idempotency key (it uses one of its
own when you pass none). If no response arrives, or `ctx` ends first, `Run`
returns a `*pine.RunUnconfirmedError` naming the key: save it and send the run
again with it. Pass a key of your own, such as your
message ID, so a fresh process can do the same.
`Steer` is never resent: a conflict at a completion boundary does not prove the
text was not delivered. `Events` is optional progress — the
resumable activity journal for showing steps and screens — not how you learn
that a turn ended.

Tokens split by tier: **`ct_` is your backend's Computer-wide token** — control,
agent + skills-authoring mutations, lifecycle; **`ps_` is the session's own drive + reads**.
The SDK attaches the right one per call.

Skill authoring is asynchronous: `Session.Learn`, `Session.Teach`, and
`Session.Refine` return the accepted author run; consume progress and its terminal
draft through `Session.AuthorEvents`.

When the Computer rejects a token that worked before, the SDK surfaces a typed
`*pine.TokenRejectedError` — a report, not an attach instruction (`ct_` lasts as long
as the binding, so on a live runtime this is `binding_auth_lost`). It never silently
re-mints (that would land a fresh runtime and invalidate every `ps_`). Reconcile, and
read `Status` before deciding on recovery. Start requires release or authoritative
absence; it creates a new binding and new sessions. Persist that binding and
reconcile unfinished work instead of replaying mutations automatically.

A Computer that can no longer be reached surfaces as
`*pine.SandboxGoneError`; `errors.As` still reaches its underlying `*pine.APIError`
for host, operation, request id, and wire detail. Reconcile it with `comp.Status(ctx)`:
paused or restoring runtimes can't be reached, but their runtime record remains.
A missing session on a live
Computer instead matches `pine.ErrSessionNotFound`, and a control ownership
conflict matches `pine.ErrControlNotHeld`. Use `pine.IsRetryable(err)` for the
server's retry judgment; it intentionally does not guess whether an
opaque transport failure or mutation is safe to replay.

## Driving the Computer with an agent

A Computer is a real cloud browser + desktop + shell. There are **three ways** an
agent (or a human) can drive it — pick per task; they share one Computer/Session.

### 1. Delegate mode — Pine's agent drives

You hand a goal; Pine's agent runs the perceive→act loop on the Computer and ends the
turn with a typed outcome, or with a question when it needs input. One persistent task
per session; each `Run` or answer is a turn (the thread is the memory — `Reset` clears it).

```go
ag := sess.Agent()
turn, err := ag.Run(ctx, "Book the cheapest SFO→JFK flight next Friday", pine.RunOptions{})
if err != nil {
	return err // errors.Is(err, pine.ErrSessionBusy) ⇒ a turn is already running
}
store.SaveTurn(sessionID, turn.TurnID) // wait for, answer or cancel this turn by its ID
// Show progress while waiting: every event names its turn.
savedTaskID, cursor := store.LoadAgentCursor(sessionID) // persist the Task and cursor together
task, err := ag.Status(ctx, pine.TaskOptions{})
if err != nil {
	return err
}
if savedTaskID != task.TaskID { cursor = "" }
for ev, err := range ag.Events(ctx, cursor, pine.EventsOptions{ExpectedTaskID: task.TaskID}) {
	if err != nil {
		return err
	}
	if err := store.ApplyAgentEventAndCursor(sessionID, ev); err != nil {
		return err
	}
	switch ev.Type {
	case pine.EventProgress: // the agent explains what it is doing
		if p, ok := ev.Progress(); ok {
			ui.Narrate(ev.TurnID, p.Text)
		}
	case pine.EventActivity: // one row per step, upserted by ID
		if a, ok := ev.Activity(); ok {
			// Title is the Computer's own description, such as "Click “Log in”";
			// Kind and Facts add detail. Every step ends with one outcome,
			// interrupted included.
			ui.UpsertStep(ev.TurnID, a.ID, a.State, a.Title)
		}
	case pine.EventResult:
		// The turn ended, possibly with a question: read it with Wait and act on its state.
		ended, err := ag.Wait(ctx, ev.TurnID, 0)
		if err != nil {
			return err
		}
		ui.ShowTurn(ended)
		return nil // an answer opens a new turn: follow that one the same way
	}
}
```

A captured screen carries its matching step when known: `frame.Activity`
is a typed activity snapshot, so a frame read from partial history still has
its action title and outcome. Attach it by activity ID without counting another
outcome or changing completion time. An absent activity means an independent
observation; do not guess from nearby events. A text-only update leaves the last
captured screen and its original attribution visible. Keep historical selection
separate from following new frames; the event ID identifies each image for replay.

`ag.Cancel(ctx, turnID)` cancels the work while that turn is current and returns
it, `canceling` until the agent stops. A waiting question is closed: its turn ends
`canceled` and can no longer be answered. An answered turn's continuation is a
different turn, so canceling the answered one is a `409` (`/errors/stale-turn`);
any other ended turn comes back unchanged. Save each event cursor with its task
ID and resume through
`Events(ctx, cursor, pine.EventsOptions{ExpectedTaskID: savedTaskID})`;
a stale-task error requires a fresh history context rather than reusing the cursor.

Before mounting an agent UI, `comp.RequireAgentProtocol(ctx)` checks that the
current binding serves the public agent protocol. Any agent or authoring call
that reaches a Computer without it returns the same local
`*pine.AgentProtocolUnsupportedError`, which is not an HTTP error: Stop, then
Start the Computer to update it. Authorization, gone-Computer and network errors
keep their original types.

`Status` returns the session's Task and its latest turn, for discovering work;
`Turns` lists the retained turns. Use `pine.TaskOptions{ExpectedTaskID:
task.TaskID}` for `Status`, `Turns` and `Reset`; reset requires the ID. Keep the
Task ID with saved event cursors.

### 2. Your own agent drives

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
`Session.Drive()` (your own agent's primitives). The package godoc is the authoritative
surface reference (`go doc go.pinesandbox.io/computer`), and the wire contract
is the published [API reference](https://pinesandbox.io/spec/computer). The
noun/verb set matches the Ruby `pinesandbox` gem (same verbs, presented
idiomatically per language), with Go-idiomatic
conveniences on top: typed constants (`pine.EventActivity`, `Controller*`, …),
option funcs (`TakeControl(ctx, WithForce())`), turn states (`pine.TurnNeedsInput`, …), and computer-use
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

Errors are **self-describing at the resource level**: every `*APIError` from a Computer
response folds the failing Computer + operation into `Error()` — resource first — so a generic
handler that logs only `err` shows WHICH Computer and WHICH call failed with no extra plumbing.
`Host` / `Op` / `RequestID` are also exported fields (the transport-fault `*TimeoutError` /
`*ConnectionError` and the control-plane / token errors carry the same context). The suffix
grammar is uniform `key=value` across the Go and Ruby SDKs; the Go SDK renders **`host=`**
because its client holds the Computer's host, where the Ruby SDK uses **`sandbox=`** because
it holds the sandbox id (same Computer, different handle each SDK already has). `op` is always `<METHOD> <path>` with the query string stripped.
Portal token/attach errors additionally expose `Code` (the stable RFC 9457 problem type)
and optional `Reason`; both are spec-defined and safe to branch on. Raw server exceptions
are never part of those fields.

If `CreateComputer` returns `*AttachAuthorizationCommittedError`, Portal advanced the
binding revision before the attach failed. Retain its non-nil `Credentials` and inspect `SandboxID` before deciding whether
to stop or recover the failed runtime;
their diagnostic formatting redacts both private keys.

```go
if _, err := sess.Agent().Run(ctx, "book the 9am slot", pinesandbox.RunOptions{}); err != nil {
    log.Printf("agent run failed: %v", err)
    // => "… (host=sbx_abc123.computer.<zone>, op=POST /v2/sessions/main/agent/run, request_id=req_abc123)"
    var apiErr *pinesandbox.APIError
    if errors.As(err, &apiErr) {
        report(computer.ID(), sess.Name(), time.Now().UTC(), apiErr.Host, apiErr.Op, apiErr.RequestID)
    }
}
```

When you file a report, **lead with the resource spine** — the durable key Pine uses to find
your Computer's records:

1. **`computer_id`** (`computer.ID()`) — WHICH Computer. Add the **session name** (`sess.Name()`)
   and, for an agent-lane issue, the **`task_id`**.
2. A **UTC time window**.
3. **`request_id`** *when you have it* — the precision handle for a single failed call
   (`apiErr.RequestID`, also in the message). A read or stream that fails *without* a response —
   a `*pinesandbox.TimeoutError` / `*ConnectionError`, or `ErrStreamLost` after a feed can't
   reconnect — carries no fresh id (a mid-stream `ErrStreamLost` does fold in the last live
   stream's `request_id`); the resource spine above is enough. Every one of these still names the
   failing Computer (`host=…`) + operation (`op=…`) in its message.

## Stored credentials and older integrations

`comp.Credentials()` returns an independent copy, including keys retained for
older saved state. Save credentials before Create and again only after changing
keys. Credentials and bindings use versioned JSON shared with Ruby. Normal
formatting redacts secrets; explicit JSON contains them and needs encrypted storage.

For integrations using older storage or field-based methods:

- `AdoptExisting` remains supported but does not recover capture keys. Retain the
  original complete credentials; prefer Reconnect when both saved records exist.
- Upgrade all readers before writing the shared credentials format: older Go
  readers can omit capture keys. Incomplete records must be completed with the
  original keys or explicitly marked ephemeral; JSON validation rejects ambiguity.
- Use keyed `Credentials` literals. Positional literals from older struct layouts
  may no longer compile.
- Ordinary Computer methods are the integration surface. Control-plane adapters
  and sandbox handles are SDK internals.


## Layout

- root package `pinesandbox` — the hand-written facade.
- `internal/base/*` — generic, Computer-agnostic primitives (zone, transport,
  SSE, problem, spec-version); enforced to never import a domain package.
- `internal/{bindhpke,bind,binder,tokens,controlplane,coordinator}` — the domain.
- `interop/` — monorepo-only module proving the attach HPKE envelope is byte-
  compatible with the Computer's implementation.

## Develop

> Contributor section — these paths are the Pine **monorepo** layout
> (`sdks/pine-computer/go/`), not the published module. The standalone published
> tree builds with `go test ./...` alone; the conformance/interop gates below
> need the monorepo and are skipped when the module is fetched on its own.

```
go test -race ./...      # unit + conformance gates (route + schema drift)
go vet ./... && gofmt -l .
cd interop && go test ./...   # SDK ⇄ Computer HPKE interop (needs the monorepo)
```

The route/schema conformance gates read the `contract/computer-*.json` artifacts
beside the SDK in the monorepo, regenerated from the OpenAPI specs by
`specs/gen-computer-{routes,schemas}.py` and kept fresh by CI (single-source each
drift axis; machine-check it).
