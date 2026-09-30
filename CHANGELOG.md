# Changelog

All notable changes to the Pine Computer Go SDK (`go.pinesandbox.io/computer`,
package `pinesandbox`) are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/). The current beta compatibility
line is `v0.3.x`; review release notes before adopting a new minor version.

## [0.3.14] — 2026-09-30

### Fixed

- Graceful stop waits for the complete browser-state save before sandbox deletion. Save failures retain the source binding and raise `StopSaveError`. An already-absent runtime is stopped without a save acknowledgment; start/attach restores the latest committed state.
- The stop deletion budget now includes DELETE, confirmation requests, retries and polling sleeps. Expiry retains the source handle with deletion unconfirmed; caller cancellation remains an error.
- `AdoptExisting` accepts `AdoptOptions{Ephemeral: true}` to preserve save-skipping stop behavior when reconstructing an ephemeral binding.
- Retry classification recognizes runtime initialization and approved-skill
  publication outages when an older response omits the wire retry hint.
- Expiry of the SDK-owned readiness budget now surfaces as `*ReadyTimeoutError`
  (`errors.As`) rather than `context.DeadlineExceeded` or a transport timeout;
  caller cancellation still surfaces as `context.Canceled`.
- Create/attach now uses `AttachOptions.ReadyTimeout` (default 300s) for
  allocation and pod readiness. `Timeout` only sets sandbox lifetime; bind
  keeps its separate budget. Set `ReadyTimeout` explicitly when capacity
  needs longer. Failed attach cleanup now uses an independent bounded context
  so an expired deadline or caller cancellation cannot suppress allocation
  deletion.

### Changed
- `GetSkill` returns skills authored on the Computer and the `computer-agent` role guide. Pine's other skills stay listed with their name and description, and requesting their content now fails with 403.
- Computer attach rejects nonempty `AttachOptions.PodEnv` locally before
  provisioning. Leave the deprecated field nil or empty; environment overrides
  cannot configure an already-started Computer runtime.
- Session browser metadata no longer exposes a raw Chrome DevTools WebSocket
  URL. Browser automation remains available through Pine's session-scoped
  Computer commands; CDP is an image-owned runtime transport.

### Removed
- The deprecated `Computer.Metrics` helper. Coordinator metrics are an
  image-owned observability surface, not a Computer SDK endpoint.

### Added

- `Client.DeleteComputer(ctx, id)` and `Computer.Delete(ctx)` permanently
  delete a Computer via Portal `DELETE /v1/computers/{id}`. `Computer.Delete`
  kills a live sandbox first. Idempotent and non-disclosing: unknown,
  already-deleted, and other projects' ids return nil. A malformed id returns
  the new `*ComputerDeletionError`; 403 returns `*ProjectAccessDenied`; 401,
  429 and 5xx return `*AttachCredentialsError`. Saved state is purged after
  24 hours and the id is never accepted again.
- `Computer.SetSkillEnabled` switches one of Pine's standard or feature skills
  (for example `pdf`) on or off for the agent processes the Computer starts
  next; the switch is saved with the Computer's state (not on an ephemeral
  binding). A running agent keeps its catalog until its session's agent is
  reset. `ListSkills` entries now carry `origin`, `class`, `enabled`, and
  `disabled_reason`.
- `Computer.Capabilities` returns the capability manifest: each product
  feature with its state for the Computer's binding (`enabled`,
  `not_entitled`, `not_configured`) and the limits the Computer enforces.
- `ErrCapacityExceeded` (`errors.Is`) and `*CapacityExceededError` (`errors.As`)
  report a create/attach refused because the project is at its concurrent
  Computer limit (HTTP 429, `COMPUTER_CAPACITY_EXCEEDED`). Nothing was
  provisioned and the SDK does not retry. Control-plane errors now also carry
  the server's machine `Code`.
- `ComputerLocation{Mode: "direct"}` selects browser internet access without
  a regional upstream proxy. Country and Direct are mutually exclusive.
- Computer-scoped custodial passkey management: typed list/delete operations,
  pending enrollment approval/decline, and session-scoped enrollment intent,
  matching the Ruby SDK and coordinator OpenAPI contract. Agent sessions can
  now list and decide only their own attributed ceremonies while keeping the
  website tab active. Pending ceremonies expose `PresentedAt` after the
  built-in Chrome action popup has rendered, without treating presentation as
  consent.
