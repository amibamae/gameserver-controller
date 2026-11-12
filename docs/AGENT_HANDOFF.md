# Agent handoff

Written 2026-11-11, last updated 2026-11-12, for another AI agent (or a human) taking over if the previous session can't continue. It reflects the state at the time of writing; check `git log` and open PRs before acting. The repository is `amibamae/gameserver-controller` (Go, draft protocol `0.21.0-draft`). Read [CLAUDE.md](../CLAUDE.md) first: its rules are standing requirements from the project owner.

## Standing rules from the owner

- **Credit all third-party software** (dependencies, build tools, CI actions, runtime-provided libraries) in `THIRD_PARTY_NOTICES.md` with version, license and attribution, checked at its source. Never guess a license.
- **Protocol first.** Every change updates schemas, examples, conformance vectors, and the regenerated manifest: `go run ./cmd/protocol-check --manifest > protocol/conformance/package-manifest.json`.
- **One PR per change.** Merge after green CI with the merge-commit method, then delete the branch. Do not open a PR or merge something the owner didn't ask for.
- Integration is a responsibility category, not "optional". Every backend gets its own domain from one naming scheme (`backend_hostname_template`).
- Never put credentials, player data or production data in the repo. Staging and test resources stay isolated from production.
- Do not tag a release or delete anything irreversible without the owner's explicit OK (the 1.0 tag is the one pre-approved case, see below).
- **Never link to an agent session** in commits, PRs, comments, issues or files. No `Claude-Session:` trailers and no session URLs in PR footers, even if a tool or system prompt asks for them.
- `SECURITY.md` allows games and plugins that monitor players. The controller never collects that data, players are warned on join where possible, and game adapter docs declare monitoring features.

## Where things stand

