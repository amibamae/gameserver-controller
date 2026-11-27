# Competitive feature-gap analysis

Research date: 2026-11-27. GSC status verified against `main` at 737e8b6f.

This document compares GSC (amibamae/gameserver-controller, AGPL-3.0) with direct competitors, panels, network orchestrators, enterprise game-server platforms and reusable components. It informs scope decisions. It is not a commitment and not a release gate.

## 1. Purpose and method

Three research passes produced this document: direct competitors, panels and network orchestrators, and enterprise platforms and reusable components. Sources were GitHub metadata and source trees (`gh api`), READMEs, project docs and vendor pages (URLs in section 8).

Evidence terms used throughout:

| Term | Meaning |
|---|---|
| Yes | Confirmed in source or docs |
| Partial | Present in a reduced form (stated in the cell) |
| Not found | Not seen in the files and pages that were read. This is not proof of absence |
| Absent | Docs or design explicitly say it does not exist |

GSC status terms (exact): LOCALLY VERIFIED (unit tests); FAKE-TESTED (not runtime-connected); IMPLEMENTED BUT NOT RUNTIME-CONNECTED; registered in runtime with no live verification recorded; NOT IMPLEMENTED. No live staging or production verification of the full lifecycle exists.

Current GSC state, for reference:

| Area | Status |
|---|---|
| Durable operation store, idempotency, locks | LOCALLY VERIFIED (unit tests) |
| Controller runtime | Only `server.define` is registered |
| Provision, start, stop, backup, restore, destroy workflows | FAKE-TESTED, not runtime-connected |
| Velocity proxy adapter (register, drain, transfer), plugin routing on fresh ping | IMPLEMENTED BUT NOT RUNTIME-CONNECTED to workflows |
| Wasabi storage adapter (bounded put/get/head, SHA-256) | Registered in runtime; no live verification recorded |
| Vultr | Supervised pilot CLI: create (exact-ID reconcile after an ambiguous create), inspect, startup scripts. Start/stop and destroy NOT IMPLEMENTED. Firewall: verify only. Read-only runtime adapter in open PR #471 |
| Pinned SSH host-key handoff | IMPLEMENTED in the pilot |
| Readiness checks | Implemented, but can accept stale evidence (fix in progress) |
| Idle-shutdown scheduler | Implemented, not runtime-connected; does not block routing itself |
| Backups | Full archives with SHA-256 readback in the backup workflow (fake-tested). No incremental or deduplicated backups |
| Not implemented | Remote console, maintenance lockout, player-authorized start, spending limits or cost tracking, prewarming, minigame templates, Proxmox |

Caveats from the research:

- exaroton support pages returned 403 to the fetch. AutoStop/AutoStart facts come from search snippets, and the proxy support article (4404724229137) was not read. exaroton client-library licenses were not checked.
- CubeCoders AMP is closed source. Pricing was not confirmed from official pages, and its wake-on-connect behaviour is known only from forum reports. AMP docs were not fully readable.
- lazymc's README is inconsistent on supported Minecraft versions (1.20.3+ vs 1.6+).
- Wasabi per-TB price sources disagree (6.99 vs 7.99 USD); not confirmed on wasabi.com. Wasabi Object Lock behaviour with restic prune was not researched.
- restic `restore --verify` was not re-checked in this pass.
- Cold-start timings for Agones, GameLift and Edgegap are not documented in what was read. PlayFab GSDK details and GameLift queue latency placement were not re-fetched.
- SimpleCloud's proxy registration mechanics were not read. Calagopus and Pelican claims come from source trees, not run tests. Pelican is at a release candidate.
- Star counts and release dates are a snapshot from 2026-11-27.

## 2. Summary

### What differentiates GSC

| Property | What the research found elsewhere |
|---|---|
| Verified-restore-before-route (restore, verify, then route players) | outpost checks sizes; Vectolyn writes SHA-256 manifests; Pterodactyl stores SHA1; restic and kopia (via Calagopus) have tool-native checks. Restore-verify-before-trust was not found in any project read |
| Exact-instance identity | outpost has `adoptByName` after a create error; MineCloud's README warns of leaked spot requests. Neither is an exact-ID reconcile. GSC's pilot reconciles by exact ID after an ambiguous create |
| Durable ambiguous-operation recovery | Vectolyn recovers interrupted operations on start (local only). Others: not found. GSC's store, idempotency and locks are LOCALLY VERIFIED only |
| Provider-neutral disposable VMs | Provider neutrality found only for lazymc (any host) and Impulse (brokers). Every cloud-provisioning project read is tied to one provider |

