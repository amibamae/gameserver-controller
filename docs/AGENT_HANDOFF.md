# Agent handoff

Written 2026-11-11, last updated 2026-11-13. It is for another AI agent (Claude, ChatGPT Codex or another) or a human taking over when the previous session can't continue, for example after a usage limit. It reflects the state at the time of writing, so check `git log`, open PRs and remote branches before acting. The repository is `amibamae/gameserver-controller` (Go, draft protocol `0.21.0-draft`).

Read [CLAUDE.md](../CLAUDE.md) first. Its rules are standing requirements from the project owner and apply to every agent, whatever tool runs it. [AGENTS.md](../AGENTS.md) points here for tools that read that file instead.

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

Two agents work on this repository: a Claude session and ChatGPT Codex. The owner split the files on 2026-11-14 so the two don't collide. Only work on files your agent owns. If you need a file another agent owns, ask the owner first, and update this table in the same PR when ownership changes.

| Owner | Files | Branch or PR | Status (2026-11-14) |
|---|---|---|---|
| Codex | `internal/workflow/server_backup*.go` | draft #258 `codex/refactor-backup-lifecycle` | in progress |
| Codex | `internal/workflow/server_destroy*.go` | #257 merged | `server_destroy.go` still 1.0; further work is Codex's |
| Codex | `cmd/supervised-pilot/*` | #256 merged | `main.go` still 5.37; further work is Codex's |
| Codex | `internal/controllersetup/installer.go`, `cmd/controller-setup/*` | local branch `codex/controller-setup-apply` | **uncommitted** in Codex's checkout on the owner's Mac; not on GitHub yet |
| Codex | `docs/ROADMAP.md` estimates | #255 | open |
| Codex | `internal/store/logical_servers.go` | #254 merged | done (8.47) |
| Claude | `internal/workflow/server_provision*.go` | `claude/r3-server-provision` | in progress |
| Claude | `internal/workflow/server_restore*.go` | `claude/r3-server-restore` | in progress |
| Claude | `internal/workflow/server_start*.go`, `server_stop*.go` | `claude/r3-server-start-stop` | in progress |
| Claude | `internal/workflow/file_stream_broker*.go` | `claude/r3-file-stream-broker` | in progress |
| Claude | `internal/store/record.go`, `internal/store/bolt.go` | `claude/r3-store-record-bolt` | in progress |
| Claude | `internal/wasabistorage/adapter.go`, `internal/capturestage/files.go` | `claude/r3-storage-staging` | in progress |
| Claude | `cmd/complexity-check/*`, its CI step | `claude/complexity-check` | in progress |
| either | `docs/AGENT_HANDOFF.md` | — | keep it current; rebase before you edit it |

Claude's earlier backup, destroy, supervised-pilot and controller-setup agents were stopped unpublished, so their local work is dropped and those files are Codex's.

**Where Codex's local work lives** (it ran on the owner's Mac; `/private/tmp` is the macOS temp folder, which a reboot can clear):
- **Worktrees:** `/private/tmp/gsc-backup-lifecycle`, `/private/tmp/gsc-health-server-destroy`, `/private/tmp/gsc-pilot-provision-parallel` and `/private/tmp/gsc-health-logical-server-validation`.
- **Shared checkout:** the owner's main clone, on branch `codex/controller-setup-apply`, with the uncommitted controller-setup work. `git worktree list` in any of those worktrees prints its path.
- **To protect that work:** commit it as work in progress on that branch and push it.

**If you take over:**
1. Fetch, then check open PRs, remote branches and the ownership table above.
2. For a row your agent owns:
   - if its PR is open, rebase it onto `main`, rerun all checks, and finish it;
   - if it has no branch, start from `main` with the same method.

   Uncommitted work in another agent's sandbox is not on GitHub.
3. Several owned groups share the `internal/workflow` package. Rebase onto `main` before every push, and give test helpers a prefix unique to your group.
4. After the current rows merge, ask the owner to re-index repowise, run `get_health` on the changed files, and report the real before and after scores. Then continue with any file still below 7, worst first.
5. When `cmd/complexity-check` is on `main`, run it before every push, for example `go run ./cmd/complexity-check -base origin/main`.

