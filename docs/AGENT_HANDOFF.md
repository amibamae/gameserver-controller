# Agent handoff

Written 2026-11-16, last updated 2026-11-16. It is for another AI agent (Claude, ChatGPT Codex or another) or a human taking over when the previous session can't continue, for example after a usage limit. It reflects the state at the time of writing, so check `git log`, open PRs and remote branches before acting. The repository is `amibamae/gameserver-controller` (Go, draft protocol `0.21.0-draft`).

## Code health requirement (applies to all new and changed code)

The owner requires this for all work. The full text is in CLAUDE.md, "Code health for new and changed code". In short:

- **Functions:** complexity 10 or less, never above 15. Nesting 3 levels or less; use early returns.
- **Conditions:** at most 3 boolean operators in one `if` or `return`. Move a longer condition into a named predicate with the same logic.
- **Duplication:** no copied blocks of 5 or more lines.
- **Files:** one responsibility per file, aim for under about 500 lines, and split into sibling files past about 800.
- **Parameters and errors:** at most 5 parameters, and no silently discarded errors.
- **Tests:** cover every new branch and pin its exact error messages. Before a refactor, write characterization tests and run them green on the old code.
- **Before merging:** if the repowise MCP tools are available, run `get_health` on the changed files. Each changed production file should score 7 or more, no file should get worse, and the scores go in the PR.
- **Exceptions:** skip a rule only when meeting it would make the code worse, and say why in the PR.
- **Scope:** apply the rules to code you add or change. Don't rewrite unrelated code to meet them.

If the repowise tools aren't available, you can measure complexity with a cyclomatic-complexity tool such as `gocyclo`. Don't add any tool to the repository or CI without a `THIRD_PARTY_NOTICES.md` entry whose license you have checked at its source.

## Current work: raising code health (resume here)

**Priority (2026-11-14).** Raise code health across the repository before starting roadmap features. Keep the existing target of 7–8 for the repository average and report scores for each changed file. Use the hosted index to rank candidates and the same local analyzer on base and branch to measure patches. The hosted index was last observed at `354ee6a`, before the backup merge.

- Backup #258 merged at `c839fd1` after green CI. It cleared all 22 changed-function findings. Local Repowise moved `server_backup.go` from 5.37 to 10.00, and every changed production file scored at least 8.04.
- The next health patch separates Vultr request identity and provider checks from the pilot provisioner. It lives on `codex/refactor-pilot-vultr-request` in `/private/tmp/gsc-pilot-vultr-health`. A characterization test pins the resource label and validation order. Local scores for the initial patch are 7.60 for `provisioner.go`, 9.23 for the provider checks and 10.00 for request preparation.
- Claude's unpublished scopes stay on hold until the original changes are available. See the ownership table below. Continue with available hotspots in the pilot and production paths.

The owner's goal is a repowise average health of **7–8**. The method:
- one PR per file group;
- characterization tests first, green on the old code;
- behaviour-preserving changes only: same error messages, check order, persisted formats, order of database writes, and transaction boundaries;
- merge after green CI.

**Done and merged:**

| PRs | What |
|---|---|
| #179–#245 | `cmd/protocol-check` splits; `plugins/velocity`; `vultrapi`, `velocityproxy`, `pilot-capacity-check` and test-file deduplication; `pilothandoff.Run`; `storeEncryptedCredential`; `Engine.dispatch`; `validateServerNetwork`; backup `BuildPlan`; provision `CheckPreconditions`; `RecoverSealedCapture`; seven named conditions |
| #246–#251 | backup `CheckPreconditions` (107 → 9) and `validateServerBackupProfile` (46 → 6); `captureinvoke` `Invoke` and `authorizeCapture`; engine `advanceLocked` (48 → 6); `store/capacity.go` deduplication (35 → 14); `server_destroy` reconcile and `CheckPreconditions` (44 → 5) |
| #252 | the code-health rule in CLAUDE.md |

**Scores at the last re-index (`09ec291`):**
- Repo average 5.88 → **6.21**; production average 5.15 → 5.50.
- Hotspot average 3.94. Repowise deducts for recent churn, and refactors are churn, so this deduction decays over time.

**Scores at the re-index of `354ee6a` (2026-11-14), after Codex's #254, #256 and #257:**
- Repo average **6.26**; production average 5.56.
- `store/logical_servers.go` 8.47.
- `cmd/supervised-pilot/provision.go` 8.58; `cmd/supervised-pilot/main.go` 5.37, still with a function at complexity 47.
- `server_destroy_plan.go` 9.23, `server_destroy_completion.go` 7.95, `server_destroy_reconcile.go` 6.62; `server_destroy.go` still 1.0 at 457 lines.

### Who owns what (check this before you start anything)