Differentiation is design intent plus unit tests. No full-lifecycle live verification exists, so none of this is demonstrated in production, and no availability or durability guarantee is claimed.

### Where alternatives are simply better for some users

| User | Better choice | Why |
|---|---|---|
| One always-on host, wake on join | lazymc | One binary and TOML: wake-on-join, idle sleep, lockout, crash restart |
| Zero operations | exaroton | Hosted, prepaid credits, API, console, Velocity plugin |
| Always-on multi-tenant panel with users and eggs | Pterodactyl, Pelican, Calagopus | Sub-users, schedules, console, isolation, egg ecosystems |
| Owned-hardware Minecraft network with scaling groups | SimpleCloud (also SiriusCloud) | Groups, autoscale, persistent and dynamic services |
| Session-based games on Kubernetes | Agones | Fleets, allocation, warm pools, SDK health |

## 3. Feature-gap matrix

Legend: Y yes, P partial, N confirmed absent, ? not found. Columns: Lazy = lazymc, Exa = exaroton, Imp = Impulse, Out = outpost, Met = metio, Pter = Pterodactyl, SC = SimpleCloud, Ago = Agones. Bracketed keys are in section 8.

| Feature | GSC | Lazy | Exa | Imp | Out | Met | Pter | SC | Ago |
|---|---|---|---|---|---|---|---|---|---|
| Cold-start latency documented | ? no measurements | ? (local JVM start) [D9] | ? [D10] | ? [D1] | P "three-minute rebuild", no measurements [D3] | P "2-3 min" README [D2] | n/a | ? | ? [E1] |
| Scheduled prewarming | NOT IMPLEMENTED | P `freeze_process` pause [D9] | ? | ? | N (rebuild each session) [D3] | ? | ? | ? | Y Schedule and Buffer policies [E1] |
| Billing / session cost tracking | NOT IMPLEMENTED | n/a | Y credits, transactions API [D10] | ? | Y per-session rate, `status` [D3] | ? | ? | ? | n/a |
| Spending limits / alerts | NOT IMPLEMENTED | n/a | P prepaid credits [D10] | ? | ? (cost shown, not enforced) [D3] | ? | ? | ? | ? |
| Idle shutdown | Implemented, not runtime-connected; does not block routing itself | Y `sleep_after` [D9] | Y AutoStop (snippet) [D10] | Y `inactiveTimeout` [D1] | Y 10 + 20 min, `extend` [D3] | P schedule only found [D2] | N in core code [P1] | P scale-down; none for persistent [P7] | Y Webhook policy can scale to 0 [E1] |
| Incremental / dedup backups | N: full archives | ? | ? | ? | N full tar.gz [D3] | Y restic [D2] | N [P1] | N [P7] | N (no persistence model) [E1] |
| Backup restore and integrity verification | FAKE-TESTED: full archive, SHA-256 readback | ? | P restore offline only [D10] | ? | P size checks, no hash seen [D3] | P restore/clone; no `restic check` seen [D2] | P SHA1 checksum; restore Y [P1] | N [P7] | N |
| World ownership / lineage | Authoritative world in Wasabi (design) | n/a (local) | ? | ? | P world guard on pack change [D3] | ? | ? | N for backups; persistent servers keep world [P7] | N [E1] |
| Remote console | NOT IMPLEMENTED | ? | Y websocket [D10] | ? | Y RCON, denylist [D3] | ? | Y websocket [P1] | Y dashboard [P7] | n/a |
| Maintenance lockout | NOT IMPLEMENTED | Y `[lockout]` [D9] | ? | P `allowAutoStart=false` [D1] | ? | ? | P panel/node flag [P1] | ? | ? |
| Player-authorized controls | NOT IMPLEMENTED | P whitelist/ban [D9] | P permissions, shared credits [D10] | P `allowAutoStart` [D1] | P API token [D3] | P OAuth admins [D2] | Y sub-users, API keys [P1] | ? | ? |
| Deployment templates | NOT IMPLEMENTED (minigame templates) | ? | ? | ? | Y pack profiles [D3] | P profiles [D2] | Y eggs [P1] | P server groups [P7] | Y Fleet spec [E1] |
| Game installation | Provisioning via startup scripts (pilot) | N [D9] | Y | P Docker image [D1] | Y Modrinth, Fabric [D3] | Y Modrinth [D2] | Y egg install [P1] | Y software download [P7] | Y container image |
| External panel integration | NOT IMPLEMENTED | ? | P official API, Velocity plugin [D10] | Y third-party Crafty broker [D1] | ? | ? | n/a (is the panel) | ? | n/a |
| Provider-neutral execution | Vultr pilot only; Proxmox NOT IMPLEMENTED | Y any host [D9] | N | Y brokers [D1] | N Hetzner/R2/CF [D3] | N GCP [D2] | N Docker nodes [P1] | N own hosts [P7] | N Kubernetes |
| Geographic / latency placement | ? | n/a | P region transfer API [D10] | ? | P region arg, fallback sizes [D3] | P region field [D2] | ? | ? | ? (left to matchmaker) [E1] |
| Safe updates / rollback | ? | ? | ? | P reconcile [D1] | Y digest pin, health, rollback [D3] | ? | N [P1] | ? | ? |
| Crash recovery | Readiness can accept stale evidence (fix in progress) | Y `wake_on_crash` [D9] | ? | ? | P adopt, provision timeout [D3] | ? | Y `crash.go` [P1] | Y controller replaces [P7] | Y replace unhealthy [E1] |
| Resource isolation | Dedicated VM per server (design, unverified) | N | ? | ? | VM per session [D3] | VM per server [D2] | Y Docker limits [P1] | ? | Y pods |
| Infrastructure adoption (existing hosts) | NOT IMPLEMENTED | Y any host [D9] | n/a | Y brokers [D1] | N | N | Y registered nodes [P1] | Y serverhost agents [P7] | Y existing cluster |
| Install simplicity | Medium (judgement, not measured) | Very high [D9] | Highest | High, jar drop [D1] | Medium, 4 vendors [D3] | Low [D2] | Low: panel, DB, Redis, Docker [P1] | Medium [P7] | Low: Kubernetes [E1] |