- `AnswerOptions.Secrets`, `AnswerWithOptions`, and `AnswerAskWithOptions`
  deliver a requested password or one-time code through the protected session
  file while answer text references only `$NAME`. The existing `Answer` and
  `AnswerAsk` signatures remain source-compatible.

## [0.3.13] — 2026-08-07

### Fixed
- `Bind` no longer seals the coordinator's usage-reporter credential into the
  HPKE bind payload, and `AttachCredentials` no longer carries
  `UsageReporterGrant` / `UsageReporterGrantExpiresAt` / `UsageReporterID`. That
  credential is minted for the platform's own use, so routing it through the SDK
  made metering depend on the SDK version an integrator had pinned — a release
  that predated the fields could not attach at all against a metered runtime. The
  coordinator now obtains it over its own internal channel. No integrator action
  is required; custom `AttachCredentialsSource` implementations that still
  populate those fields keep compiling only if they drop them, since the struct
  fields are gone.

## [0.3.12] — 2026-08-07

### Added
- `EventInput` identifies accepted run, answer, and steer input in the durable
  agent event stream, allowing replaying clients to reconstruct the ordered
  task transcript.
- `RunOptions.Secrets` / `SteerOptions.Secrets` deliver caller-supplied
  credentials/configuration to the session secrets file (`$SESSION_SECRETS`) instead of
  model-visible text: the agent references values as `$NAME`, so literals never
  enter the task transcript, events, or logs. Ephemeral (pod-scoped; re-send
  after re-attach); see the spec's `SessionSecrets` schema for naming rules.
- Computer create/attach accepts `AttachOptions.Location` with a canonical
  ISO 3166-1 alpha-2 country intent. The option is authorized through Portal
  rather than lifecycle pod configuration, and `Computer.Location()` exposes
  the effective binding location returned by Portal. A later attach may select
  another admitted country after the Computer is stopped or killed.
- `Client.AvailableLocations` returns the project-authenticated, inventory-backed
  country catalog and the server-owned default without exposing proxy pools.

### Changed
- Installation documentation now reflects the already-public
  `go.pinesandbox.io/computer` vanity module; `GOPRIVATE` and GitHub
  authentication are not required.
- Portal attach request/response wire structs now use exact spec-conformance
  gates, so an added or renamed Portal field cannot silently leave the Go SDK
  behind while ordinary compilation remains green.
- `AgentResult.Summary` is the standalone source of truth for the actual goal
  outcome; the normal terminal envelope remains `ok/completed`.
- Replace the unused generic `Finding` / `AgentResult.Findings` surface with the
  optional typed `AgentResult.Author` payload used only by skill-author tasks.
  Older SDK releases still decode new result JSON because `findings` is optional
  in their Go structs and unknown `author` fields are ignored.

## [0.3.11] — 2026-07-21

### Added
- Agent TaskEvents now expose the coherent `Controller` / `ModeEpoch` control
  snapshot and a typed `AgentEvent.ControlChange()` accessor for additive
  `controller_changed` frames, so a task follower can track human takeover and
  agent release without parsing raw event JSON.
- Ephemeral persistence mode: `AttachOptions.Ephemeral` on `CreateComputer` /
  `AttachComputer` provisions an access-lease-only Computer that persists
  nothing. An ephemeral attach binds no capture keypair, omits
  `pk_computer`/`key_generation` from the attach-credentials mint, carries no
  `key_assertion`, seals only the `broker_grant` into the HPKE bind payload (no
  `computer_key`), and `Stop` skips the pre-stop checkpoint.
  `Computer.PersistenceMode()` surfaces the mode the coordinator echoed on the
  bind (`"persistent"` or `"ephemeral"`). The persistent default
  (`Ephemeral: false`) is unchanged.

## [0.3.10] — 2026-07-18

### Added
- Typed data-plane control flow: `ErrControlNotHeld`, `ErrSessionNotFound`, and
  `SandboxGoneError` (which preserves the underlying `APIError` diagnostics).
- `IsRetryable(error) bool` exposes the server's problem-detail retry judgment
  without treating transport failures or arbitrary mutations as replay-safe.

## [0.3.9] — 2026-07-18