Claude is paused until November 19, 2026 at 4 a.m. Vancouver time. Codex is continuing the code-health backlog. Keep unfinished Claude scopes on hold when their work is unavailable. Check the published branches and open PRs before resuming any scope.

| Owner | Files | Branch or PR | Status (2026-11-16) |
|---|---|---|---|
| Codex/Assistant | Go I/O-in-loop findings | feat/bootstrap-scaling branch | **committed and pushed** (0e48322): manifest.go, adapterdir.go |
| Assistant | Rust conformance-rs I/O-in-loop fixes | local work in progress | **syntax verified**, build blocked by MSVC linker |
| Codex | `internal/workflow/server_backup*.go` | #258 merged | completed |
| Codex | `internal/workflow/server_destroy*.go` | #257 merged | `server_destroy.go` still 1.0; further work is Codex's |
| Codex | `cmd/supervised-pilot/*` | #256 merged | `main.go` still 5.37; further work is Codex's |
| Codex | `internal/controllersetup/installer.go`, `cmd/controller-setup/*` | local branch `codex/controller-setup-apply` | **uncommitted** in Codex's checkout on the owner's Mac; not on GitHub yet |
| Codex | `docs/ROADMAP.md` estimates | #255 | open |
| Codex | `internal/workflow/readiness.go` | `codex/refactor-readiness-results` | Evidence validation refactor; local score 7.43 → 7.72, maximum complexity 18 → 15 |
| Codex | `internal/store/logical_servers.go` | #254 merged | done (8.47) |
| Codex | `internal/workflow/server_provision*.go` | `codex/finish-provision-health` | Continuing the five published Claude commits from `3f68309`; local worktree `/private/tmp/gsc-finish-provision-health`; checker passed; local provision score 6.25 → 7.17 after extracting plan, preconditions, reconciliation, completion and results; extracted files score 8.58–10.00 |
| Codex | `internal/workflow/server_restore*.go` | `codex/finish-restore-health` | Five published Claude commits rebased locally in `/private/tmp/gsc-finish-restore-health`; focused restore suite and checker passed after correcting schema-first test expectations; score and full validation pending |
| Claude | `internal/workflow/server_start*.go`, `server_stop*.go` | `claude/r3-server-start-stop` | WIP pushed (1 commit, tests only), unfinished; no PR |
| Claude | `internal/workflow/file_stream_broker*.go` | `claude/r3-file-stream-broker` | WIP pushed (4 commits), unfinished; no PR |
| Claude | `internal/store/record.go`, `internal/store/bolt.go` | `claude/r3-store-record-bolt` | WIP pushed (12 commits), unfinished; no PR |
| Claude | `internal/wasabistorage/adapter.go`, `internal/capturestage/files.go` | `claude/r3-storage-staging` | WIP pushed (10 commits), unfinished; no PR |
| Codex | `cmd/complexity-check/*`, its CI step | #260 merged | checker runs on PRs |
| OpenCode | `plugins/velocity/src/**` | `feat/velocity-bridge-disable-start-locks` | **done locally** (2026-11-15): the 3 hard start-up locks (token-file perms, loopback bind, fallback in `velocity.toml`) are now disableable via a `[security]` table — `enforce-token-permissions` (default on), `enforce-loopback-bind` (default on), `enforce-fallback` (default **off**, since the controller registers the fallback backend at runtime). 41 tests green. 5 commits: `4049b0b`→`2cb8b4a`→`29b836e`→`6643885`→`6e7d2a0`. Owner-authorized to review and merge once green; review in progress, not yet pushed. |
| either | `docs/AGENT_HANDOFF.md` | — | keep it current; rebase before you edit it |

**Next up (2026-11-16):** complete the Rust conformance-rs I/O-in-loop fix for schemas.rs once MSVC is available, then commit and push with Go fixes.

### Rust I/O-in-loop fix (completed)

Fixed two functions in `conformance-rs/src/schemas.rs` that had N+1 filesystem reads:

1. **load()** - hoisted `read_json` calls to batch all schema file reads before the loop
2. **check_examples()** - hoisted `read_json` calls to batch all example file reads before validation

The code changes are syntactically correct. To build and verify:

```powershell
$env:PATH = [System.IO.Path]::Combine("$env:USERPROFILE\\.cargo\\bin") + ";" + $env:PATH
cd conformance-rs
cargo build --release
```

This requires MSVC Build Tools (Visual Studio Build Tools) on Windows, which may not be available in this environment. Once MSVC is installed and the build succeeds, commit the changes to `feat/bootstrap-scaling` along with the Go fixes.

**Completed I/O-in-loop findings:** 4 total
- cmd/protocol-check/manifest.go (Go) ✓ committed
- internal/config/adapterdir.go (Go) ✓ committed  
- conformance-rs/src/manifest.rs (Rust) ✓ syntax verified
- conformance-rs/src/schemas.rs (Rust) ✓ syntax verified

