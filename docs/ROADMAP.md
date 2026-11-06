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

**Current phase.** Continue reviewing protocol contracts while building the initial controller in Go.

- [x] Define protocol-first, language-neutral, transport-independent principles.
- [x] Separate adapter calls from the management API.
- [x] Require the controller to work without a web panel; future clients may be independent.
- [x] Draft common identifiers, resources, timestamps, versions, capabilities, errors, request context, and secret references.
- [x] Draft durable operation acceptance, status transitions, idempotency, deadlines, retries, cancellation, reconciliation, recovery, progress, and partial outcomes.
- [x] Publish the initial operation catalog and parameter schemas for server definition, provision, start, stop, backup, restore, and destroy.
- [ ] Review common types and resolve open decisions.
- [ ] Review operation semantics, status transitions, cancellation, and recovery.
- [x] Draft lifecycle states, transitions, preconditions, postconditions, and failure behavior.
- [ ] Review lifecycle semantics and recovery edge cases.
- [x] Draft hosting, storage, execution, game, and integration adapter contracts.
- [ ] Review adapter contracts and capability behavior.
- [x] Draft adapter descriptors, capabilities, and protocol negotiation.
- [ ] Finalize compatibility negotiation, constraints, and permissions.
- [x] Publish a draft management API contract and OpenAPI definition; no hosted panel is required.
- [ ] Review API resources, authentication, pagination, events, and compatibility.
- [x] Draft protocol security, authorization, audit, privacy, consent, and environment isolation.
- [ ] Review threat model, permission scopes, approvals, and audit integrity/retention.
- [x] Select draft standards: JSON, JSON Schema 2020-12, JSON-RPC 2.0, OpenAPI 3.2.1, RFC 3339, and RFC 8785 where required.
- [ ] Validate standards, generators, and transport in Go; welcome optional implementations in other languages.
- [x] Publish initial schemas, examples, test vectors, and conformance requirements.
- [x] Publish operation results for provision, backup, restore, and destroy, plus Velocity transfer request/result schemas.
- [x] Add CI validation and an executable runner for the behavioral conformance suite.
- [x] Register input and output schemas for hosting instance creation.
- [ ] Extend schemas across storage, execution, game, and integration operations; complete hosting coverage and typed results.
- [x] Draft compatibility rules for negotiation, schema identity, evolution, and in-flight operations.
- [ ] Review the full protocol and freeze compatibility rules.
- [ ] Welcome independent implementations in other languages; they are optional contributions, not a release gate.
- [ ] Release stable protocol 1.0 only after interoperability evidence.

**Exit criteria:** The initial Go implementation follows the published contracts without relying on undocumented behavior. Other-language contributions remain welcome.

## Phase 2: Implement the Go controller and build a fake-adapter harness

- Use Go for the initial controller implementation and its validation tooling.
- Choose initial state persistence and configuration validation.
- Expand language-neutral behavioral tests as contracts mature.
- Implement fake hosting, storage, execution, game, and integration adapters.
- Test compatibility, errors, idempotency, timeouts, and recovery without production services.
- Establish build, test, dependency, and release practices that do not require AI.

**Exit criteria:** A minimal controller passes the agreed conformance suite with fake adapters, no production credentials, and no live infrastructure.

## Phase 3: Local controller and safe manual lifecycle

- Implement the core and application services against the protocol.
- Build a local CLI for inspection, config validation, plan/preview, and operation monitoring.
- Implement the Vultr hosting adapter and standard OS selection.
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

The project is in **Phase 1: Protocol design and review**, with initial Go validation tooling and a supervised backup pilot. The current schema snapshot is 0.21.0-draft. The full controller engine, management API, and production adapters are not yet implemented. CI validates schemas, examples, OpenAPI metadata, and the behavioral conformance suite.

Hosting instance creation has registered input and output schemas. Typed result schemas cover provision, backup, restore, and destroy. Storage and game capability schemas, other operation results, and additional edge-case vectors remain work. The drafts have not all been independently validated or reviewed. No controller engine or production adapter has been built.

Next, finish reviewing the contracts, add edge cases, and run the complete conformance suite in Go. Contributors may test optional implementations in other languages.