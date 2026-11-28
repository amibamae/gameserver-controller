# Development Roadmap

**Status:** Planning document. The phases are ordered to reduce production risk; dates are not commitments.

## Current focus: product readiness and competitive usefulness

**Priority set on 2026-11-27.** The primary product is a self-hosted, provider-independent controller for persistent game worlds on cost-aware, disposable infrastructure. Its value must be demonstrated by a working reference deployment, recoverable world data, measurable operational benefits and understandable code, rather than the number of schemas, adapters, tests or merged pull requests.

This section sets priorities across the existing phases; it does not replace their checklists, declare unfinished work complete, change protocol contracts or freeze other authorized development. The first reference deployment remains Go + bbolt + systemd, Vultr, Wasabi, Paper and Velocity. Advanced reliability and portability work remains on the roadmap, with optional integrations and independent clients rather than mandatory infrastructure.

### Owner use cases and personal production readiness

The first measure of success is safe, routine operation of the owner's production Minecraft network. Public adoption and broader competitive maturity follow rather than precede personal production readiness. Preserve the existing always-available proxy/lobby, Java and Bedrock compatibility, and configured voice chat. Introduce the controller through read-only inspection, isolated staging, supervised production operations, and individually verified automation. Keep a manual recovery and rollback path; automatic production destruction remains disabled pending separate authorization.

The owner's longer-term deployment goal is to consolidate the Minecraft network onto self-hosted Proxmox VE if the available hardware, connectivity, reliability and total operating cost can support the workload. This is a personal deployment preference, not a change to the product's supported hosting model: Vultr remains a first-class standalone hosting option for users without local server hardware, as well as for hybrid deployments and optional fallback. A cloud-only user must be able to provision, run, recover, back up and safely retire game servers without installing Proxmox, a LAN wake agent or any other on-premises infrastructure. Begin with isolated local minigames, then migrate persistent worlds and, where appropriate, the proxy, lobby and controller through individually approved and reversible cutovers. Keep Wasabi as the off-site authoritative backup and artifact store and retain Vultr as an optional fallback; moving compute locally must not make the only backup dependent on the same physical host. If the main Proxmox host is allowed to power down, the public entry point or wake endpoint, controller functions needed to request wake, and trusted wake mechanism must remain reachable independently (for example, on a small always-on local node or minimal external service). Full local consolidation and whole-host shutdown are alternative deployment choices when those functions cannot be separated. No automatic migration, failover or production shutdown is authorized by this roadmap.

Two workload classes must share the existing lifecycle and adapter contracts:

- **Persistent worlds:** survival, creative or other durable game state. Retain authoritative world identity and lineage, verified backups, configurable periodic backup and rollback retention, safe restoration and exact-target cleanup gates.
- **On-demand minigame sessions:** start a server or arena from a reviewed, versioned template when players request a game; use immutable maps and release artifacts stored in Wasabi, fresh readiness, bounded queues, explicit match completion and safe return to the lobby. Scale to zero when there are no active matches or waiting players, subject to configurable idle grace and capacity policies. Classify state explicitly: disposable arena state may be reset only under an approved lifecycle policy; player progress, statistics, rewards and other authoritative data must be durably persisted and verified before cleanup. Unknown or uncertain state fails closed. Do not bypass existing destruction safeguards; define any stateless cleanup policy protocol-first and independently test it before enabling it.

### P0: Finish and prove the reference lifecycle

- [ ] Connect the real hosting, storage, execution, game and integration adapters to the durable runtime and management operations for provision, start, stop, backup, restore and authorized destruction. Reuse existing implementations and in-progress work; distinguish runtime registration from actual workflow integration.
- [ ] Ensure durably accepted operations cannot be stranded by queue saturation or worker failure. Reconcile waiting and ambiguous outcomes automatically with bounded backoff, resource locking, restart recovery and observable errors. Preserve authorized operator intervention.
- [ ] Demonstrate the complete reference lifecycle on isolated resources: player joins the lobby; create the exact VM; secure bootstrap and pinned SSH handoff; restore the authoritative world; start Paper; establish fresh readiness and route players; recover a Paper crash on the same healthy VM; safely drain and stop; save and independently verify a fresh backup; destroy only the exact authorized staging VM; confirm provider absence; recreate and restore the latest world.
- [ ] Verify world integrity, inventories and progress after restoration; bind backup proof to the correct world, environment, instance and data generation. Prevent stale-world overwrite, unsafe concurrent writers and destructive cleanup after failed or uncertain verification. Keep production destruction disabled and preserve configurable backup/rollback retention.
- [ ] Exercise real failure paths in isolation: lost provider responses, duplicate requests, controller crashes around side effects, queue saturation, stale readiness, SSH and provider outages, corrupted or incomplete backups, disk exhaustion, players joining during shutdown and uncertain destruction. Test fakes must never be production adapters; use the existing [test harness rules](TEST_HARNESSES.md).
- [ ] Stabilize maintainability: audit duplicated or unnecessary abstractions, stale implementation reports and committed build outputs; prefer focused behavior-preserving changes with characterization tests and independent review. Preserve existing safety invariants, protocol-first discipline and code-health rules.
- [ ] Prepare a reproducible alpha candidate with installation instructions, versioned build artifacts, documented limitations and evidence of staging recovery. Independent review and authorized live verification are required before making production-readiness claims.

