# Development Roadmap

**Status:** Planning document. The phases are ordered to reduce production risk; dates are not commitments.

## Estimated timeline

These ranges are elapsed calendar time from November 2026. Plan for **12–18 months for the full roadmap through Phase 7**, with room to extend the schedule if usage limits or live testing cause longer pauses. The milestones overlap, so the ranges are measured from the same starting point.

| Milestone | Estimated calendar time | What needs to be proven |
| --- | --- | --- |
| Supervised staging pilot | 1–3 months | Use the lower-cost Vultr VM to verify Debian bootstrap, Paper readiness, and a backup and restore of a disposable world through Wasabi. |
| First production deployment | 4–8 months | Wire the required lifecycle workflows to real adapters, verify recovery and cleanup on staging, then restore a fresh backup of the live world onto the larger production VM and verify it before an approved cutover. |
| Full roadmap through Phase 7 | 12–18 months | Complete production orchestration and scaling, release rollback, automated staging updates, and validation with another hosting provider and game. |

Agents work in sessions when usage is available. Several agents can make progress in parallel, but usage limits can pause them for days or weeks, and some work has to wait for another change, review, or CI run. The ranges include time for those gaps, fixes after staging tests, and operator availability for live checks. Revisit the estimates after the first successful staging restore and whenever the pace of work changes materially.

The optional hosted subdomain service and other-language contributions are outside this schedule. Stable protocol 1.0 follows the implementation evidence gate below.

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
- [x] Implement the metadata-only `server.define` and fake-adapter `server.start` and graceful `server.stop` workflows over the durable engine.
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
- Detect when the backend is idle. Treat `idle_shutdown` as a minimum and keep the server until its billing decision window (by default minutes 58 to 59 of each hour since creation) before backing up and destroying it; time added capacity the same way. The decision logic is in `internal/scalingpolicy`; the scheduler that applies it is future work.
- Stop Paper, back up and verify the world, then destroy the VM.
- Add vertical auto-scaling: move a server one plan up or down an operator-defined plan ladder at a time, through a `server.migrate` operation to a migration target server. During the migration handover, players move to the migration target, and the old live server is destroyed only after its final backup is verified. A migration target server is the only case allowed to exceed two managed servers, controlled by `migration_targets.enabled` (default true) and `migration_targets.max_servers` (default 1, 0 for no limit), controller-wide with a per-server override. Vertical scaling itself has a separate per-server switch, `scaling.vertical.enabled`. See [the design](CORE_DESIGN.md#vertical-auto-scaling-planned).
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