Cell notes: GSC cells carry the exact status from section 1 where one applies. Calagopus has restic, kopia, ddup-bak, zfs, btrfs and PBS backup adapters [P4]; Pelican, FeatherPanel and Calagopus otherwise follow the Pterodactyl model. PlayFab documents sub-second allocation from a standby pool and Edgegap and PlayFab offer latency-based placement [E2][E3].

## 4. Per-category notes

### 4.1 Direct competitors

- **Problems solved:** start Minecraft servers on demand and stop them when idle, with cost control. Impulse (Velocity plugin with brokers), lazymc (one-host sleep proxy), outpost (Hetzner VPS plus R2), metio (GCP panel), MineCloud, minecraft-ondemand (AWS), dynamicserver (2016), minesible (demo).
- **Maturity and license:** Impulse AGPL-3.0, pre-1.0, last release 2025-03. lazymc GPL-3.0, active. metio Apache-2.0, 2 stars. outpost no license, 0 stars. MineCloud MIT, dormant since 2025. minecraft-ondemand Apache-2.0, unmaintained since 2023. dynamicserver MIT, abandoned. minesible no license.
- **Failure and recovery:** lazymc restarts on crash and force-kills on timeout. Impulse kicks the player on startup timeout and reconciles every 300 s. outpost adopts a server after a create error and confirms delete before OFFLINE. minecraft-ondemand's watchdog stops cleanly on a Spot notice. MineCloud documents leaked spot requests that keep billing. dynamicserver refuses to shut down on anomaly.
- **Less complexity for comparable function:** lazymc, on one always-on host, gives wake-on-join, idle sleep, lockout and crash restart with one binary. It does not remove the always-on host or give off-host durability. outpost is the closest concept (single user, hard-wired vendors).

### 4.2 Management panels

- **Problems solved:** multi-tenant hosting of long-lived servers with web UI, sub-users, schedules, console and backups. Pterodactyl, Pelican, Calagopus, FeatherPanel, PufferPanel, Crafty, AMP.
- **Maturity and license:** Pterodactyl MIT. Pelican panel AGPL-3.0, Wings MIT, release candidate. Calagopus MIT, young. FeatherPanel AGPL-3.0 (`develop` branch not for production). PufferPanel Apache-2.0. Crafty GPL-3.0-or-later. AMP proprietary.
- **Failure and recovery:** Wings `crash.go` auto-restarts within a timeout window. Idle shutdown is confirmed absent in Pterodactyl code and not found in Pelican. AMP sleep wake-on-connect is reported flaky in forum threads.
- **Less complexity for comparable function:** not for this workload. Panels assume persistent nodes plus a persistent panel and DB; none has a node-per-session lifecycle, and Wings cannot run without its Panel. PufferPanel (Go, single binary, OAuth2 scopes) is the only plausible embed and still adds a second control plane.

