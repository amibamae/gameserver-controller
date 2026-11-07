# Development Roadmap

**Status:** Planning roadmap. The phases reduce production risk; dates are not commitments.

## Phase 0: Project foundation

- [x] Set the license and project policies.
- [x] Document architecture direction and safety boundaries.
- [x] Publish contribution guidance and security reporting.
- [x] Disclose AI use and commit that AI will never be required for development, build, runtime, or operations.
- [x] Document the reference deployment and production/staging boundaries.
- [x] Draft the core design, adapter contracts, configuration, and security model.

## Phase 1: Language-neutral protocol

**Status: Phase 1 exit criteria met for the current draft package.** The reviewed specification and Go validation suite are complete; the controller engine and fake-adapter harness are Phase 2, and production adapters follow in Phase 3. Stable protocol 1.0 remains gated on implementation evidence.

- [x] Define protocol-first, language-neutral, transport-independent principles.
- [x] Separate adapter calls from the management API.
- [x] Require the controller to work without a web panel; future clients may be independent.
- [x] Draft common identifiers, resources, timestamps, versions, capabilities, errors, request context, and secret references.
- [x] Draft durable operation acceptance, status transitions, idempotency, deadlines, retries, cancellation, reconciliation, recovery, progress, and partial outcomes.
- [x] Publish the initial operation catalog and parameter schemas for server definition, provision, start, stop, backup, restore, and destroy.
- [x] Review common types and resolve open decisions.
- [x] Review operation semantics, status transitions, cancellation, and recovery.
- [x] Draft lifecycle states, transitions, preconditions, postconditions, and failure behavior.
- [x] Review lifecycle semantics and recovery edge cases.
- [x] Draft hosting, storage, execution, game, and integration adapter contracts.
- [x] Review adapter contracts and capability behavior.
- [x] Draft adapter descriptors, capabilities, and protocol negotiation.
- [x] Finalize compatibility negotiation, constraints, and permissions.
- [x] Publish a draft management API contract and OpenAPI definition; no hosted panel is required.
- [x] Review API resources, authentication, pagination, events, and compatibility.
- [x] Draft protocol security, authorization, audit, privacy, consent, and environment isolation.
- [x] Review threat model, permission scopes, approvals, and audit integrity/retention.
- [x] Select draft standards: JSON, JSON Schema 2020-12, JSON-RPC 2.0, OpenAPI 3.2.1, RFC 3339, and RFC 8785 where required.
- [x] Validate schemas, references, examples, OpenAPI structure, and conformance vectors in Go; document the optional stdio transport. Runtime controller/transport implementation is Phase 2; optional implementations in other languages are welcome.
- [x] Publish initial schemas, examples, test vectors, and conformance requirements.
- [x] Publish operation results for provision, backup, restore, and destroy, plus Velocity transfer request/result schemas.
- [x] Add CI validation and an executable runner for the behavioral conformance suite.
- [x] Register input and output schemas for hosting instance creation.
- [x] Extend schemas across storage, execution, game, and integration operations; complete hosting coverage and typed results.
- [x] Draft compatibility rules for negotiation, schema identity, evolution, and in-flight operations.
- [x] Review the full protocol and freeze compatibility rules.
Other-language implementations are welcome contributions, but they are not part of the Phase 1 exit criteria or release gate.
- [ ] Release stable protocol 1.0 only after interoperability evidence.

**Exit criteria:** The protocol package has reviewed semantics and compatibility rules; every remaining design choice is resolved or explicitly out of scope; schemas, examples, OpenAPI, and behavioral conformance validation pass in Go. Phase 1 does not require the controller engine or alternate-language implementations. A stable 1.0 release remains subject to its interoperability gate below.

## Phase 2: Implement the Go controller and build a fake-adapter harness

- Use Go for the initial controller implementation and its validation tooling.
- [x] Choose the initial operation persistence backend (bbolt).
- [x] Implement durable operation acceptance with JCS request fingerprints, actor-scoped idempotency, and per-server locks.
- [x] Bundle the protocol schemas and add a closed local schema registry for runtime validation.
- [x] Add the Go in-process adapter registry and scripted fake adapters for all five categories.
- [x] Add side-effect-free configuration validation for the initial local v1 shape; runtime loading and secret resolution remain future work.
- [x] Persist immutable adapter-step inputs and schema-validated completed outputs with their exact capability identities.
- Expand language-neutral behavioral tests as contracts mature.
- Implement lifecycle workflows and durable step dispatch/reconciliation against the adapter registry.
- Expand fake-adapter scenarios to cover workflow preconditions, failures, timeout, and recovery.
- Test compatibility, errors, idempotency, timeouts, and recovery without production services.
- Establish build, test, dependency, and release practices that do not require AI.

**Exit criteria:** A minimal controller passes the agreed conformance suite with fake adapters, no production credentials, and no live infrastructure.

