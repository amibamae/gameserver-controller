# Development Roadmap

**Status:** Planning document. The phases are ordered to reduce production risk; dates are not commitments.

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
- [ ] Expand language-neutral behavior tests as contracts mature.
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
- Implement Wasabi's S3-compatible storage adapter.
- Implement the Minecraft Paper adapter.
- Add dry runs, exact-target confirmation, approvals, durable operation tracking, and audit.
- Validate provision, readiness, backup, restore, and destroy on isolated resources.
- Keep the management API documented and implementable by independent clients. A hosted panel remains out of scope.

## Phase 4: Production lifecycle orchestration

- Provision a disposable instance and restore its selected world.
- Add Velocity readiness and routing.
- Route lobby players only after the backend passes readiness checks.
- For games without a proxy, add the optional lobby wake and managed DNS handoff described in [the design](NO_PROXY_DNS_HANDOFF.md); players reconnect after readiness.
- Record provision-to-Paper-ready duration locally for estimates and an advisory progress bar; offer separate owner-enabled sharing of boot-time averages to the hosted lobby/DNS service, with extra rejoin buffer and no provider credentials exposed.
- Detect when the backend is idle.
- Stop Paper, back up and verify the world, then destroy the VM.
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

The Phase 1 package on main has met its exit criteria for protocol version `0.21.0-draft`. This untagged branch is development source, not an immutable interoperability snapshot. The pilot CLI can run the existing supervised backup and explicitly confirmed, capacity-backed Vultr VM creation under a source-controlled two-slot limit: one production VM and one shared staging/testing VM. A live production server may remain unmanaged and is excluded. The generic durable `server.destroy` workflow is implemented and fake-tested, but the pilot CLI has no destroy command and the workflow is not wired to a production hosting adapter. Explicit local capacity adoption now verifies exact Vultr metadata and commits ownership under the same capacity policy; logical-server attachment and production migration remain future work. The Go Vultr client can create, read, and update provider startup scripts, and the scrubbed Debian bootstrap template can be rendered and uploaded with the guarded Go command. Capacity-backed provisioning now requires an exact startup-script ID and SHA-256 digest, checks the provider's saved script before creation, and includes the ID in `script_id`. The provider API has no content-version pin, so a change after the check is still possible. Bootstrap execution, game readiness, end-to-end provision/backup/restore, Wasabi integration, and an isolated backup run remain future work. Running a supervised backup on an isolated test server and checking its artifacts/storage still needs an operator handoff.

The schema package covers hosting, storage, execution, game, and integration operations, with typed results for all seven standard server operations. The protocol specification and compatibility rules have been reviewed. Go CI checks schemas, references, examples, OpenAPI 3.2.1 structure, and published conformance fixtures.

Phase 2 now includes Go lifecycle/operation transition rules, a durable bbolt store, protocol-schema request validation, atomic acceptance with actor-scoped idempotency and per-server locks, an injected trusted-plan boundary that rejects client-supplied adapter steps, initial logical-server records committed atomically with `server.define`, rejection of later lifecycle operations when their logical server is missing, an atomic operation/server-state commit with durable transition evidence, metadata-only `server.define` and fake-adapter `server.start`, graceful `server.stop`, fake-tested generic durable `server.destroy`, `server.backup`, `server.provision`, and `server.restore` workflows, immutable step-input and completed-output snapshots, an embedded local schema registry, an in-process adapter registry with scripted fakes for all five categories, generic durable dispatch/reconciliation with restart recovery, fake workflow tests, side-effect-free validation for the initial local configuration, a bounded systemd credential accessor, durable controller API-key issuance/authentication primitives, and exact-scope HTTP authentication middleware. The management API has an authenticated operation-inspection handler and a disabled-by-default HTTPS runtime with TLS file checks and bounded server timeouts; only `GET /v1/operations/{operation_id}` is implemented. The remaining management routes and full runtime composition are future work. The backup workflow now plans offline capture, bounded storage upload and readback, game-specific validation, and lifecycle recovery; its capture output remains protected by the sealed-capture recovery path. The restore workflow verifies immutable backup objects, exact target identity, overwrite approval, runtime state, and post-restore checks; the server remains stopped by default. End-to-end provider validation and isolated backup artifact checks remain future work. The request validator still needs to be wired into the eventual controller/API composition. The `server.start`, `server.stop`, `server.destroy`, `server.provision`, and `server.restore` plans depend on injected trusted profile resolvers; proxy-drain profile resolution and approval-validated force stop remain future work. Credential-accessor integration, process transport, the remaining management routes, and production adapters also remain future work across Phases 2 and 3. The pilot capacity policy is now consumed by the Vultr create and explicit local capacity-adoption paths. Adoption does not yet attach a logical server to the production lifecycle.

Phase 1 means the reviewed specification package and Go validator/conformance suite are ready for implementation. Stable protocol 1.0 still requires the initial Go controller to pass the published behavioral suite. Other-language implementations are welcome, but they are not a release requirement.