**Notes:**
- Rust build is blocked by MSVC linker availability; syntax has been verified
- Once MSVC is configured, run `cargo build --release` to confirm the changes work
- Commit and push to remote when ready

## Remaining work, least to most model effort

Finish the code-health work above first. Then do these in order. "Model / reasoning" is the recommended setting for that task. For a non-Claude agent, read "Opus 5.5, high" as your strongest model at high reasoning effort, and "xhigh" as the most careful setting available. New code in each of these tasks must meet the code-health requirement.

| # | Model / reasoning | Task | Depends on |
|---|---|---|---|
| R1 | done | #162 merged. | — |
| R2 | done | #163 merged. | — |
| R3 | done (#164) | Velocity adapter and bridge plugin, Cloudflare DNS adapter, per-server `network` config, billing-aware scaling policy. | — |
| R4 | done (#164) | `Runtime.CheckNetwork` enforces §6; the step R6 planner must call it before accepting an operation. | R3 |
| N1 | done (#166) | One file per adapter: `internal/controllerruntime/adapter_<name>.go` registers each implementation; `adapters.d/` holds one profile per file. | — |
| R5 | done (#167 and the part 2 PR) | Rust consumer `conformance-rs/` passes all 17 initial-profile fixtures plus the network-profile requirements, with RFC 8785 JCS (numbers via `ryu-js`; Rust's own float formatting breaks ties differently from ECMAScript). | R2 |
| N2 | done (proxy outage notifications PR) | `internal/integrationmonitor` (outage tracker, probe loop, dispatcher with retries, controller log line) and `internal/notification` (webhook, SMTP, ntfy senders). Runtime files `adapter_webhook.go`, `adapter_smtp.go`, `adapter_ntfy.go`, and the `controller.monitoring.proxy` config. Protocol: integrations §7.1–7.2, `notification-*` schemas, two vectors. `integration.notification.send` is non-mutating by design (no durable operation), and delivery is at least once and in memory only. Not yet: monitoring integrations other than Velocity, and durable delivery. | N1 |
| N3 | done (SSH handoff PR) | SSH keys (`internal/sshkey`, `controller-setup ssh-key`), single-use host key handoff (`internal/hosthandoff`, protocol hosting §6.1 and vector), provision checks for the controller key and an SSH-only-from-controller firewall group, the bootstrap swap account, and `gsc-pilot handoff` (`internal/pilothandoff`) that sends the VM Wasabi credentials only to the pinned key. Needs a live Vultr test of cloud-init `ssh_keys` alongside a startup script. | N2 |
| R6 | done (management API routes PR) | Routes in `internal/managementapi/{servers,capabilities,problems}.go`; acceptance wiring and runner in `internal/controllerruntime/{api,runner}.go`; protocol authorization section 2.1 and vector `management-operations-submit-decision.json`. | — |
| R7 | Opus 5.5, high | Finish controller setup (`internal/controllersetup`): provider credential checks, local audit, rollback of all host changes. | R6 |
| R8 | Opus 5.5, high | Attach adopted VMs to a logical server (builds on `internal/pilotcapacity`). | R6 |
| R9 | Opus 5.5, high | Paper game adapter (install, readiness, save), using `protocol/schemas/game/`. | — |
| R10 | Opus 5.5, xhigh | Conformance evidence harness (`internal/conformance`) plus `internal/integrationpolicy`; fix what fails. | R3–R9 |
| R11 | Opus 5.5, high | Wire the proxy into workflows: register after readiness, transfer the waiting cohort in batches of at most 100, drain before stop. | R9, R10 |
| R12 | Opus 5.5, xhigh | Vultr hosting adapter, wired to `server.destroy`, plus a `gsc-pilot destroy` command. Destroy only with exact-target confirmation and only after a verified backup. | R11 |
| R13 | Opus 5.5, xhigh | Scaling scheduler applying `scalingpolicy` decisions (idle, then the minute 58–59 window, then stop/backup/destroy). | R12 |
| M1 | Opus 5.5, high | Migration target capacity: `controller.capacity.migration_targets` (`enabled`, `max_servers`) with per-server override; `internal/pilotcapacity` allows a third managed server only for a migration reservation bound to one live server; the `server.migrate` catalog entry and schema, and a capacity vector. | R6 |
| M2 | Opus 5.5, xhigh | `server.migrate` workflow: provision the migration target, restore the newest verified backup, readiness, proxy handover, verified final backup of the old server, then destroy it; any failure before the handover destroys the migration target. | M1, R11, R12 |
| M3 | Opus 5.5, xhigh | Manual scaling ([design](CORE_DESIGN.md#manual-scaling-planned)): `server.migrate` `trigger: manual`, `target_plan` and `timing` (`immediate`, `end_of_billing_period`, `next_start`); one pending change per server, cancel and replace; manual pauses auto-scaling until it completes, with a response note, log line and `scaling.auto_paused`/`auto_resumed` notifications; per-server auto-scaling override route `PUT /v1/servers/{server_id}/scaling` with a new `server.scaling.manage` permission and a CLI wrapper. Protocol pieces go in the `server.migrate` schema. | M1, M2, R13 |
| R14 | Sonnet 5.5, medium | Rename `0.21.0-draft` to `1.0.0` (about 1,270 strings in 400 files) and push the `protocol-v1.0.0` tag. The owner pre-approved that you push this tag after the release PR merges green. | everything |
| — | needs the owner | Live validation: Velocity plugin on a real proxy, Cloudflare on a staging zone, and Vultr partial-hour billing on an invoice. | R13 |

Keep new schemas in `0.21.0-draft` until R14.

## Details that matter

- **R2 notice gaps:** GitHub Actions in the workflows (`actions/checkout@v4`, `actions/setup-go@v5`, `actions/setup-java@v4`, `gradle/actions/setup-gradle@v4`), Gradle 8.14.3, the Temurin JDK used in CI, and libraries Velocity provides to the plugin at runtime: Gson 2.10.1 (Apache-2.0), toml4j 0.7.2 (MIT), SLF4J 2.0.17 and Guice 6.0.0 (their jars carry license files), and velocity-api 3.4.0. The velocity-api POM has no license element and its jar has no license file, so confirm the license in the PaperMC/Velocity repository before stating one.
- **R5 Rust tests:** an independent consumer that reads the same protocol package files and fixtures, never imports Go code or Go-generated expected output, runs in its own CI job, and is not a release gate. Start with the "initial profile" fixtures in `docs/PROTOCOL_INTEROPERABILITY_PLAN.md`. Add fixtures for network-profile requirements (integrations §6) and backend hostname uniqueness. Rust 1.97 and crates.io are available. Credit every crate under R2's rule.
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
- **Refactor safety nets that worked:**
  - Golden snapshots of a workflow's planned steps (for example `internal/workflow/testdata/server_backup_plans.golden.json`, with an `-update-…-golden` flag).
  - Per-step precondition tables pinning exact errors: `internal/workflow/server_provision_preconditions_test.go`.
  - Recording stores that log every write in order: `internal/workflow/engine_advance_test.go`.
  - For `cmd/protocol-check`, a byte-identical comparison of every evaluator's output across all 183 fixtures before and after.
- **Repowise:** health scores come from the repowise MCP tools (`get_health`, `get_risk`, `setup_status` for the indexed commit). The owner triggers re-indexes; scores are only real after a re-index of the current `main`. The `refactoring_targets` pagination in `get_health` repeats the same 25 entries, so rank functions with a local complexity count instead.

### PaperMC & systemd investigation (2026-11-16)

Completed systematic investigation of systemd integration patterns for PaperMC server deployments:

1. **Startup script flow** (`cmd/vultr-bootstrap/main.go`):
   - Creates Vultr startup scripts via `StartupScriptRequest{Name, Type, Script}`
   - Schema: ID (resource ref), Name (255 chars max), Type ("boot"|"pxe"), Script (base64-encoded, max 1 MiB UTF-8)
   - Validation rejects null bytes, non-UTF-8, and oversized scripts

2. **Systemd unit files** (`scripts/bootstrap/minecraft-debian.sh.tmpl`):
   - `minecraft-restore.service`: One-shot oneshot service to restore from Wasabi (RemainAfterExit=yes)
   - `minecraft-vanilla.service`: Main Minecraft Paper server, depends on restore completing
   - `mcrcon.socket` + `mcrcon.service`: Socket activation for secure localhost RCON (25577)
   - **No dedicated health check unit** - uses protocol-level unit inspection

3. **Volume mounting pattern**:
   - External Wasabi S3 storage, not block volumes
   - rclone configuration written via `internal/pilothandoff/handoff.go`
   - Restore script downloads from cloud; sync uploads to cloud on shutdown
   - Cold starts: restore from Wasabi → start server

4. **Health check implementation**:
   - Protocol schemas for unit inspection, start/stop, force-stop, logs-read, health-inspect, delete
   - No systemd health socket in bootstrap; uses `systemctl is-active` and journal logs
   - Schema validation ensures consistent state reporting across adapters

5. **Systemd credential integration**:
   - All secrets use `systemd-credential://NAME` reference scheme
   - Credentials stored encrypted in `/run/systemd/credentials/`
   - Scope: Vultr API tokens, Wasabi credentials, SSH private keys

Report saved to `INVESTIGATION_REPORT.md` with full code paths, example shapes, and behavioral patterns.
