# Changelog

All notable changes to the Pine Computer Go SDK (`go.pinesandbox.io/computer`,
package `pinesandbox`) are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/). The current beta compatibility
line is `v0.3.x`; review release notes before adopting a new minor version.

## [0.3.17] — 2026-10-17

### Changed

- Agent work is addressed by turn. A turn starts when a run or an answer is
  accepted and ends when the agent finishes, fails, is canceled or asks a
  question. Every ending is the turn's `Result`, with that turn's usage
  (`AgentResult.Usage`); a question is a `needs_input` result whose summary is
  the question, read with `AgentTurn.Question()`. `Run` and `Answer` return the
  `AgentTurn` to save. `Wait(ctx, turnID, timeout)` holds while that turn runs
  and returns it once it ends, or at the timeout with the turn still running;
  it never cancels the turn or follows its successor, and it reads again after
  a lost response or a problem marked retryable. `Answer`, `Steer` and
  `Cancel` take the turn ID. `Answer` starts the turn that continues the same
  goal, and an answered turn names it in `NextTurnID`, so a worker reading an
  old turn never re-presents an answered question. Only the latest turn can be
  answered. `Turns` lists retained turns; `Status` returns the Task with its
  latest turn for discovery. Turn states are named `TurnRunning` through
  `TurnCanceled`; `TerminalNeedsInput` names the question's terminal reason.
- `RunOptions.IdempotencyKey` names a submission: a run repeated with the same
  key returns the turn it admitted. Without one, `Run` uses its own key so its
  retry after a lost response is safe; a fresh process must pass the key it
  saved. Repeating the same answer returns the same next turn. `Cancel` stops a
  running turn and closes a waiting question, which ends its turn canceled;
  canceling an answered turn is a 409 `/errors/stale-turn`, and canceling any
  other ended turn returns it unchanged. So `Answer` and `Cancel` resend a lost
  response for up to about 30 seconds, honoring Retry-After (a network fault, a
  408, or a gateway 502, 503 or 504); `Steer` never does. When no response
  arrives, or the context ends first, `Run` returns a `*RunUnconfirmedError`
  naming the key to send it again with; `APIError.RetryAfter` reads the
  response's Retry-After.