### P1: Make the controller usable

- [ ] Demonstrate an on-demand minigame reference path after the core lifecycle is usable: create or start from a versioned template, queue and route players only when ready, distinguish an empty lobby from an active or reserved match, persist required results, return players to the lobby, and safely clean up exact session resources. Test simultaneous requests, failed startup, controller recovery, mid-match disconnects, and players joining during drain. Bound concurrency, session creation rates, resource quotas and costs. Do not build a general-purpose matchmaking platform as a prerequisite.
- [ ] Complete the operator-facing management API, CLI setup, configuration validation, diagnostics and operation progress (including cold-start progress and actionable failure reasons).
- [ ] Evaluate maintenance lockout, scheduled backups and maintenance, scoped player/delegated administration and a secure remote console. Use Wasabi object storage for persistent world data, backups and versioned deployment artifacts; prefer verified, authorized object transfers and reviewed bootstrap or configuration workflows over a separate remote file manager. SFTP is not a product requirement. Retain pinned SSH for secure host handoff and remote execution. Keep an independent panel or Discord command client optional, with least-privilege authorization.
- [ ] Improve deployment templates, installation reproducibility, and documentation for an independent operator to deploy without AI assistance. Review privacy and consent for any player-related controls or metrics.

### P2: Demonstrate operational and economic value