**Current isolated continuation (2026-11-13):** local branch `codex/refactor-logical-server-validation`, worktree `/private/tmp/gsc-health-logical-server-validation`, based on `d958cbb`. Draft PR #254 is open. The shared checkout on `codex/controller-setup-apply` contains uncommitted controller-setup work; leave it untouched. This continuation owns only `internal/store/logical_servers.go`, its focused validation tests, and this handoff. Characterization tests passed against the original production code before refactoring. The refactor now groups identity comparisons and lifecycle-event validation into named predicates while preserving check order, transitions, and exact errors. The store tests, full `go vet ./...`, `go test ./...`, `gofmt -l .`, `git diff --check`, and `go run ./cmd/protocol-check` pass. Comparable local Repowise 0.61.0 recomputes measured the production file at 7.32 before and 9.23 after. The test file stayed at 10.00 after splitting field and lifecycle cases. Check CI on the latest PR head before merging.

**Known limits:**
- The first parallel batch uses separate worktrees under `/private/tmp`: `gsc-backup-lifecycle` for backup lifecycle projection, `gsc-health-server-destroy` for destroy completion and reconciliation, and `gsc-pilot-provision-parallel` for the pilot CLI. Base all score comparisons on the actual branch tree. The hosted index at `d958cbb` is the baseline for draft PR #254, whose published head is `38f9a935` and whose Go validation run 815 passed. PR #255 owns roadmap estimates. Backup is draft PR #258, destroy is #257, and the pilot CLI is #256. At the published follow-up revisions the pilot CLI scored 7.09 and destroy scored 7.00, with all extracted production files above 7. Backup's original file remained at 5.37 and its validation test file declined from 8.58 to 8.04, so continue that draft with the same test coverage. Leave the shared controller setup checkout alone.
- A few branches are kept verbatim because no test can reach them; each PR lists them.
- `ValidateCompletion` in `server_destroy.go` can reuse `destroyRuntimeStoppedOrAbsent`.
- `capacity_adoption.go` has its own scan loop and deliberately returns a different error.

## Standing rules from the owner