- Removed with the old addressing: `Result` (read the turn's `Result`),
  `AnswerWithOptions`, `AnswerAsk`, `AnswerAskWithOptions`, the
  `ExpectedTurnID` answer and steer options (`Answer` and `Steer` take the turn
  ID and return the `AgentTurn`, as `Cancel` does), the `AgentTask` fields
  `State`, `Reason`, `Goal`, `Context`, `Constraints` and `Usage` (read its
  `Turn`), `CancelOptions`, `ExecutionID`, `CurrentTurnID`, `TurnAttempt`,
  question request IDs, `AgentEvent.Terminal`, `AgentEvent.Ask` and `AgentAsk`
  (the public stream sends no `needs_input` event), `AgentResult.Status`, and
  `AgentDuration.ActiveMs` (no turn includes time waiting for an answer). Every
  event carries its `TurnID`. The run-total `usage.finalized` event is no
  longer sent.
- Removed the native event-kind constants `EventReasoning`, `EventCommand`,
  `EventStep`, `EventNeedsInput`, `EventUsage`, `EventScreenshot` and
  `EventUsageFinalized`: the public stream never sends those kinds.
- Agent and authoring calls require a Computer that serves the public agent
  protocol. `RequireAgentProtocol` checks the current binding before you build
  an agent UI; any other agent or authoring call that reaches a Computer
  without it returns the same `*AgentProtocolUnsupportedError`, a local error
  with no HTTP status, without retrying the request: Stop, then Start the
  Computer to update it. Resource, authorization and gone-Computer errors keep
  their types.
- Move public skill-author admission and cancellation to the V2 agent family.
  Existing session, file and control APIs retain their contracts.
- Memory reset requires the observed Task ID and rejects a stale Task without
  changing the replacement session. `Status`, `Turns` and `Reset` take one
  `TaskOptions` argument.
- Agent operations and authoring streams use the public V2 agent contract.
  It requires a runtime with V2 support; there is no fallback to the native
  trace. Narration arrives as `progress`. `activity` reports each step of the
  agent's work with exactly one outcome (`interrupted` when its turn stopped
  first), a `kind` (browser, command, file, skill or tool), an imperative
  `title` ("Open Home Page – E-Label", "Click “Log in”", "Edit notes.md", "Run
  python3 build_report.py", "Use skill book-flight" for a skill of yours) and
  typed `facts`: the page a browser step opened, the element it acted on, the
  work-directory files it touched, the skill it used and a browser step's
  actions. A command step shows the command it ran: a credential delivered to
  the session reads as its `$NAME`, the session's directories read relative to
  its working directory, and Pine's own directories read as `[Pine]`. Activity
  never carries Pine's tool names or tool-call arguments, command output or
  anything typed into a page. Upsert
  activity rows by `activity_id` and display a title as is.
  The screen arrives as `frame` events (WebP). Command output, reasoning, the
  trace's PNG screenshots and provider metadata are omitted. Ordinary V1 agent
  calls are denied on the new runtime, so upgrade consumers before switching
  pools.
- Native task and event fields (`ThreadID`, `Source`, `Visibility`,
  `TaskState`, `Reason`) are removed. `RunOptions.Context` is a string and
  `Constraints` accepts `*RunConstraints` with `MaxTokens` and `MaxWallMs`
  budgets.
- Creating or starting a Computer that Pine cannot start yet (HTTP 503
  `COMPUTER_CREATE_BUSY`), for example during a burst of the project's
  starts, no longer fails at once. The SDK waits the delay Pine asks for and
  sends the same request again, up to 10 times and never past your context's
  deadline. If Pine is still busy, it returns the error as
  before: a `ServerError` with code `COMPUTER_CREATE_BUSY` and its
  `RetryAfter`.

### Added

- Add `Computer.SetIdleTimeout(ctx, time.Duration)` for enrolled runtimes and expose admitted
  lifecycle limits, scheduled Stop, hard deadline and actual runtime revision.
  Optional project-policy discovery reads the allowance before allocation.
  A legacy runtime is refused before a mutation; conflicts are not replayed.

- `Computer.Binding` and `Client.ReconnectComputer` export/import a versioned live-runtime record alongside durable credentials, preserving retained keys without allocation or network I/O. `RuntimeDetails` exposes the existing lifecycle status metadata without changing `Status`. Start conflicts now expose `RuntimeRetainedError` with the retained runtime ID and state.

- `AgentEvent.Progress`, `AgentEvent.Input`, `AgentEvent.Activity` and
  `AgentEvent.Result` decode the public agent event kinds, alongside
  `ControlChange`. `AgentActivity` carries the step's ID, state, `Kind`,
  optional title and `Facts` (`ActivityFacts`); a malformed title is dropped
  and unknown states kept. New constants name the public event kinds
  (`EventProgress`, `EventActivity`, `EventControllerChanged`) and activity
  states.
- `AgentEvent.Frame` decodes a `frame` event, a picture of the Computer's
  screen taken after a step that may have changed it, into `AgentFrame`
  (media type, size and the decoded image). `AgentMode.Events` with
  `EventsOptions{SkipFrames: true}` reads the stream without frames.
  `EventFrame` names the kind.
- `AgentFrame.Activity` carries the matching step snapshot when attribution is
  available, including in partial history; `ObservedAt` carries capture time.
  Neither creates another step outcome. Frames without activity are independent
  observations; never infer association from event order.
- `ErrCreateRateLimited` matches a create or start refused because the project
  has started as many new Computers in the last minute as it may. Nothing was
  created, and the SDK doesn't retry it. `errors.As` reaches
  `CreateRateLimitedError`, whose `RetryAfter` says how long to wait before
  starting another Computer. `ErrCapacityExceeded` still means the project is
  at its concurrent Computer limit; neither matches the other.
- Runtime lifecycle errors (`ControlPlaneError`, `CapacityExceededError`,
  `ServerError` and the rest) carry the server's `Retry-After` as
  `RetryAfter`, zero when the server sent none.

### Fixed

- Correct event-stream examples: an empty cursor replays retained history,
  and a pending question does not close the stream.
- Treat event-history expiry and journal storage exhaustion as nonretryable errors in the fallback error taxonomy.

## [0.3.16] — 2026-10-14

### Changed

- Waiting for a Computer to start, pause, resume, stop or be deleted returns
  as soon as its state changes instead of polling, still honoring your
  context's cancellation and deadline. A connection dropped during the wait
  reconnects within that deadline. Where Pine doesn't support this yet, the SDK
  polls as before. Saved credentials keep working.
- Error messages and package documentation describe the Computer and its
  runtime. The messages of `SandboxFailedError`, `ReadyTimeoutError`,
  `AttachAuthorizationCommittedError`, `PodPoisonedError`,
  `BrokerUnreachableError` and `SpecVersionMismatch` are reworded; their types,
  fields and `errors.Is`/`errors.As` behavior are unchanged.

### Removed

- `Computer.Health`, which read a route outside the public API.

## [0.3.15] — 2026-10-11

### Added

- `Client.CreateDiagnostic` and `Client.GetDiagnostic` request and read a saved diagnostic of a Computer, with a support reference, using the project key and Computer ID. Naming a session also has Pine's agent review that session's recorded work, without changing anything; otherwise the diagnostic checks the Computer's lifecycle status. A diagnostic never repeats your work or opens a support ticket.

- `Computer.Pause` and `Computer.Resume` keep a Computer's sessions and their
  state while it isn't working, for projects with Pause enabled. Both wait for
  the operation to finish, within a bounded time. A Computer that can't be
  paused says so, and a failed resume never starts a fresh Computer in its
  place.
- `Client.StartComputer` starts a stopped Computer from its saved state.
  It accepts saved `Credentials` plus runtime options. The released
  `AttachComputer(ctx, id, key, opts)` form remains supported.
- `Client.StopComputer(ctx, id)` stops a Computer by id, for cleanup when a
  start's response was lost and you hold no session credentials.
- `Client.RecoverComputer` replaces a Computer whose runtime failed: it releases
  the failed runtime and starts again from the last saved state. It never
  stands in for `Resume`.
- `Computer.Status(ctx)` reads a saved Computer's state (running, paused,
  recovering or stopped) without changing it. Lookup errors stay distinct from
  those states and never trigger a replacement.