## Phase 3: Local controller and safe manual lifecycle

- Implement the core and application services against the protocol.
- Build a local CLI for inspection, config validation, plan/preview, and operation monitoring.
- Implement the Vultr hosting adapter and standard OS selection.
- Add Vultr API create/adopt through an atomic source-controlled capacity policy, initially capped at one controller-owned VM; the live production server may remain unmanaged.
- Apply versioned bootstrap scripts to new instances.
- Implement Wasabi’s S3-compatible storage adapter.
- Implement the Minecraft Paper adapter.
- Add dry runs, exact target confirmation, approvals, durable operation tracking, and audit.
- Validate provision, readiness, backup, restore, and destroy against isolated resources.
- Keep the management API documented and independently implementable. A hosted panel remains out of scope.

## Phase 4: Production lifecycle orchestration

- Provision a disposable instance and restore its selected world.
- Add Velocity readiness and routing integration.
- Route lobby players only after the backend passes readiness checks.
- Detect when the backend is idle.
- Stop Paper, back up and verify the world, then destroy the VM.
- Fail closed when save, upload, verification, authorization, or cleanup checks fail.
- Reconcile uncertain provider results and recover interrupted work safely.

## Phase 5: Releases and rollback

- Define immutable software release manifests.
- Store Paper, plugin, and configuration artifacts separately from world backups.
- Add compatibility metadata and checksums.
- Implement release listing, status, rollback preview, and rollback.
- Record the bootstrap revision and configuration used for each instance; support a previously approved revision.
- Apply retention without deleting artifacts an active deployment or rollback needs.
- Test rollback with copied worlds and isolated instances.

## Phase 6: Staging and automated updates

- Discover updates and create candidate releases.
- Test bootstrap candidates on fresh, isolated instances from standard provider OS offerings.
- Create staging deployments from timestamped production backup copies.
- Keep staging read-only against production backups.
- Automatically validate compatible routine updates.
- Require explicit approval for new Minecraft major versions and world migrations.
- Keep staging available until someone accepts or rejects the candidate.

## Phase 7: Other providers, games, and contributors

- Add a second hosting provider, with OVH as a candidate, to test provider independence.
- Add another storage provider if useful.
- Publish adapter development guidance and conformance tooling.
- Add another game after the game contract stabilizes.
- Welcome adapter and tooling contributions in other languages; do not require alternate-language implementations for release.
- Review trust boundaries and compatibility for plugins and extensions.

## Future management clients

Web, desktop, and mobile clients are an optional parallel effort, not a requirement for the controller.

- Maintain a documented, versioned management API.
- Let third parties build clients without changing the core.
- Keep authentication, authorization, approvals, audit, and safety checks in the controller.
- Do not require maintainers to host a web panel.
- Do not expose provider credentials or let clients bypass lifecycle policy.

## What counts as complete

Code alone does not complete a phase. Include documentation, configuration examples, automated and failure-path tests, security and permission review, and isolated validation. Keep secrets and production data out of the repository. Require operator approval for production-impacting changes.

Protocol completion also requires language-neutral schemas and examples, compatibility rules, and a passing conformance suite for the initial Go implementation. Other implementations may run the same suite as optional contributions.

## Reference deployment

The first end-to-end target is Vultr, Wasabi, Paper, and Velocity on Debian. Existing infrastructure stays in service while the controller is developed and tested separately. Do not replace production scripts until equivalent behavior is implemented, tested, and deliberately migrated.

## Current state and next work

The Phase 1 protocol package has met its exit criteria on main at version 0.21.0-draft. This untagged branch is development source, not an immutable interoperability snapshot. The supervised backup CLI is code-complete within its limited wrapper scope; it does not create VMs. The pilot capacity policy starts at one controller-owned VM and is changeable in the source-controlled policy file before the Vultr create/adopt path is rebuilt; it remains declarative until that path exists. The live production server can remain unmanaged. A supervised backup against an isolated test server plus artifact/storage verification remains an operator handoff.

The schema package covers hosting, storage, execution, game, and integration operations, with typed results for all seven standard server operations. The protocol specification and compatibility rules have been reviewed; Go CI validates schemas, references, examples, OpenAPI 3.2.1 structure, and published conformance vectors. Phase 2 now includes Go lifecycle/operation transition rules, a durable bbolt operation store, atomic acceptance with actor-scoped idempotency and per-server locks, immutable step input and completed output snapshots, an embedded local schema registry, an in-process adapter registry with scripted fakes for all five categories, and side-effect-free validation for the initial local configuration shape. Durable step dispatch, workflow orchestration/reconciliation, runtime config loading and secret resolution, process transport, and production adapters remain future work in Phases 2 and 3.

Phase 1 means the reviewed specification package and Go validator/conformance suite are ready for implementation. Stable protocol 1.0 remains gated on the initial Go controller passing behavioral conformance; optional implementations in other languages are welcome but are not a release requirement.
