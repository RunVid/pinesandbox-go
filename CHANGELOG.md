# Changelog

All notable changes to the Pine Computer Go SDK (`go.pinesandbox.io/computer`,
package `pinesandbox`) are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and the SDK uses
**pool-version-aware semver**: in the 0.x phase, version is
`0.<POOL_VERSION>.<patch>`. So `require go.pinesandbox.io/computer v0.3.x`
targets `pine-cua-pool-v3` (the compatibility contract integrators pin to).

## [0.3.7] — 2026-07-07

### Removed
- Removed `Computer.RefreshBrokerGrant` and the attach-provider `GrantRefresh`
  mint. Broker-grant refresh is now only the platform lease refresher;
  integrations do not run a timer or call a refresh API.

### Changed
- Attach-credential mints now type authz failures precisely: invalid `pk_`
  returns `*InvalidClientKey` (401), while disabled/revoked/insufficient project
  access returns `*ProjectAccessDenied` (403).
- `ControlTokenSource` derives cache freshness from the minted token's own
  `exp` claim when present, using response expiry metadata only as fallback.

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

[Unreleased]: https://github.com/RunVid/PineSandbox/commits/computer-skills
