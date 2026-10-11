# Changelog

All notable changes to the Pine Computer Go SDK (`go.pinesandbox.io/computer`,
package `pinesandbox`) are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/). The current beta compatibility
line is `v0.3.x`; review release notes before adopting a new minor version.

## [0.3.15] — 2026-10-11

### Added

- `Client.CreateDiagnostic` and `Client.GetDiagnostic` request and read a saved diagnostic with a support reference, using the project key and Computer ID. An optional session requests a read-only agent investigation with separate private and customer-facing reports; omitting it checks lifecycle status. No customer work is repeated or support ticket opened. Requires the diagnostics backend, coordinator support path, and gateway routes.

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
- **The resident agent.** `Session.Agent()` runs a goal, then steers, answers,
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