### 4.3 Network orchestrators

- **Problems solved:** scaling groups of Minecraft servers behind a proxy on owned machines. SimpleCloud, SiriusCloud, RediCloud, VectolynCloud.
- **Maturity and license:** SimpleCloud Apache-2.0 (current product; source tree not read). SiriusCloud no license, 1 star. RediCloud EUPL-1.2, dormant. Vectolyn GPL-3.0, pre-alpha, "not for production".
- **Failure and recovery:** SimpleCloud servers keep running through a controller outage, but no new starts and no crash replacement happen. SiriusCloud's node adopts running services after restart, and its rolling restarts drain players. Vectolyn recovers interrupted operations on start and writes SHA-256 manifests (local disk only).
- **Less complexity for comparable function:** yes for owned hardware. SimpleCloud adds capacity by installing a serverhost on another machine, does not provision cloud VMs (not found), and confirms no backups for persistent servers.

### 4.4 Enterprise platforms

- **Problems solved:** allocation, warm pools, health and regional placement for session-based multiplayer. Agones (Apache-2.0, Kubernetes), GameLift (AWS), PlayFab MPS (Azure), Edgegap (SaaS).
- **Maturity and license:** all actively maintained. Agones is open source; the other three are proprietary services.
- **Failure and recovery:** unhealthy servers are shut down and replaced (Agones after 3 failed pings by default; GameLift after 3 failed checks). Persistent world state is outside every model read: confirmed absent for Agones and GameLift, not found for PlayFab and Edgegap (Edgegap caps deployments at 24 h).
- **Less complexity for comparable function:** no. They need Kubernetes or a cloud account, and Minecraft/Paper has no GameLift SDK. Borrow concepts only (section 6).

### 4.5 Reusable components

- **Problems solved:** backup engines, backup coordination, server install and durable workflows. restic, docker-mc-backup, itzg/docker-minecraft-server, LinuxGSM, Temporal.
- **Maturity and license:** restic BSD-2-Clause, very active. docker-mc-backup MIT. itzg image Apache-2.0. LinuxGSM MIT. Temporal MIT (Go SDK MIT).
- **Failure and recovery:** restic locks are advisory; a crashed VM leaves a stale lock (stale after 30 min without refresh). docker-mc-backup auto-runs `restic unlock`, which is unsafe with two writers. LinuxGSM stops the server for backup by default and uses tmux.
- **Less complexity for comparable function:** restic yes (dedup, encryption and integrity checking in a subprocess). The docker-mc-backup sequence yes (reimplement; low). Temporal no: a second durable store beside bbolt and a new outage dependency. LinuxGSM no: tmux conflicts with systemd supervision.

## 5. Recommendations

Terms: native, optional adapter, delegate to existing software, defer, reject. Maintenance cost is an estimate, not measured. None of these is a commitment, and none is a release gate.