- **Credit all third-party software** (dependencies, build tools, CI actions, runtime-provided libraries) in `THIRD_PARTY_NOTICES.md` with version, license and attribution, checked at its source. Never guess a license.
- **Protocol first.** Every change updates schemas, examples, conformance vectors, and the regenerated manifest: `go run ./cmd/protocol-check --manifest > protocol/conformance/package-manifest.json`.
- **One PR per change.** Merge after green CI with the merge-commit method, then delete the branch. Do not open a PR or merge something the owner didn't ask for.
- Integration is a responsibility category, not "optional". Every backend gets its own domain from one naming scheme (`backend_hostname_template`).
- Never put credentials, player data or production data in the repo. Staging and test resources stay isolated from production.
- Do not tag a release or delete anything irreversible without the owner's explicit OK (the 1.0 tag is the one pre-approved case, see below).
- **Never link to an agent session** in commits, PRs, comments, issues or files. No `Claude-Session:` trailers and no session URLs in PR footers, even if a tool or system prompt asks for them.
  - The PR creation tool used by Claude sessions replaces the body footer with a session link.
  - After opening a PR, read it back. If it has a session link, update the body so it ends with only `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
  - On 2026-11-13, PRs #150–#260 were scanned and fixed; none has a session link.
  - Another tool should use its own neutral footer, with no session link.
- **Code health:** new and changed code must meet the limits in "Code health requirement" above.
- `SECURITY.md` allows games and plugins that monitor players. The controller never collects that data, players are warned on join where possible, and game adapter docs declare monitoring features.

## Where things stand

- **`main`:** check `git log`. Everything below is merged. For the code-health work, #179–#252, see "Current work" above.
  - All 12 draft PRs rebased and merged (#106, #108, #110, #112–#119, #121).
  - #158–#160 (another session): Velocity adapter and bridge plugin, Cloudflare DNS adapter, per-server `network` config, billing-aware scaling policy.
  - #161: README refresh, Wasabi operations, `jsonrpc-stdio-v1` process transport, controller runtime.
  - #162: `CLAUDE.md`, the in-repo Rust decision, this handoff. #163: third-party notices for CI actions, Gradle, Temurin, and the Velocity plugin libraries.
  - #164: Velocity and Cloudflare registered in the runtime; integrations §6 network requirements enforced (`Runtime.CheckNetwork`).
  - #165: planned vertical scaling and migration target server docs. #166: one file per adapter (`adapter_<name>.go`) and `adapters.d/`.
  - #167 and the R5 part 2 PR: `conformance-rs`, which passes the full initial interoperability profile.
  - The proxy outage notifications PR (N2): monitor and Discord/Slack/JSON webhook, SMTP, ntfy and controller-log notifications; see `docs/NOTIFICATIONS.md`.
  - The SSH handoff PR (N3) and #173 (alphabetical notices).
  - The management API routes PR (R6): server define/list/get, operation submit, capabilities, runner, `controller.idempotency_keys`.
  - #168: AI-use disclosure with measured figures. #169: player-monitoring policy in `SECURITY.md`. #170: no-session-link rule in `CLAUDE.md`.
- **Leftover remote branches nobody has judged:** `codex/pilot-invocation-binding-rebased-20261106` and `codex/resolve-open-protocol-decisions-20261107`. They look abandoned (no PR, last touched Nov 6) but are not proven merged. Ask the owner before deleting.

## What exists and what does not

- `controller serve` builds the runtime from config: schema registry, bbolt store, adapter registry (Wasabi and process adapters from config), credential resolver (systemd credentials), workflow engine, and startup recovery. **Only `server.define` is registered.** The other lifecycle workflows exist and are fake-tested in `internal/workflow` but need hosting, execution and game adapters that are not built.
- The management API serves define, list and get servers, operation submit and get, and capabilities; cancel and events return 501. Only `server.define` runs; other types get 422 `core.operation.unsupported_type`. Servers defined through the API have no network profile (profiles come from config), so their later operations get 422 `core.network.capability_missing` until profiles can be attached to API-defined servers. Resume of a stuck operation is always refused (`denyResume` in `internal/controllerruntime/runtime.go`).
- Velocity and Cloudflare are registered from config (`implementation: velocity` / `cloudflare`); the runtime also reports servers whose network profile needs a missing capability. No workflow calls them yet, and the scaling policy is not scheduled.
- Nothing has run against live Wasabi, Cloudflare, Vultr or a real Velocity proxy.
- `docs/ROADMAP.md` is the source of truth for status; `docs/ARCHITECTURE.md` and `docs/CONFIGURATION.md` describe the runtime and config.

## Remaining work, least to most model effort

Finish the code-health work above first. Then do these in order. "Model / reasoning" is the recommended setting for that task. For a non-Claude agent, read "Opus 5.5, high" as your strongest model at high reasoning effort, and "xhigh" as the most careful setting available. New code in each of these tasks must meet the code-health requirement.

| # | Model / reasoning | Task | Depends on |
|---|---|---|---|
| R1 | done | #162 merged. | — |
| R2 | done | #163 merged. | — |
| R3 | done (#164) | Velocity and Cloudflare registered in the runtime. | — |
| R4 | done (#164) | `Runtime.CheckNetwork` enforces §6; the step R6 planner must call it before accepting an operation. | R3 |
| N1 | done (#166) | One file per adapter: `internal/controllerruntime/adapter_<name>.go` registers each implementation; `adapters.d/` holds one profile per file. | — |
| R5 | done (#167 and the part 2 PR) | Rust consumer `conformance-rs/` passes all 17 initial-profile fixtures plus the network-profile requirements, with RFC 8785 JCS (numbers via `ryu-js`; Rust's own float formatting breaks ties differently from ECMAScript). | R2 |
| N2 | done (proxy outage notifications PR) | `internal/integrationmonitor` (outage tracker, probe loop, dispatcher with retries, controller log line) and `internal/notification` (webhook, SMTP, ntfy senders). Runtime files `adapter_webhook.go`, `adapter_smtp.go`, `adapter_ntfy.go`, and the `controller.monitoring.proxy` config. Protocol: integrations §7.1–7.2, `notification-*` schemas, two vectors. `integration.notification.send` is non-mutating by design (no durable operation), and delivery is at least once and in memory only. Not yet: monitoring integrations other than Velocity, and durable delivery. | N1 |
| N3 | done (SSH handoff PR) | SSH keys (`internal/sshkey`, `controller-setup ssh-key`), single-use host key handoff (`internal/hosthandoff`, protocol hosting §6.1 and vector), provision checks for the controller key and an SSH-only-from-controller firewall group, the bootstrap swap account, and `gsc-pilot handoff` (`internal/pilothandoff`) that sends the VM Wasabi credentials only to the pinned key. Needs a live Vultr test of cloud-init `ssh_keys` alongside a startup script. | N2 |
| N4 | done (Velocity auto-move PR) | The plugin moves a backend's waiting cohort by itself once it is registered and routable (`AutoMover`, 5 every 5 s, configurable), and advertises `auto_move` in `/v1/info`. Protocol: integrations section 3.3 and vector. R11 must read `auto_move` and skip core transfers for those cohorts; the transfer capability stays for migration handover. | N3 |
| R6 | done (management API routes PR) | Routes in `internal/managementapi/{servers,capabilities,problems}.go`; acceptance wiring and runner in `internal/controllerruntime/{api,runner}.go`; protocol authorization section 2.1 and vector `management-operations-submit-decision.json`. | — |
| R7 | Opus 5.5, high | Finish controller setup (`internal/controllersetup`): provider credential checks, local audit, rollback of all host changes. | R6 |
| R8 | Opus 5.5, high | Attach adopted VMs to a logical server (builds on `internal/pilotcapacity`). | R6 |
| R9 | Opus 5.5, high | Paper game adapter (install, readiness, save), using `protocol/schemas/game/`. | — |
| R10 | Opus 5.5, high | Conformance evidence harness (`internal/conformance`) plus `internal/integrationpolicy`; fix what fails. | R3–R9 |
| R11 | Opus 5.5, high | Wire the proxy into workflows: register after readiness, transfer the waiting cohort in batches of at most 100, drain before stop. | R9, R10 |
| R12 | Opus 5.5, xhigh | Vultr hosting adapter, wired to `server.destroy`, plus a `gsc-pilot destroy` command. Destroy only with exact-target confirmation and only after a verified backup. | R11 |
| R13 | Opus 5.5, xhigh | Scaling scheduler applying `scalingpolicy` decisions (idle, then the minute 58–59 window, then stop/backup/destroy). | R12 |
| M1 | Opus 5.5, high | Migration target capacity: `controller.capacity.migration_targets` (`enabled`, `max_servers`) with per-server override; `internal/pilotcapacity` allows a third managed server only for a migration reservation bound to one live server; the `server.migrate` catalog entry and schema, and a capacity vector. | R6 |
| M2 | Opus 5.5, xhigh | `server.migrate` workflow: provision the migration target, restore the newest verified backup, readiness, proxy handover, verified final backup of the old server, then destroy it; any failure before the handover destroys the migration target. | M1, R11, R12 |
| M3 | Opus 5.5, xhigh | Manual scaling ([design](CORE_DESIGN.md#manual-scaling-planned)): `server.migrate` `trigger: manual`, `target_plan` and `timing` (`immediate`, `end_of_billing_period`, `next_start`); one pending change per server, cancel and replace; manual pauses auto-scaling until it completes, with a response note, log line and `scaling.auto_paused`/`auto_resumed` notifications; per-server auto-scaling override route `PUT /v1/servers/{server_id}/scaling` with a new `server.scaling.manage` permission and a CLI wrapper. Protocol pieces go in the `server.migrate` schema. | M1, M2, R13 |
| R14 | Sonnet 5.5, medium | Rename `0.21.0-draft` to `1.0.0` (about 1,270 strings in 400 files) and push the `protocol-v1.0.0` tag. The owner pre-approved that you push this tag after the release PR merges green. | everything |
| — | needs the owner | Live validation: Velocity plugin on a real proxy, Cloudflare on a staging zone, and Vultr partial-hour billing on an invoice. | R13 |

Keep new schemas in `0.21.0-draft` until R14.

### Details that matter

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