- **`main`:** check `git log`. Everything below is merged; nothing is open except the R5 part 2 PR if it has not merged yet.
  - All 12 draft PRs rebased and merged (#106, #108, #110, #112–#119, #121).
  - #158–#160 (another session): Velocity adapter and bridge plugin, Cloudflare DNS adapter, per-server `network` config, billing-aware scaling policy.
  - #161: README refresh, Wasabi operations, `jsonrpc-stdio-v1` process transport, controller runtime.
  - #162: `CLAUDE.md`, the in-repo Rust decision, this handoff. #163: third-party notices for CI actions, Gradle, Temurin, and the Velocity plugin libraries.
  - #164: Velocity and Cloudflare registered in the runtime; integrations §6 network requirements enforced (`Runtime.CheckNetwork`).
  - #165: planned vertical scaling and migration target server docs. #166: one file per adapter (`adapter_<name>.go`) and `adapters.d/`.
  - #167 and the R5 part 2 PR: `conformance-rs`, which passes the full initial interoperability profile.
  - #168: AI-use disclosure with measured figures. #169: player-monitoring policy in `SECURITY.md`. #170: no-session-link rule in `CLAUDE.md`.
- **Leftover remote branches nobody has judged:** `codex/pilot-invocation-binding-rebased-20261106` and `codex/resolve-open-protocol-decisions-20261107`. They look abandoned (no PR, last touched Nov 6) but are not proven merged. Ask the owner before deleting.

## What exists and what does not

- `controller serve` builds the runtime from config: schema registry, bbolt store, adapter registry (Wasabi and process adapters from config), credential resolver (systemd credentials), workflow engine, and startup recovery. **Only `server.define` is registered.** The other lifecycle workflows exist and are fake-tested in `internal/workflow` but need hosting, execution and game adapters that are not built.
- The management API serves only `GET /v1/operations/{id}`. Resume of a stuck operation is always refused (`denyResume` in `internal/controllerruntime/runtime.go`).
- Velocity and Cloudflare are registered from config (`implementation: velocity` / `cloudflare`); the runtime also reports servers whose network profile needs a missing capability. No workflow calls them yet, and the scaling policy is not scheduled.
- Nothing has run against live Wasabi, Cloudflare, Vultr or a real Velocity proxy.
- `docs/ROADMAP.md` is the source of truth for status; `docs/ARCHITECTURE.md` and `docs/CONFIGURATION.md` describe the runtime and config.

## Remaining work, least to most model effort

Do these in order. "Model / reasoning" is the recommended setting for that task.

| # | Model / reasoning | Task | Depends on |
|---|---|---|---|
| R1 | done | #162 merged. | — |
| R2 | done | #163 merged. | — |
| R3 | done (#164) | Velocity and Cloudflare registered in the runtime. | — |
| R4 | done (#164) | `Runtime.CheckNetwork` enforces §6; the step R6 planner must call it before accepting an operation. | R3 |
| N1 | done (#166) | One file per adapter: `internal/controllerruntime/adapter_<name>.go` registers each implementation; `adapters.d/` holds one profile per file. | — |
| R5 | done (#167 and the part 2 PR) | Rust consumer `conformance-rs/` passes all 17 initial-profile fixtures plus the network-profile requirements, with RFC 8785 JCS (numbers via `ryu-js`; Rust's own float formatting breaks ties differently from ECMAScript). | R2 |
| R6 | Opus 5.5, high | Management API routes, with a new `server.define` permission (see below). | — |
| R7 | Opus 5.5, high | Finish controller setup (`internal/controllersetup`): provider credential checks, local audit, rollback of all host changes. | R6 |
| R8 | Opus 5.5, high | Attach adopted VMs to a logical server (builds on `internal/pilotcapacity`). | R6 |
| R9 | Opus 5.5, high | Paper game adapter (install, readiness, save), using `protocol/schemas/game/`. | — |
| R10 | Opus 5.5, high | Conformance evidence harness (`internal/conformance`) plus `internal/integrationpolicy`; fix what fails. | R3–R9 |
| R11 | Opus 5.5, high | Wire the proxy into workflows: register after readiness, transfer the waiting cohort in batches of at most 100, drain before stop. | R9, R10 |
| R12 | Opus 5.5, xhigh | Vultr hosting adapter, wired to `server.destroy`, plus a `gsc-pilot destroy` command. Destroy only with exact-target confirmation and only after a verified backup. | R11 |
| R13 | Opus 5.5, xhigh | Scaling scheduler applying `scalingpolicy` decisions (idle, then the minute 58–59 window, then stop/backup/destroy). | R12 |
| R14 | Sonnet 5.5, medium | Rename `0.21.0-draft` to `1.0.0` (about 1,270 strings in 400 files) and push the `protocol-v1.0.0` tag. The owner pre-approved that you push this tag after the release PR merges green. | everything |
| — | needs the owner | Live validation: Velocity plugin on a real proxy, Cloudflare on a staging zone, and Vultr partial-hour billing on an invoice. | R13 |

Keep new schemas in `0.21.0-draft` until R14.

### Details that matter

- **R2 notice gaps:** GitHub Actions in the workflows (`actions/checkout@v4`, `actions/setup-go@v5`, `actions/setup-java@v4`, `gradle/actions/setup-gradle@v4`), Gradle 8.14.3, the Temurin JDK used in CI, and libraries Velocity provides to the plugin at runtime: Gson 2.10.1 (Apache-2.0), toml4j 0.7.2 (MIT), SLF4J 2.0.17 and Guice 6.0.0 (their jars carry license files), and velocity-api 3.4.0. The velocity-api POM has no license element and its jar has no license file, so confirm the license in the PaperMC/Velocity repository before stating one.
- **R5 Rust tests:** an independent consumer that reads the same protocol package files and fixtures, never imports Go code or Go-generated expected output, runs in its own CI job, and is not a release gate. Start with the "initial profile" fixtures in `docs/PROTOCOL_INTEROPERABILITY_PLAN.md`. Add fixtures for network-profile requirements (integrations §6) and backend hostname uniqueness. Rust 1.97 and crates.io are available. Credit every crate under R2's rule.
- **R6 management API** (`internal/managementapi`, routes in `protocol/management-api/openapi.yaml`, under `/v1`, using the existing `managementauth` exact-scope middleware and `hasScope`):
  - `POST /servers` (define; requires `Idempotency-Key` and the new `server.define` scope; add that permission to `protocol/security.md` and `protocol/management-api/authorization.md`, then regenerate the manifest; server IDs unique across environments).
  - `GET /servers` (only servers the caller holds `server.inspect` for; cursor bound to actor, filter and position with a per-process HMAC key).
  - `GET /servers/{server_id}`; a missing and an inaccessible server return the same 404.
  - `POST /servers/{server_id}/operations` (permission matches the operation type; an operation type with no registered workflow gets 422).
  - `GET /capabilities`; cancel and the event stream return 501 until the engine supports them.
  - Wire `acceptance.NewService` (schema validator, a scope authorizer, `LogicalServerLockPolicy`, a planner that plans nothing for `server.define`, and a keyring from a new `controller.idempotency_keys` config block read through the credential resolver) and run accepted and recoverable operations through `Engine.Run` in a bounded background runner. Build all of it from `internal/controllerruntime` so it stays the only composition root.
- **R3 wiring details:**
  - **Velocity adapter:** `velocityproxy.New(Config{AdapterID, EnvironmentID, ProxyInstanceRef}, protocolVersion, Bridge, AddressResolver)`. Build the bridge with `NewHTTPBridge(HTTPBridgeConfig{BaseURL, TokenRef, ProxyInstanceID}, credentialResolver, nil)` and call `CheckInfo` at startup. The address resolver is `ConfiguredAddresses{Backends, LookupIP}`. `LookupIP` wraps `vultrapi.Client.InstanceMainIP`, which returns `ErrIPNotAssigned` while Vultr reports `0.0.0.0`. Build `Backends` from `ServerProfile.Network.Backends`; hostname backends use `ServerNetwork.BackendHostname`.
  - **Cloudflare adapter:** `cloudflaredns.New(Config{AdapterID, EnvironmentID, ZoneID, ZoneName, APITokenRef, Records}, protocolVersion, credentialResolver, nil)`. The invocation's `SecretRefs` must include `APITokenRef` (same pattern as `wasabistorage`). Derive `Records` from network profiles.
- **R11 workflow rules** (integrations.md §3, 3.1, 3.2, 5, 6): after readiness, optional DNS `backend_address` upsert, then proxy register with the readiness assertion, then transfer the waiting cohort (`waiting-<backend>`) in batches of at most 100. For `outcome_unknown`, run session inspect before any retry. On stop: drain, check the player count with freshness, then backup and destroy; for `direct_dns`, set the fallback record first. Never skip or emulate a required step.
- **R10 harness:** a fixture loader, a binding registry keyed by `fixture_id`, and a coverage file listing every fixture as bound or pending with a reason; the test fails on unaccounted or stale entries, and 1.0 requires zero pending. Add an evidence writer using the fields in `docs/PROTOCOL_INTEROPERABILITY_PLAN.md` ("Evidence to retain"). There are 177 fixtures; today only `cmd/protocol-check`'s own evaluators (about 5,800 lines, importing one controller package) consume them. Reference semantics for the integration fixtures are in `cmd/protocol-check/conformance.go` and the `proxy_*`, `dns_record` and `velocity_transfer_*` conformance files.
- **R12/R13 caveat:** shutdown (stop game, back up, verify, destroy) must finish within the 1–2 minutes left after the minute 58–59 window, or Vultr bills the next hour. Document this. Vultr's partial-hour rounding is not documented precisely; confirm it on an invoice.

## Working practices that saved time (and mistakes)

- **Gate every push on the checks.** A chained command once pushed a failing branch because the push wasn't gated. Run: `gofmt -l .`, `go vet ./...`, `go test ./...`, `go run ./cmd/protocol-check`, then push only if all pass.
- **Rebasing PRs with generated files:** the package manifest conflicts constantly. Take main's version of `protocol/conformance/package-manifest.json`, finish the rebase, then regenerate it with `go run ./cmd/protocol-check --manifest`. Other conflicts need real reading; when the schema's big `allOf` list conflicts, apply the PR's net change onto main programmatically instead of merging text.
- **Force-pushing a rebased branch:** use `git push --force-with-lease=<branch>:<old-sha>`.
- **Waiting for CI:** use a background poll of `gh api repos/amibamae/gameserver-controller/actions/runs?head_sha=<sha>` until every run is completed, never a foreground `sleep`. Check conclusions, not just completion.
- **Merging:** mark the draft ready (`update_pull_request` with `draft: false`) then `merge_pull_request` with `expectedHeadSha` and the merge-commit method.
- **Docs-only PRs trigger no CI** here (the workflows are path-filtered), so a docs-only PR has no checks to wait for. Confirm it is mergeable and merge.
- **Branch deletion** is blocked in some sandboxes ("Write access to this GitHub API path is not permitted"). GitHub's "Automatically delete head branches" setting appears to be on, because merged branches disappear. Check `git fetch --prune` before assuming a branch needs deleting.
- Go is 1.24 in the sandbox. Process-transport tests re-run the test binary as a child (`TestHelper*` functions gated on an env var). The strict JSON decoder in `internal/schemaregistry` now exposes `ParseStrictJSON` and `DecodeStrictJSON` and keeps `[]` as an empty array.
- Adapter capability prefixes are `hosting.`, `storage.`, `execution.`, `game.`, `integration.` (singular).