- [ ] Prioritize Proxmox VE as an additional optional hosting/execution target for the owner's home infrastructure, alongside the existing cloud reference path. Reuse hosting and execution contracts: scoped API credentials, exact guest identity, template-based VM/container creation or start, readiness, graceful stop and reconciliation of asynchronous tasks. Do not require Proxmox for Vultr deployments or remove other provider candidates. Maintain and independently verify cloud-only, local-only and hybrid deployment paths through the same lifecycle contracts; provider choice must not weaken world protection or make either hosting adapter mandatory for the other.
- [ ] Support two separate opt-in idle policies: cloud billing-aware release of explicitly authorized disposable instances (a stopped Vultr instance remains billable; see [Vultr billing](https://docs.vultr.com/support/platform/billing/are-stopped-instances-still-billed-on-vultr)), and local guest shutdown to reclaim CPU/RAM. For dedicated, explicitly enrolled Proxmox nodes, evaluate optional whole-host power management with a separate always-available controller or trusted LAN wake agent and supported Wake-on-LAN or out-of-band management (see [Proxmox node management](https://pve.proxmox.com/pve-docs/pvenode.1.html)). Require verified absence of active matches, waiting demand, unrelated or protected guests, pending writes, backups and maintenance; protect against new demand racing shutdown; preserve manual override, secure connectivity, boot/reconciliation checks, minimum on/off times and safe failure. Never shut down a shared or general-purpose workstation automatically. Keep the host running if safety or wake capability is uncertain.
- [ ] Establish a Proxmox migration readiness gate using representative concurrent survival, lobby, proxy and minigame workloads: measure peak and sustained CPU (including per-core saturation), memory and swap, disk latency and free capacity, network throughput and player latency, Paper tick performance, backup/restore impact, thermal and power behavior, and recovery after host or internet failure. Reserve capacity for the hypervisor and recovery operations; prevent minigames from starving persistent worlds with quotas and admission control. Verify public connectivity for Java TCP, Bedrock UDP and configured voice chat, firewall isolation, home internet availability and exposure to denial-of-service, and assess UPS and off-site recovery needs. Compare measured total cost and service quality with the current cloud deployment. Proceed only when the owner approves the measured capacity and recovery results; retain a tested rollback and prevent simultaneous authoritative world writers during migration or fallback.
- [ ] Measure physical host power at idle, under load and after guest or host shutdown, plus wake latency and availability. Cloud compute costs include provisioning, backup, cleanup and billing increments; storage, proxy and networking may still incur charges. Report measured savings instead of promising zero cost or zero watts.
- [ ] Measure idle CPU and memory, database growth, boot/restore/readiness time, player wait, backup and verification duration, network/disk usage, recovery time and billed VM hours. Compare total operating cost against fixed-price hosting for representative workloads; distinguish measurements from estimates.
- [ ] Evaluate actual session-cost accounting, configurable spending limits and alerts, and billing-aware shutdown using documented provider behavior. Do not let cost optimization bypass data safety or active-player protections.
- [ ] Evaluate optional scheduled prewarming and idle grace policies to reduce cold starts; disclose the cost tradeoff and keep scale-to-zero available.
- [ ] Evaluate incremental/deduplicated backups and direct VM-to-object-storage transfers with independent verification. Prefer mature tools where compatible with world lineage, authorization and restore guarantees.

### P3: Expand through justified optional integrations

- Evaluate a second real hosting provider and game implementation after the reference path provides a credible baseline; use the existing adapter contracts rather than duplicating orchestration.
- Evaluate LinuxGSM, existing Minecraft container tooling, backup tools such as Restic, and infrastructure-as-code integrations where they reduce implementation and maintenance cost. Adoption requires license/security review and the same save, readiness, recovery and restore guarantees as native adapters.
- Keep VictoriaLogs, Homelable, external panels, Discord clients, container execution and other deployment choices optional. Failure or absence of an optional integration must not prevent core operation.
- Evaluate region/latency/cost-aware placement, vertical and horizontal scaling, warm pools and advanced availability profiles against measured demand. The existing scaling and reliability decisions remain in place; they are not prerequisites for a usable standalone controller. See [ADR 0006](decisions/0006-optional-ha-and-reliability-tiers.md). Availability and world-durability targets are objectives, not demonstrated guarantees.

### Competitive review and scope discipline

Maintain a source-backed feature-gap assessment as part of roadmap review. Useful references include [Impulse](https://github.com/Arson-Club/Impulse) and [lazymc](https://github.com/timvisee/lazymc) for wake-on-demand and player experience; [MineCloud](https://github.com/VeriorPies/MineCloud) and [exaroton](https://exaroton.com/) for on-demand hosting and delegated controls; [Pterodactyl](https://pterodactyl.io/) and [Pelican](https://pelican.dev/) for operator experience; [Agones](https://agones.dev/) for allocation and scaling; [LinuxGSM](https://linuxgsm.com/), [Restic](https://restic.net/) and [Temporal](https://temporal.io/) for reusable components and implementation patterns. Review other alternatives as relevant.

For each proposed feature, establish the user problem, current GSC status, evidence of a gap, safety and licensing implications, maintenance cost and a simpler alternative. Choose native implementation, optional adapter/client, external tooling, deferral or rejection. Do not treat feature parity as a release gate or infer that a competitor lacks undocumented safety mechanisms.

**Simple by default, sophisticated when necessary.** Treat unnecessary
complexity as a product and maintenance defect. Apply the following criteria
to new features and to reviews of existing abstractions:

- Identify a concrete user or operational need, measurable benefit and the simplest safe alternative. Prefer reuse, focused implementations or deferral over speculative infrastructure. Optionality alone is not justification to build a feature.
- Keep the standalone path first-class: no mandatory PostgreSQL, clustering, Kubernetes, external monitoring or additional hosting providers. Advanced availability, scaling, integrations and power-user options must be explicitly enabled, independently configurable where applicable, documented and testable.
- Verify baseline installation, startup, upgrades, core workflows and recovery without optional features or their dependencies. Test each enabled feature's behavior and relevant failure/isolation paths; absence or outage of a nonessential integration must not prevent normal core operation. An enabled feature may require its own dependencies and must fail safely.
- Evaluate complexity over the full lifecycle: configuration, code comprehension, resource use, security, testing, upgrades and ongoing maintenance. Document tradeoffs in the proposal or PR; remove redundant abstractions through behavior-preserving, reviewed changes rather than adding layers to satisfy metrics.
- Keep essential security, authorization, durable recovery, world integrity and exact-target destructive-action checks mandatory. Never make a safety invariant an optional power-user setting.

Do not make optional advanced capabilities prerequisites for personal
production readiness unless evidence establishes a real dependency. Preserve
the existing long-term reliability objectives as optional tiers or research,
not as promises or mandatory infrastructure.

### Parallel delivery and evidence

Use the existing [coordination workflow](../GSC_COORDINATION.md) and [CLI](COORDINATION_CLI.md), active task claims, file reservations, isolated branches/worktrees and handoffs. Check current ownership before editing, coordinate shared interface changes and reuse work already underway. This prioritization applies to remediation work; it does not revoke other agents' authorization, block unrelated changes, or permit overwriting their work. Continue independent tasks when a dependency is occupied or blocked.

Record implementation status separately from verification: planned, partial, implemented but not runtime-connected, fake-tested, locally verified, live staging verified or production verified, as applicable. A completion claim needs source and reproducible evidence; green CI alone is not live verification. Keep unverified criteria visible. Respect independent review, required checks and owner authorization for infrastructure spending, destructive actions and releases.

## Estimated timeline

These are deliberately conservative elapsed-calendar-time planning ranges measured from November 2026, not deadlines or estimates of continuous agent working hours. Plan for **24–36 months for the existing roadmap through Phase 7**, with a further **12 months of contingency** if necessary (up to 48 months as a planning allowance, not a completion promise). Milestones overlap and use the same starting point; do not add the ranges together. Progress may be faster, but no milestone is accepted before its evidence and safety gates are met.

| Milestone | Estimated calendar time from November 2026 | What needs to be proven |
| --- | --- | --- |
| Remediation foundation and supervised staging pilot | 3–6 months | Address critical runtime and maintainability defects; use the lower-cost isolated Vultr VM to verify Debian bootstrap, Paper readiness, and backup and restore of a synthetic disposable world through Wasabi. |
| End-to-end reference lifecycle and alpha candidate | 6–12 months | Connect the real adapters and durable workflows; demonstrate routing, same-instance recovery, safe drain, verified backup, authorized staging-only destruction and successful redeployment with preserved world state; provide reproducible installation and independent review. |
| First approved production deployment | 12–18 months | Complete sustained staging and fault-injection verification, demonstrate safe recovery and rollback, and independently verify a fresh backup of the live world before an explicitly approved production cutover. Production destruction remains disabled unless separately authorized by the owner. |
| Operator usability and measured economic value | 18–24 months | Complete prioritized management and diagnostic functionality; validate operating costs, startup and recovery performance, backup efficiency and justified optional improvements. |
| Full existing roadmap through Phase 7 | 24–36 months | Complete production orchestration and scaling, release rollback, automated staging updates, and validation with another hosting provider and game, subject to their individual acceptance gates. |
| Additional contingency, if needed | 36–48 months | Allow for extended testing, provider changes, security remediation, integration rework, resource or budget constraints, and long interruptions in development. Replan beyond this window if evidence or resources require it. |

Reserve substantial calendar slack for independent reviews, repeated failure and recovery testing, provider outages and API changes, CI delays, development interruptions, competing workstreams, maintainer availability and authorization for isolated live tests. Parallel development is useful but does not eliminate dependencies or guarantee throughput. Do not accelerate a release by weakening safety checks, reducing meaningful verification or treating fake-tested functionality as live-tested. An incomplete or blocked milestone stays incomplete even if its planning window has passed.

Review these ranges after the first successful staging restore, after each major milestone, and whenever availability or scope changes materially. Keep the current-focus priorities above aligned with the existing coordination workflow; unrelated authorized feature work may continue in parallel. Candidate competitive features are not all committed deliverables and must earn their place through evidence of user value and maintenance cost.

The optional hosted subdomain service, other-language contributions, and advanced availability research beyond the existing Phase 7 scope are outside this schedule. Stable protocol 1.0 follows the implementation evidence gate below. Availability and durability objectives require separate measured evidence and must not be inferred from calendar progress.

## Phase 0: Project foundation

- [x] Set the license and project policies.
- [x] Document the architecture and safety boundaries.
- [x] Publish contribution guidance and security reporting.
- [x] Disclose AI use and state that AI is never required for development, builds, runtime, or operations.
- [x] Document the reference deployment and production/staging boundaries.
- [x] Draft core design, adapter contracts, configuration, and security model.

## Phase 1: Language-neutral protocol

**Status: Exit criteria met for the current draft package.** The specification and Go validation suite are reviewed. The controller engine and fake-adapter harness belong to Phase 2; production adapters follow in Phase 3. Stable protocol 1.0 still needs implementation evidence.

- [x] Set protocol-first, language-neutral, transport-independent principles.
- [x] Separate adapter calls from the management API.
- [x] Require the controller to work without a web panel; clients may be independent.
- [x] Draft common identifiers, resources, timestamps, versions, capabilities, errors, request context, and secret references.
- [x] Draft durable operation acceptance, status transitions, idempotency, deadlines, retries, cancellation, reconciliation, recovery, progress, and partial outcomes.
- [x] Publish the operation catalog and parameter schemas for server definition, provision, start, stop, backup, restore, and destroy.
- [x] Review common types and close their open decisions.
- [x] Review operation semantics, transitions, cancellation, and recovery.
- [x] Draft lifecycle states, transitions, preconditions, postconditions, and failure behavior.
- [x] Review lifecycle semantics and recovery edge cases.
- [x] Draft hosting, storage, execution, game, and integration contracts.
- [x] Review adapter contracts and capability behavior.
- [x] Draft adapter descriptors, capabilities, and protocol negotiation.
- [x] Finalize compatibility negotiation, constraints, and permissions.
- [x] Publish the draft management API and OpenAPI description; a hosted panel is not required.
- [x] Review API resources, authentication, pagination, events, and compatibility.
- [x] Draft security, authorization, audit, privacy, consent, and environment-isolation rules.
- [x] Review threat model, permission scopes, approvals, and audit integrity/retention.
- [x] Select draft standards: JSON, JSON Schema 2020-12, JSON-RPC 2.0, OpenAPI 3.2.1, RFC 3339, and RFC 8785 where required.
- [x] Validate schemas, references, examples, OpenAPI structure, and conformance fixtures in Go CI; document optional stdio transport. Runtime controller and transport implementation remain in Phase 2; other-language implementations are welcome.
- [x] Publish schemas, examples, test vectors, and conformance requirements.
- [x] Publish operation results for provision, backup, restore, and destroy, plus Velocity transfer request/result schemas.
- [x] Add CI validation and an executable conformance-fixture runner.
- [x] Register hosting instance-creation input and output schemas.
- [x] Extend schemas across storage, execution, game, and integration operations; finish hosting coverage and typed results.
- [x] Draft compatibility rules for negotiation, schema identity, evolution, and in-flight operations.
- [x] Review the protocol and freeze compatibility rules.

Other-language implementations are welcome, but they are not Phase 1 exit criteria or a release gate.

- [ ] Release stable protocol 1.0 after implementation evidence.

**Exit criteria:** Reviewed protocol semantics and compatibility rules; remaining choices resolved or explicitly out of scope; schemas, examples, OpenAPI, and behavioral conformance validation pass in Go. Phase 1 does not require a controller engine or other-language implementations. Stable 1.0 remains subject to the implementation gate below.

## Phase 2: Go controller and fake-adapter harness

- Use Go for the initial controller and validation tooling.
- [x] Choose the first operation store: bbolt.
- [x] Implement durable operation acceptance with JCS request fingerprints, actor-scoped idempotency, and per-server locks.
- [x] Persist initial logical-server records atomically with accepted `server.define` operations.
- [x] Bundle protocol schemas and add a closed local schema registry.
- [x] Add the in-process adapter registry and scripted fakes for all five categories.
- [x] Validate the initial local v1 configuration without side effects; runtime loading and secret resolution remain future work.
- [x] Add a bounded accessor for systemd credentials; runtime and provider-adapter integration remain future work.
- [x] Persist immutable adapter-step inputs and schema-validated completed outputs with the exact capability identities.
- [x] Implement generic durable adapter dispatch, outcome persistence, reconciliation hooks, bounded safe retry, and restart recovery.
- [ ] Expand language-neutral behavior tests as contracts mature. In progress: the independent Rust consumer [`conformance-rs`](../conformance-rs/README.md) passes the full initial interoperability profile (17 fixtures, including RFC 8785 configure digests) plus the network-profile requirements, recomputes the package digests, and validates every schema and example. More fixtures follow as contracts mature.
- [x] Implement the metadata-only `server.define` and fake-adapter `server.start` and graceful `server.stop` workflows over the durable engine. Only `server.define` is registered in the normal controller runtime.
- [x] Implement the generic durable `server.destroy` workflow with fake-adapter tests.
- [x] Implement the Go `server.backup` workflow with bounded capture/storage streams, readback verification, game validation, and lifecycle recovery.
- [x] Implement fake-adapter-tested Go workflow definitions for `server.provision` and `server.restore`; production resolvers, adapters, and runtime composition remain Phase 3 work.
- [x] Add fake-adapter cases for workflow preconditions, definitive and ambiguous failures, timeout, bounded retry, accepted outcomes, and restart recovery.
- [x] Test compatibility, errors, idempotency, timeouts, and recovery without production services.
- [x] Establish build, test, dependency, and release practices that do not require AI.

**Exit criteria:** A minimal controller passes the agreed suite with fake adapters, no production credentials, and no live infrastructure.

## Phase 3: Local controller and safe manual lifecycle

- [x] Add a read-only Go setup planner for an existing Debian/systemd machine; it validates configuration and prints a redacted JSON plan without host changes or provider access.
- [x] Add local-only entry for configured provider credentials, encrypted as named systemd credentials with explicit replacement confirmation.
- [x] Add a local root-only API-key issuance and revocation CLI backed by a reusable authorized application service; the command requires an existing private store, exact scopes, explicit expiry, one-time token output, and exact revocation confirmation.
- [x] Add tested Go client operations to create, read, and update Vultr startup scripts without retrying ambiguous mutations.
- [x] Add a scrubbed Debian bootstrap template and an explicitly confirmed Go upload command; boot execution, readiness, and end-to-end validation remain open.
- [x] Bind capacity-backed VM creation to an exact Vultr startup-script ID and SHA-256 digest; verify the provider script before create and pass its ID in `script_id`. A provider-side content change between verification and processing remains possible and is documented.
- [x] Add an authenticated Go HTTP handler for operation inspection with exact server/environment scope checks and indistinguishable missing/inaccessible responses.
- [x] Add a disabled-by-default HTTPS runtime for operation inspection with fixed-audience bearer authentication, TLS 1.2+, private-key permission checks, and bounded HTTP timeouts.
- [ ] Complete [controller setup on an existing machine](CONTROLLER_SETUP.md) in Go. The CLI now installs a validated config, controller binary checked for Go build metadata and a runtime build identifier, hardened systemd unit, service account, and private state directories without changing service enablement or state; provider credential checks, local audit integration, rollback of all host changes, and end-to-end validation remain open.
- [x] Implement scoped, expiring, revocable controller API keys and enforce authentication and exact authorization for the current management route before enabling its remote listener. Remaining API routes are still future work.
- Preserve password-backed sessions and delegated controller access as the future panel's integration contract; panel implementation remains out of scope.
- Implement the core and application services against the protocol.
- Build a local CLI for inspection, config validation, plan/preview, and operation monitoring.
- Implement Vultr hosting and standard OS selection.
- [x] Add explicitly confirmed Vultr VM creation through an atomic, source-controlled two-slot capacity policy: one production VM and one shared staging/testing VM. Unmanaged live production stays outside managed capacity.
- Add production adoption through the same policy; the live production server may remain unmanaged.
- Validate bootstrap execution and game readiness on fresh, isolated instances; include startup-script ID and digest in instance records.
- [ ] Complete Wasabi's S3-compatible storage adapter integration. The Go `internal/wasabistorage` package implements bounded put/get/head, prefix-fenced list with bound continuation tokens, readback-verified copy, exact-version delete, and versioning and retention inspection, with profile and environment checks, systemd credential references, SHA-256 verification, and optional exact-version enforcement. Runtime/config registration, workflow authorization tests, and isolated Wasabi validation remain open; see [the adapter status](WASABI_STORAGE_ADAPTER.md).
- [x] Keep each adapter implementation in its own file (`internal/controllerruntime/adapter_<name>.go`, registered from `init()`), and allow one adapter profile per file in `adapters.d/`, so adapters are added or removed by adding or removing files.
- Implement the Minecraft Paper adapter.
- Add dry runs, exact-target confirmation, approvals, durable operation tracking, and audit.
- Validate provision, readiness, backup, restore, and destroy on isolated resources.
- Keep the management API documented and implementable by independent clients. A hosted panel remains out of scope.

## Phase 4: Production lifecycle orchestration

- Provision a disposable instance and restore its selected world.
- Add Velocity readiness and routing.
- Route lobby players only after the backend passes readiness checks.
- For games without a proxy, add the optional lobby wake and managed DNS handoff described in [the design](NO_PROXY_DNS_HANDOFF.md); players reconnect after readiness.
- [x] Register the Velocity and [Cloudflare DNS](CLOUDFLARE_DNS_ADAPTER.md) adapters in the controller runtime from configuration (`implementation: velocity` / `cloudflare`), with a startup bridge identity check and DNS records derived from network profiles.
- [x] Resolve each server's network profile and refuse planning when a required integration capability is not advertised by an adapter in its environment (`internal/controllerruntime`, protocol schema `integrations:network-profile`, conformance vector `network-profile-requirements.json`). The management API submit route runs this check before accepting an operation.
- [x] Verify new pilot VMs by a single-use SSH host key before sending them secrets (protocol hosting section 6.1): the controller generates SSH keys or imports them, checks that the Vultr firewall group admits SSH only from the controller, and `gsc-pilot handoff` swaps and pins the host key, then sends the VM's Wasabi credentials and starts the restore. Live validation on a Vultr VM remains.
- [x] Watch each proxy bridge and notify operators when it goes down and when it comes back (integrations section 7). The monitor uses a failure threshold and backoff, re-checks proxy identity after any failure, and notifies by Discord/Slack/JSON webhook, SMTP email, ntfy push, and the controller log. Events carry no player data; see [notifications](NOTIFICATIONS.md).
- [x] The Velocity plugin moves waiting players to their server by itself once the controller has registered it and it passes the health check: 5 every 5 seconds by default, skipping players who chose the lobby (integrations section 3.3, vector `proxy-cohort-automatic-move.json`).
- Use them in workflows: register backends after readiness, move the waiting cohort, drain before stop, and update direct-join and backend DNS records.
- Record provision-to-Paper-ready duration locally for estimates and an advisory progress bar; offer separate owner-enabled sharing of boot-time averages to the hosted lobby/DNS service, with extra rejoin buffer and no provider credentials exposed.
- Detect when the backend is idle. Treat `idle_shutdown` as a minimum and keep the server until its billing decision window (by default minutes 58 to 59 of each hour since creation) before backing up and destroying it; time added capacity the same way. The decision logic is in `internal/scalingpolicy`; `internal/idlescheduler` applies it on each tick and requests the stop, verified backup and destroy sequence through an injected interface. Wiring the scheduler into the controller runtime is future work.
- Stop Paper, back up and verify the world, then destroy the VM.
- Add vertical auto-scaling: move a server along an operator-defined plan ladder through a `server.migrate` operation to a migration target server, sized for forecast traffic. A scale-up may skip rungs when many players keep joining, a scale-down moves one rung at a time, and a hold time and margin keep the server from switching back and forth. During the migration handover, players move to the migration target, and the old live server is destroyed only after its final backup is verified. A migration target server is the only case allowed to exceed two managed servers, controlled by `migration_targets.enabled` (default true) and `migration_targets.max_servers` (default 1, 0 for no limit), controller-wide with a per-server override. Vertical scaling itself has a separate per-server switch, `scaling.vertical.enabled`. See [the design](CORE_DESIGN.md#vertical-auto-scaling-planned).
- Add manual scaling: an operator schedules a move to any plan with the timing `immediate`, `end_of_billing_period` or `next_start` (the next boot after everyone has left). A manual move pauses auto-scaling for that server until it completes, and the operator is told and can turn auto-scaling off or on per server. See [the design](CORE_DESIGN.md#manual-scaling-planned).
- Fail closed if save, upload, verification, authorization, or cleanup checks fail.
- Reconcile unclear provider results and recover interrupted work safely.

## Phase 5: Releases and rollback

- Define immutable software release manifests.
- Store Paper, plugin, and configuration artifacts separately from world backups.
- Add compatibility metadata and checksums.
- Implement release listing, status, rollback preview, and rollback.
- Record the bootstrap revision and configuration used for each instance; support a previously approved revision.
- Apply retention without deleting artifacts an active deployment or rollback needs.
- Test rollback with copied worlds and isolated instances.

## Phase 6: Staging and automated updates

- Discover updates and build candidate releases.
- Test bootstrap candidates on fresh, isolated instances from standard provider OS images.
- Create staging deployments from timestamped copies of production backups.
- Keep staging read-only against production backups.
- Automatically validate compatible routine updates.
- Require explicit approval for new Minecraft major versions and world migrations.
- Keep staging available until an operator accepts or rejects a candidate.

## Phase 7: Other providers, games, and contributors

- Add a second hosting provider, with OVH as a candidate, to test provider independence.
- Add another storage provider if useful.
- Publish adapter guidance and conformance tooling.
- Add another game after the game contract stabilizes.
- Welcome adapter and tooling contributions in other languages; do not require them for release.
- Review trust boundaries and compatibility for plugins and extensions.

## Controller resilience and self-maintenance (future)

**Status:** Planned, not required now. These items are for future implementation, after the single-controller lifecycle is complete and stable. None of them are release gates today; see the [architecture notes](ARCHITECTURE.md#controller-resilience-future).

- Run two or three controllers for peer failover: the controllers elect who takes over, and exactly one is active at a time. Shared durable store, per-server locks, idempotency, and audit must stay authoritative across replicas so a takeover never double-dispatches an operation. Optional for operators who want the highest uptime.
- Self-healing controller: detect an unsafe state and spin up a replacement controller on the same machine to take over its duties, reducing the need for multiple replicas. Must be an audited lifecycle operation that verifies the exact target and environment, never bypasses lifecycle policy or authorization, and starts the replacement from the same durable state.
- Controller auto-update: update the controller itself automatically, respecting release discipline (immutable versioned releases, validation before promotion, compatibility and world-format checks, operator approval for anything affecting a running server), and coordinate with self-healing and failover so an update never drops ownership of live state. Only in scope once the rest of the controller lifecycle is implemented.

## Future management clients

Web, desktop, and mobile clients may develop in parallel; the controller does not depend on them.

- Maintain a documented, versioned management API.
- Let third parties build clients without changing the core.
- Keep authentication, authorization, approvals, audit, and safety rules in the controller.
- Do not require maintainers to host a web panel.
- Do not expose provider credentials or let clients bypass lifecycle policy.

An optional hosted offering could expose the same API to customer applications
and assign join subdomains under a service-owned domain. This is a future
deployment option, not part of the local pilot or a commitment to run a web
panel. The current single-controller state store must not be exposed as a
shared customer service. Tenant-scoped API authorization, persisted state and
idempotency, locks, credentials, DNS record ownership, quotas, rate limits,
audit, and operational isolation are prerequisites; see the [architecture
notes](ARCHITECTURE.md#optional-hosted-control-plane). Treat this as a separate
multi-tenant security and operations milestone after the single-tenant
lifecycle works.

## What counts as complete

A phase needs more than code. Include documentation, configuration examples, automated and failure-path tests, security and permission review, and isolated validation. Keep secrets and production data out of the repository. Require operator approval for production-impacting changes.

Protocol completion also requires language-neutral schemas and examples, compatibility rules, and a passing suite for the initial Go implementation. Other implementations may run that suite as optional contributions.

## Reference deployment

The first end-to-end target is Vultr, Wasabi, Paper, and Velocity on Debian. Keep existing infrastructure running while developing and testing the controller separately. Do not replace production scripts until equivalent behavior is implemented, tested, and deliberately migrated.

## Current state and next work

The Phase 1 package on main has met its exit criteria for protocol version `0.21.0-draft`. This untagged branch is development source, not an immutable interoperability snapshot. The pilot CLI can run the existing supervised backup and explicitly confirmed, capacity-backed Vultr VM creation under a source-controlled two-slot limit: one production VM and one shared staging/testing VM. A live production server may remain unmanaged and is excluded. The generic durable `server.destroy` workflow is implemented and fake-tested, but the pilot CLI has no destroy command and the workflow is not wired to a production hosting adapter. Explicit local capacity adoption now verifies exact Vultr metadata and commits ownership under the same capacity policy; logical-server attachment and production migration remain future work. The Go Vultr client can create, read, and update provider startup scripts, and the scrubbed Debian bootstrap template can be rendered and uploaded with the guarded Go command. Capacity-backed provisioning now requires an exact startup-script ID and SHA-256 digest, checks the provider's saved script before creation, and includes the ID in `script_id`. The provider API has no content-version pin, so a change after the check is still possible. The initial Go Wasabi package implements bounded put/get/head but has no runtime/config wiring or live Wasabi validation. The initial Go Velocity integration adapter (`internal/velocityproxy`) enforces the proxy backend, cohort, session-inspect, and player-transfer contract. It talks to the [Velocity bridge plugin](../plugins/velocity) over the versioned [proxy bridge API](PROXY_BRIDGE_API.md), registers backends at the instance's current Vultr IP or a managed hostname, and treats a backend as not routable when that address changes. Runtime/config wiring and live validation remain future work. The protocol now defines DNS record capabilities, and the initial Cloudflare DNS adapter (`internal/cloudflaredns`) implements them against a fake Cloudflare API; runtime wiring and live validation remain future work. Bootstrap execution, game readiness, end-to-end provision/backup/restore, complete Wasabi integration, and an isolated backup run remain future work. Running a supervised backup on an isolated test server and checking its artifacts/storage still needs an operator handoff.

The schema package covers hosting, storage, execution, game, and integration operations, with typed results for all seven standard server operations. The protocol specification and compatibility rules have been reviewed. Go CI checks schemas, references, examples, OpenAPI 3.2.1 structure, and published conformance fixtures.

Phase 2 now includes Go lifecycle/operation transition rules, a durable bbolt store, protocol-schema request validation, atomic acceptance with actor-scoped idempotency and per-server locks, an injected trusted-plan boundary that rejects client-supplied adapter steps, initial logical-server records committed atomically with `server.define`, rejection of later lifecycle operations when their logical server is missing, an atomic operation/server-state commit with durable transition evidence, metadata-only `server.define` and fake-adapter `server.start`, graceful `server.stop`, fake-tested generic durable `server.destroy`, `server.backup`, `server.provision`, and `server.restore` workflows, immutable step-input and completed-output snapshots, an embedded local schema registry, an in-process adapter registry with scripted fakes for all five categories, generic durable dispatch/reconciliation with restart recovery, fake workflow tests, side-effect-free validation for the initial local configuration, a bounded systemd credential accessor, durable controller API-key issuance/authentication primitives, and exact-scope HTTP authentication middleware. The management API runs over a disabled-by-default HTTPS runtime with TLS file checks and bounded server timeouts. It serves `POST /v1/servers` (define, with the `server.define` permission and server IDs unique across environments), `GET /v1/servers` (exact-scope filtering and HMAC-bound cursors), `GET /v1/servers/{server_id}`, `POST /v1/servers/{server_id}/operations`, `GET /v1/operations/{operation_id}` and `GET /v1/capabilities`; cancel and the event stream return 501. Submissions go through the acceptance service (schema validation, exact-scope authorization, the integrations section 6 network check, per-server locks and keyed idempotency from `controller.idempotency_keys`), and a bounded background runner executes accepted and recovered operations. The decision order is published as the `management.operations.submit-decision` conformance vector. `internal/controllerruntime` composes the schema registry, bbolt store, adapter registry (with configured Wasabi and `jsonrpc-stdio-v1` process adapters), credential resolver, and workflow engine for `controller serve`, and runs startup recovery; only `server.define` is registered, because the other lifecycle workflows need hosting, execution, and game adapters that do not exist yet. The backup workflow now plans offline capture, bounded storage upload and readback, game-specific validation, and lifecycle recovery; its capture output remains protected by the sealed-capture recovery path. The restore workflow verifies immutable backup objects, exact target identity, overwrite approval, runtime state, and post-restore checks; the server remains stopped by default. End-to-end provider validation and isolated backup artifact checks remain future work. The `server.start`, `server.stop`, `server.destroy`, `server.provision`, and `server.restore` plans depend on injected trusted profile resolvers; proxy-drain profile resolution and approval-validated force stop remain future work. Credential-accessor integration, runtime use of the Go `jsonrpc-stdio-v1` process transport (implemented in `internal/adapterprocess`, without the local stream profile), and production adapters also remain future work across Phases 2 and 3. The pilot capacity policy is now consumed by the Vultr create and explicit local capacity-adoption paths. Adoption does not yet attach a logical server to the production lifecycle.

Phase 1 means the reviewed specification package and Go validator/conformance suite are ready for implementation. Stable protocol 1.0 still requires the initial Go controller to pass the published behavioral suite. Other-language implementations are welcome, but they are not a release requirement.