- `Credentials` supports JSON storage shared with Ruby, including retained capture
  and state keys and ephemeral mode. `Computer.Credentials()` returns a copy.
  Save before Create and after changing keys; Start reads the binding revision.
  Complete persistent records from the released Go JSON format remain readable.
  New writes use the shared schema: upgrade all readers first, because old Go
  readers silently omit the capture keypair. JSON encoding and decoding now
  reject incomplete credentials, including legacy records missing that keypair.
  Existing keyed struct literals remain supported; unkeyed literals must name
  their fields when using the expanded struct.

### Changed

- Starting a Computer never replaces one that is running or paused. Reconnect
  with `AdoptExisting`, stop it first, or use `RecoverComputer` after a recorded
  failure.
- Creating and starting a Computer are safe to retry: a retried request never
  creates a second Computer or changes its identity, and the call still returns
  only once the Computer is ready and its state restored.
- A stop already under way can be completed from another backend or a fresh
  SDK instance. No saved binding revision is required.
- `ListSkills` lists only the Pine skills your agent can be offered. Pine
  skills carry `switchable` in place of `class`, and `disabled_reason` is gone;
  `enabled` still says whether the next agent is offered the skill.
- `Capabilities` lists only the product features this Computer can use, each
  with `id`, `title` and `description`. The per-feature `state` and the
  learned-skill limits are gone.

### Removed

- The generic sandbox operations. Use the Computer methods (`CreateComputer`,
  `StartComputer`, `StopComputer`, `DeleteComputer`) instead.

### Fixed

- `Session.Exec` no longer reports a successful exit when a completion frame
  follows an error without an exit code. Truncated streams keep an unknown exit.
- `Client.AvailableLocations` with an unknown or revoked `pk_` says so
  (`invalid project client key`) and wraps `*InvalidClientKey`, the type
  project-token minting returns, so `errors.As(err, &invalidKey)` works. It is
  still a `*LocationDiscoveryError`.

## [0.3.14] — 2026-09-30

### The SDK at the start of the beta

- **Computers.** `Client.CreateComputer` provisions a persistent Computer, in a
  country you choose (`AttachOptions.Location`) or one picked for you, and
  `AttachComputer` or `AdoptExisting` reconnects it later from the identity and
  keys your backend keeps. `Computer.Stop` saves cookies, learned skills and
  localStorage and releases the runtime; `DeleteComputer` removes a Computer and
  its saved state for good.
- **Credentials.** Your backend holds the project `pk_` key and each
  Computer's keys. Private keys never leave it, so Pine can't read a
  Computer's saved state. The SDK mints the short-lived credentials each call
  needs; tokens are never method parameters.
- **Sessions.** Several sessions on one Computer, each with its own browser
  window, shell and work directory, sharing the Computer's sign-ins.
- **Pine's agent.** `Session.Agent()` runs a goal, then steers, answers,
  cancels or resets it; its events stream with typed constants and resume from
  a saved cursor. Secrets reach the agent by name (`$NAME`) and never enter the
  conversation.
- **Driving it yourself.** `Session.Drive()` observes the page and acts on it,
  with helpers such as `Click`, `TypeText` and `Scroll`, and manages tabs;
  `Session.Exec` runs shell commands.
- **Files and artifacts.** Read and write the work directory, and list, open or
  zip the agent's artifacts.
- **Skills.** `Session.Learn` and `Session.Teach` draft a skill from a finished
  turn or a person's demonstration for you to review. `ListSkills` lists the
  Computer's skills, and `SetSkillEnabled` switches Pine's optional skills on
  or off.
- **Passkeys.** List and remove a Computer's passkeys, and approve or decline
  new ones.
- **Your users at the desktop.** `Session.Delegate` mints a short-lived,
  view-only desktop envelope for the web SDK; `TakeControl` and
  `ReleaseControl` pass the controls.
- **Errors.** A flat set of errors you can match with `errors.Is` and
  `errors.As`, including project capacity (`ErrCapacityExceeded`), binding
  conflicts and a Computer whose runtime is gone.

### Earlier releases

- Versions before 0.3.14 were private previews. This release supersedes them.