### Added
- Required v3 asymmetric state encryption (component envelope v3): `CaptureKeypair`
  (`GenerateCaptureKeypair`, `Fingerprint`) plus
  `Computer.SetCaptureKeypair` / `AddPriorCaptureKeypair`. Supply the current
  keypair through `AttachOptions.CaptureKeypair` and any retained generations
  through `PriorCaptureKeypairs`; the high-level create/attach flows configure
  them before provisioning. Every v3 attach submits `pk_computer`/`key_generation`, forwards the
  portal's `key_assertion` on the bind, and runs the two-round restore
  automatically — the SDK unwraps each component's sealed secret with the
  matching keypair generation and answers the coordinator's
  `restore_challenge`. Retain superseded keypair generations until their
  state is re-sealed; a restore that needs an unregistered generation
  fails with a typed, actionable bind error.
- `TokenRejectedError` **replaces `RebindRequiredError` (breaking rename)**:
  a 401 on a bound `ct_`/`ps_` is a report that the coordinator did not
  recognize the token, **not** an instruction to re-attach — on a live,
  current sandbox this state is `binding_auth_lost`; attach only on confirmed
  sandbox-gone evidence. Migration is a compile-caught find/replace of the
  type name; the `Error()` string no longer says "rebind required".
- `ErrTaskNotFound` (`404 /errors/task-not-found`) and `ErrNoActiveTurn`
  (`409 /errors/no-active-task`): the two idle-session sentinels matching the
  wire pairs the coordinator actually emits. A `task-not-found` read means a
  **valid idle session** — start a turn; never re-create the session.

### Changed
- Computer provisioning now uses the smaller `POST /computer-sandboxes`
  request. Deployment and resource selection are backend-managed rather than
  client-selected.
- Attach uses Portal binding revisions and a stable idempotency receipt.
  Concurrent losers return `BindingRevisionConflictError` and must reload/adopt
  the integrator database winner. `AttachAuthorizationCommittedError` preserves
  a Portal-committed revision when the later coordinator bind fails and, for
  `CreateComputer`, carries the durable credentials needed to adopt or retry
  the newly created Computer.
- Each sandbox receives exactly one Portal attach authorization. Readiness and
  transient restore retries replay that committed envelope; an expired restore
  challenge restarts round one without re-minting. Pod-identity changes are
  terminal for that sandbox and recover on a fresh sandbox.
- Portal errors expose `Code` (the stable RFC 9457 problem type) plus optional
  spec-defined `Reason`; raw internal exception text is not a client contract.
- Capture-key attach options are validated as one immutable generation set
  before the Computer is mutated or a sandbox is provisioned. Reusing a
  generation with different key material is rejected locally.
- `ErrNoActiveTask` is deprecated but **left frozen** at its original
  `{404, /errors/no-active-task}` value. `APIError.Is` matches by problem-type
  slug only, so this sentinel already matched the coordinator's real
  `{409, /errors/no-active-task}` mutation error by slug; its gap was only that
  it never matched a task READ on a never-run session
  (`{404, /errors/task-not-found}`, a different slug), which `ErrTaskNotFound`
  now fills. Migrate reads to `ErrTaskNotFound` and mutations to
  `ErrNoActiveTurn`; the deprecated sentinel's fields are unchanged (no
  breakage for code that inspects them).
- Bind response `expires_at` is deprecated server-side: `ct_` is now
  binding-lifetime (no idle expiry, no renewal). This SDK never parsed the
  field, so no code change is needed to consume new coordinators.

## [0.3.8] — 2026-07-13

### Added
- `Session.Refine(ctx, skill, version, guidance)` starts an asynchronous author
  turn that revises an existing immutable skill version. It uses the Computer's
  `ct_` and returns the raw 202 body; consume progress and the terminal draft
  through `Session.AuthorEvents` like Learn and Teach.
- **`Session.OpenArtifact`** — a streaming artifact read (`io.ReadCloser`):
  `ReadArtifact` without buffering the whole file, for consumers that copy
  artifacts onward (object storage, an HTTP response) and should hold O(1)
  memory rather than up to the platform's 100 MiB per-artifact cap. The open
  retries transient faults under the same budget as the buffered read
  (pre-headers only — the returned stream is caller-owned). The caller must
  `Close` the returned reader.