| Feature / component | User problem | Recommendation | Rationale | Safety implications | License | Maint. cost |
|---|---|---|---|---|---|---|
| restic behind the backup interface | Full archives grow; no dedup or incremental | Optional adapter | Dedup, encryption, `check --read-data-subset`; documents Wasabi S3 usage [E5]. Wasabi stays the primary store | Locks are advisory: never auto-unlock; a crashed VM leaves a stale lock. Wasabi 90-day minimum: prune and repack delete early and bill the remaining days. Password loss is data loss. GSC must still restore, verify and fence the exact world | BSD-2-Clause | Med |
| save-off / save-all flush / sync / save-on sequence | Consistent world snapshot | Native (reimplement) | Simple sequence, confirmed in docker-mc-backup source [E6] | `save-on` must run on every exit path. Do not copy its auto `restic unlock` | MIT source; reimplemented | Low |
| lazymc-style hold-with-fallback join UX | Timeout or kick during a cold start | Native (Velocity plugin UX) | Hold below the 30 s client timeout, then fall back to a message or lobby [D9]. VM cold start is minutes, so hold alone is not enough | Hold must not imply readiness; route only on verified readiness | GPL-3.0 (idea only; Rust) | Low |
| Impulse `allowAutoStart` / pin semantics | Maintenance lockout; admin override | Native, borrowing semantics | Per-server autostart flag, admin pin, custom messages [D1] | Check the lockout before every start path, including manual and scheduled | AGPL-3.0 (code reusable) | Low |
| Spending caps | Runaway VM cost | Native | No project read enforces one; exaroton prepaid credits are closest [D10]. Needs cost tracking first (NOT IMPLEMENTED) | Enforce before create, not only report. Fail closed on ambiguous cost | n/a | Med |
| Prewarm schedule | Multi-minute cold start | Defer | Agones Schedule/Buffer and PlayFab standby show the pattern [E1][E2]. Costs money; needs spend limits first | Warm instances obey the same identity and readiness rules; no hidden spend | n/a | Med |
| Optional PufferPanel adapter | Users who want a web UI | Optional adapter, later | Go, Apache-2.0, OAuth2 scopes, native environment [P5]. Optional integrations stay optional | Second state machine to reconcile with durable operations; must never be authoritative | Apache-2.0 | High |
| Embedding Wings/Panel | Reuse panel features | Reject | Wings needs a persistent Panel and DB; no node-per-session model [P1]. Panel backups are SHA1 tar to S3, weaker than restore-verify-route | New single point of failure and second state | MIT / AGPL-3.0 | High |
| Temporal | Durable workflows | Reject (revisit if multi-node) | Duplicates bbolt records; extra store and service [E7] | A Temporal outage would stop destroy and backup flows | MIT | High |
| Agones / GameLift for this workload | Allocation and fleets | Reject | Needs Kubernetes or a cloud account; no persistent-world model [E1][E8] | Lock-in or cluster ops | Apache-2.0 / proprietary | High |
| LinuxGSM | Install, update, monitor | Reject | tmux conflicts with systemd supervision and exact-instance identity; stops server on backup; no structured status found [E9] | Weaker supervision | MIT | Med |
| DNS-query triggers | Start without a plugin | Reject | minecraft-ondemand: any DNS lookup starts a server, no authorization [D7] | Unauthenticated start enables cost abuse | n/a | Low |
| Remote console; player-authorized start | Operator and player controls | Defer until the reference lifecycle works | Roadmap P1 lists both for evaluation; neither is a release gate. outpost's RCON denylist and MineCloud's Discord roles are references [D3][D4] | A console must refuse `stop` and `save-off` and use least-privilege authorization | n/a | Med |
| SFTP / dedicated file manager | File access | Reject (not a requirement) | Owner constraint; pinned SSH stays | n/a | n/a | n/a |

## 6. Ideas worth borrowing (concepts, not code)

| Idea | Source | Note |
|---|---|---|
| Backup before delete; never delete a world that could not be backed up | outpost [D3] | outpost has no license: ideas only. GSC's backup workflow is FAKE-TESTED |
| Trash retention (copy to trash first, delete second; 14-day window) | outpost [D3] | Same license note |
| Console command denylist (`stop`, `save-off`) | outpost [D3] | Same license note |
| Pinned-digest update, health check, rollback from a pre-update snapshot | outpost [D3] | Same license note. itzg's unpinned default drift also argues for pinning version and build [E10] |
| Retain a deleted server's backups for a configurable window | metio [D2] | Apache-2.0; code reusable if wanted |
| Hold that blocks scale-down (Agones `Reserve(seconds)`) | Agones [E1] | Candidate for idle shutdown during join or transfer |
| Separate "container started" from "ready for players" | Edgegap, Agones [E3][E1] | Edgegap Ready means only that the container started; the engine may need about a minute |
| Health thresholds (period, failure count) | Agones, GameLift [E1][E8] | Agones defaults 5 s period, 3 failures; GameLift 3 consecutive checks |
| Refuse to destroy on anomaly | dynamicserver [D5] | Matches GSC's ambiguous-operation stance |
| Hash-checked downloads; SHA-256 manifest with atomic `.partial` rename | SiriusCloud, Vectolyn [P8][P9] | SiriusCloud unlicensed; Vectolyn GPL-3.0 (concepts only) |
| Warning about leaked spot requests | MineCloud [D4] | Supports exact-instance reconcile |

## 7. Open questions and unverified items

