# Changelog

All notable changes to the Pine Computer Go SDK (`go.pinesandbox.io/computer`,
package `pinesandbox`) are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and the SDK uses
**pool-version-aware semver**: in the 0.x phase, version is
`0.<POOL_VERSION>.<patch>`. So `require go.pinesandbox.io/computer v0.3.x`
targets `pine-cua-pool-v3` (the compatibility contract integrators pin to).

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