### Fixed
- Cold `CreateComputer` / `AttachComputer` no longer die on a fixed 30s transport
  timeout. The unary timeout is now a FALLBACK applied only when the caller passes
  no `context` deadline, so a caller's deadline is honored instead of clipped to
  30s; and attach bounds the whole provision (`POST /sandboxes` + readiness poll)
  by `AttachOptions.Timeout` (the readiness budget, default 300s). Pass a longer
  `AttachOptions.Timeout`, or a longer `context` deadline, for unusually slow
  provisioning — other calls keep the 30s default.

## [0.3.7] — 2026-07-07

### Removed
- Removed `Computer.RefreshBrokerGrant` and the attach-provider `GrantRefresh`
  mint. Broker-grant refresh is now only the platform lease refresher;
  integrations do not run a timer or call a refresh API.

### Changed
- Attach-credential mints break out `*ProjectAccessDenied` (403 — the project,
  key, or computer may not mint now) from the generic `*AttachCredentialsError`.
  A bad key (401), a rate limit (429), and server errors stay
  `*AttachCredentialsError` (with `.Status`), so `errors.As` on the attach call
  keeps catching them.
- `ControlTokenSource` takes its cache expiry from the minted token's own `exp`
  claim when present, falling back to the response expiry metadata otherwise.

## [0.3.6] — 2026-07-06

### Added
- **`Artifact.Filename`** — the human-facing name WITH extension (e.g.
  `filled_w9.pdf`), the basename of the id-prefixed `RelativePath`. Display and
  name downloads by this, never by `ID` (an `art_…` hash). Additive/non-breaking;
  derived from `RelativePath` when talking to a coordinator that predates the
  field, so it is always populated.

## [0.3.5] — 2026-07-06