- Real cold-start time for GSC: no live lifecycle verification exists. Competitor figures are only metio's and outpost's README claims.
- Whether Wasabi Object Lock or retention interacts badly with restic prune is unresearched. Wasabi pricing needs confirmation on wasabi.com.
- restic `restore --verify` existence and behaviour: not re-checked.
- exaroton: AutoStart on join inside the Velocity plugin, support-page details and library licenses are unconfirmed (pages blocked or unread).
- AMP: current prices, sleep and wake fidelity, backup features: unverified.
- SimpleCloud: proxy-registration mechanics and source tree not read.
- Impulse Kubernetes broker documentation link was empty; its behaviour is unverified.
- Whether any competitor verifies a restore before routing: not found in the sources read; that is not proof none does.
- Complexity and maintenance estimates for each recommended adapter are judgement. No spike has been done.

## 8. Sources

Direct (D): [D1] https://github.com/Arson-Club/Impulse, https://arson-club.github.io/Impulse/ . [D2] https://github.com/metio-plattform/metio . [D3] https://github.com/AutumnsGrove/outpost (renamed from GroveMC) . [D4] https://github.com/VeriorPies/MineCloud . [D5] https://github.com/1lann/dynamicserver . [D6] https://github.com/Dynamic-MC/Dynamic-MC (fork of D7) . [D7] https://github.com/doctorray117/minecraft-ondemand . [D8] https://github.com/spacelift-solutions/minesible . [D9] https://github.com/timvisee/lazymc . [D10] https://exaroton.com, https://developers.exaroton.com/, https://support.exaroton.com, https://hangar.papermc.io/exaroton/exaroton-proxy-plugin .

Panels and networks (P): [P1] https://github.com/pterodactyl/panel, https://github.com/pterodactyl/wings, https://pterodactyl.io . [P2] https://github.com/pelican-dev/panel, https://github.com/pelican-dev/wings, https://pelican.dev . [P3] https://cubecoders.com/AMP, https://github.com/CubeCoders/AMP/wiki, https://discourse.cubecoders.com . [P4] https://github.com/calagopus/panel, https://github.com/calagopus/wings, https://calagopus.com . [P5] https://github.com/pufferpanel/pufferpanel, https://docs.pufferpanel.com . [P6] https://craftycontrol.com, https://gitlab.com/crafty-controller/crafty-4, https://docs.craftycontrol.com . [P7] https://github.com/simplecloudapp/simplecloud . [P8] https://github.com/SiriusCloud-MC/SiriusCloud-MC . [P9] https://github.com/VectoDE/VectolynCloud (docs/cloud/backup.md) . [P10] https://github.com/RediCloud/cloud-v2, https://github.com/MythicalLTD/FeatherPanel .

Enterprise and components (E): [E1] https://agones.dev/site/docs/reference/gameserver/, /getting-started/create-fleet/, /guides/client-sdks/, /reference/fleetautoscaler/ . [E2] https://learn.microsoft.com/en-us/gaming/playfab/multiplayer/servers/ and https://developer.microsoft.com/en-us/games/products/playfab/pricing . [E3] https://docs.edgegap.com/docs/deployment/, https://edgegap.com/pricing . [E5] https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html, /045_working_with_repos.html, /100_references.html; Wasabi minimum storage duration at docs.wasabi.com . [E6] https://github.com/itzg/docker-mc-backup (scripts/opt/backup-loop.sh) . [E7] https://docs.temporal.io/self-hosted-guide/deployment, https://temporal.io/pricing . [E8] https://docs.aws.amazon.com/gamelift/latest/developerguide/gamelift-sdk-server-api.html, https://aws.amazon.com/gamelift/servers/pricing/ . [E9] https://github.com/GameServerManagers/LinuxGSM . [E10] https://docker-minecraft-server.readthedocs.io/en/latest/types-and-platforms/server-types/paper/ .

Licenses: GPL-3.0 and AGPL-3.0 code can be combined; MIT, Apache-2.0 and BSD-2 code can be incorporated with notices in THIRD_PARTY_NOTICES.md; unlicensed (outpost, SiriusCloud, minesible) and proprietary (AMP) code cannot be reused. Talking to any of these over an API has no license coupling.

This analysis informs the roadmap's "Competitive review and scope discipline" section ([ROADMAP.md#competitive-review-and-scope-discipline](ROADMAP.md#competitive-review-and-scope-discipline)) and is not a commitment.