### Changed
- **Errors are self-describing at the RESOURCE level.** Every error surface now
  folds a resource-first context — `(host=<computer host>, op=<METHOD path>,
  request_id=<id>)`, `op` query-stripped — into its message, so a generic handler
  that logs only `err` sees WHICH Computer and WHICH operation failed (the primary
  spine) plus the `request_id` precision handle. Coverage: `*APIError` (new
  exported `Host` / `Op` fields, set by the transport + coordinator);
  `*TimeoutError` / `*ConnectionError` (new `Host` / `Op` / `RequestID` fields,
  rendered once — message shape `pinesandbox: request timed out (host=…, op=…): …`);
  `ErrStreamLost` (host + op + the last established stream's `request_id`); the
  control-plane errors (`Host`/`Op`/`RequestID` on `cpBase`); and the portal
  token/attach errors (`Host`/`Op` carried through from the wrapped `*APIError`).
  Class/field-additive and wire-compatible — no existing field or error type
  changed — but **error message STRINGS now carry troubleshooting context** (a
  0.3.x behavior change for anyone keying tests/log-matchers off `err.Error()`).
  Added a Troubleshooting section to the README.

### Deprecated
- `Computer.Metrics`: pre-gateway operator convenience — the gateway blocks
  `/metrics` on the public hosts (direct in-cluster/local addressing only),
  and checkpoint/fleet health is now a first-class platform alerting surface.
  Kept for compatibility; slated for removal after a downstream-usage check.

## [0.3.4] — 2026-07-03

### Changed

- **Structured `AgentUsage` (breaking; matches the v3 coordinator wire).** The
  flat `{Tokens, Cost, ComputeMs}` tally is replaced by the spec's structured
  shape: `Usage.LLM` (`AgentTokenUsage` — disjoint
  input/output/cache_read/cache_write + Pine-computed total), `Usage.Duration`
  (`AgentDuration` — `TotalMs` + `ActiveMs`, active excludes human-wait), and
  `Usage.Cost` (`AgentCost` — USD; `Total`/`LLM` are `*float64`, nil when the
  model is un-carded — never a guessed 0; `Compute` nil until a per-second
  rate exists). `charge_id` rides the `AgentEvent` envelope.

### Added

- **Access-lease error sentinels.** `ErrLeaseExpired` (403 — definitive portal
  refusal: re-attach or surface the suspension) and
  `ErrLeaseRefreshUnavailable` (503, retryable — transient refresh failure:
  retry, do NOT re-attach), `errors.Is`-matchable like the other control
  sentinels; both pinned in the shared `error-taxonomy.json`.

## [0.3.3] — 2026-06-26

### Changed

- **Typed control, handoff, and computer-use surfaces (breaking).** The opaque
  hand-built pieces are now spec-typed:
  - `Session.ControlState` returns typed fields (`Controller`, `Epoch`,
    `SessionName`, `IdlePaused`, `IdleDeadline`, `LastTransitionAt`) + `ETag`
    instead of a raw `.State` blob (it models the full schema — no raw hatch).
  - `Session.UpdateControl` takes a typed **`ControlPatch`** (`Controller`,
    `IdlePaused`, `IdleDeadline` absolute or `IdleDeadlineIn` relative, `ActorType`;
    nil = leave unchanged) instead of `body any`. New convenience **`TakeControl(ctx,
    ...ControlOption)` / `ReleaseControl`** — the common case takes no args (no magic
    `"user_click"`); pass **`WithForce()`** to override an existing holder. They
    handle the ETag-fetch → If-Match → 412-retry (a fresh Idempotency-Key per
    attempt — the retry must not reuse the rejected key).
  - `Session.ListHandoffs` returns a **`*HandoffList`** (`Handoffs` + the
    `NextBefore` pagination cursor); `GetHandoff` returns `*Handoff`. Summary fields
    are typed (`HandoffID`, `StartedAt`, `EndedAt`, `ControllerAtStart/End`); the
    deep forensic detail (nav / form_submit / xhr_submit / clicked_action) is in
    `Handoff.Raw` for `GetHandoff`.
  - `DriveMode.ComputerUse` returns a typed **`ComputerUseResult`** (`Screenshot`
    for `action=="screenshot"`, else `OK`). Added typed
    convenience helpers **`Click` / `RightClick` / `DoubleClick` / `MouseMove` /
    `TypeText` / `Key` / `Scroll` / `Screenshot`** over the raw `ComputerUse`.

  The design line: type the surfaces with stable spec schemas you act on; keep raw
  (with a `.Raw` escape hatch / typed accessor) the loose/forensic/admin payloads.

### Added

- **Typed constants for the strings you match/supply** (no more magic strings):
  agent event kinds (`EventNeedsInput`, `EventResult`, …), `Controller*`, `Actor*`,
  `Terminal*` reasons, and control-event types — so you write `ev.Type ==
  pine.EventNeedsInput`, not `"needs_input"`. (Additive sets — switch with a default.)
- **`AgentMode.AnswerAsk(ctx, ask, text)`** + `AgentEvent.Ask` now carries `TurnID`,
  so answering a `needs_input` pause needs no id plumbing:
  `if ask, ok := ev.Ask(); ok { ag.AnswerAsk(ctx, ask, reply(ask.Question)) }`.

## [0.3.2] — 2026-06-25

### Fixed

- **Control + skills-authoring now route through the Computer's `ct_`.** Take-control
  (`Session.UpdateControl` / `ControlState` / `ControlEvents` / handoffs) and the
  skills-authoring mutations (`Session.Learn` / `Teach` / `AuthorSkill` / `CancelAuthor`)
  were sent with the session `ps_`, but the coordinator makes these operator routes
  `ct_`-only (a `ps_` is rejected `403`) — so take-control and authoring **failed**. They
  now use the Computer's `ct_`, matching the agent mutations (run/steer/answer). The model:
  **`ct_` is the operator surface** (control lease + agent/authoring mutations + lifecycle);
  **`ps_` is the session's own drive + reads**. The skills-authoring lifecycle is entirely
  `ct_` — including its event stream — so a ct_-only handle can both start and watch a
  learn/teach. `control/notify` stays `ps_`.

### Added

- **`Computer.AdoptSession(name, ps_)`** — rebuild a drive-capable `*Session` from a
  persisted session token with no coordinator round-trip (the session analog of
  `Client.AdoptExisting`). This is the reuse path for a **stateless / multi-instance /
  restarted** backend: persist `{name, ps_}` at `CreateSession`, then per request
  `AdoptExisting` the Computer (gives `ct_`) and `AdoptSession` (gives `ps_`).
  `Computer.Session(name)` cannot be used for drive reuse — the coordinator redacts the
  `ps_` on read, so its handle does ct_-routed ops only; a drive op on it `401`s.
- **`ErrSessionLimit` (409 `/errors/session-limit`).** `CreateSession` at the Computer's
  concurrent-session cap now carries a typed sentinel — `errors.Is(err, pine.ErrSessionLimit)`
  — so a hit cap is distinguishable from a malformed request; free a slot with
  `DestroySession`, then retry. (Pairs with the coordinator's typed-problem fix.)
- **`AgentEvent.Ask()` — typed `needs_input` payload.** Instead of hand-parsing
  `ev.Payload` to answer an `ask`, `ev.Ask()` returns a typed `*AgentAsk`
  (`RequestID`, `Question`, `Context`, `Options`) when the event is a `needs_input`
  pause: `if ask, ok := ev.Ask(); ok { ag.Answer(ctx, ask.RequestID, …, ev.TurnID) }`.
  Other event payloads stay raw (`ev.Payload` / `ev.Raw`) by design.

## [0.3.1]

### Added

- **Typed, resuming agent/control event iterators.** `AgentMode.Events` and
  `Session.ControlEvents` now return Go 1.23 `iter.Seq2[AgentEvent, error]` /
  `iter.Seq2[ControlEvent, error]` instead of raw-byte callbacks. The typed
  `AgentEvent` mirrors the spec `TaskEvent` envelope (`Type`, `EventID`, `Ts`,
  `TaskState`/`Reason` pause semantics, `Terminal`, …) with a `Raw` escape hatch.
  The continuous feed transparently resumes from the last event id on a dropped
  connection (bounded reconnect budget → `ErrStreamLost` on exhaustion); `break`
  to stop, cancel ctx to end cleanly.
- **`errors.Is`-able control sentinels for the agent lane** — `ErrTaskNotReady`
  (poll again while a turn is in flight), `ErrSessionBusy`, `ErrNoActiveTask`,
  `ErrActionNotImplemented` (no resident agent configured). `APIError.Is` matches
  by RFC-9457 problem-type slug, so `errors.Is(err, pine.ErrTaskNotReady)` works
  on the live wire error while `errors.As(err, &apiErr)` still reaches full detail.

### Changed

- **`DelegatedConnection.ComputerHost` is now a full URI**
  (`https://<id>.computer.<zone>`) per `computer-api.yaml`, not a bare host — so
  the web SDK derives the desktop `ws`/`wss` scheme from it rather than guessing.
  Browser code uses `connectionFromDelegation(envelope)` (web SDK) to get a ready
  `wss://…/vnc/connect` URL; no hand-assembly of the desktop path.

## [0.3.0] — 2026-06-23

### Added

- **Initial release — the full Computer server SDK surface.** Greenfield Go
  port matching the Ruby `pinesandbox` gem's facade contract
  (`sdks/pine-computer/contract/FACADE.md`):
  - `Client` (`NewClient`, `CreateComputer` / `AttachComputer` / `AdoptExisting`).
  - `Computer` — `Attach` (full HPKE bind handshake with the readiness/race retry
    budgets), `Stop` / `Kill` / `Alive`, session management, `AddPriorKey`,
    served-skill admin (list / get / versions / activate / deactivate / delete),
    `LatestSnapshot` / `Capture`, orphan downloads, `RefreshBrokerGrant`,
    `Health` / `Metrics`, `DelegateDesktop`.
  - `Session` — `Exec` (SSE), files, artifacts, tabs, control state (ETag /
    If-Match), handoffs, `ControlEvents`, `DesktopToken`, `Delegate`, `Learn` /
    `Teach` / `AuthorSkill` (+ `AuthorEvents`), `Epoch` / `Focus` /
    `RecreateTerminal`.
  - `AgentMode` (delegate mode — `Run` / `Steer` / `Answer` / `Cancel` / `Reset`
    / `Status` / `Result` / `Events`, ct_-gated mutations + ps_ reads) and
    `DriveMode` (BYOA — `Observe` / `ComputerUse` / `UploadFile`).
  - `DelegatedConnection` (browser-safe handoff — carries no ct_/ps_/JWS) and
    `GenerateCredentials` (offline UUIDv7 id + 32-byte state key).
- Distributed via the vanity import path `go.pinesandbox.io/computer` (decoupled
  from the backing VCS repo) so an org rename never breaks consumers.
- Drift gates wired into CI: CG-1 version identity, CG-4 route conformance, and
  wire-type schema conformance (the OpenAPI-3.1 codegen replacement — see the
  design doc §14.3).
