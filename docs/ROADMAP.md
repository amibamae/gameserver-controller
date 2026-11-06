# Development Roadmap

**Status:** Planning roadmap. Phases are ordered to reduce production risk; dates are not commitments.

## Phase 0 — Repository and project foundation

- [x] Establish license and project policies.
- [x] Document initial architecture direction and safety boundaries.
- [x] Establish public repository documentation, contribution guidance, and security reporting.
- [x] Publish AI-use disclosure and commit that AI will never be a required development, build, runtime, or operational dependency.
- [x] Document the initial reference deployment and production/staging safety requirements.
- [x] Establish initial core design, adapter contract, configuration, and security documents.

## Phase 1 — Language-independent protocol design

**Current phase.** Define the contracts before selecting an implementation language or building the orchestration engine.

- [x] Establish protocol-first principles, language neutrality, transport independence, and compatibility goals.
- [x] Establish separate adapter-protocol and management-API communication surfaces.
- [x] Establish that the controller must be usable without a web panel; future web, desktop, and mobile clients may be developed independently.
- [x] Draft common protocol concepts: identifiers, external resource references, timestamps, versions, adapter identity, capabilities, structured errors, request context, and secret references.
- [x] Publish the first durable operation protocol draft covering acceptance, operation and step states, idempotency, timeouts, retries, cancellation, reconciliation, recovery, progress, and partial outcomes.
- [ ] Review and agree on common types and resolve remaining open decisions.
- [ ] Review and agree on operation semantics, legal status transitions, and cancellation/recovery rules.
- [ ] Define lifecycle states, allowed transitions, preconditions, postconditions, and failure semantics.
- [ ] Define hosting, storage, game-runtime, and integration adapter contracts in protocol terms.
- [ ] Define adapter identity, protocol compatibility negotiation, capability discovery, and permissions.
- [ ] Define management API requirements for independent clients, without requiring a hosted panel.
- [ ] Define authentication, authorization, audit, privacy, consent, and environment-isolation requirements across protocol surfaces.
- [ ] Evaluate candidate data formats, schemas, API descriptions, RPC envelopes, and transports; do not treat candidates as decisions until reviewed.
- [ ] Create language-neutral examples, schemas, test vectors, and conformance requirements.
- [ ] Review the complete protocol and mark a stable initial protocol version only after compatibility rules and conformance tests are agreed.

**Exit criteria:** An implementer in a different programming language can understand the specification and build a conforming implementation without relying on undocumented behavior or language-specific interfaces.

## Phase 2 — Implementation technology selection and reference test harness

- Evaluate programming languages against the completed protocol, maintainability, reliability, development speed, contributor accessibility, and available libraries.
- Select an implementation language only after protocol review; keep language choice separate from protocol authority.
- Select the initial state persistence and configuration-validation approach.
- Build language-neutral conformance fixtures and a test harness.
- Implement fake hosting, storage, game, and integration adapters.
- Test protocol compatibility, error behavior, idempotency, timeout handling, and recovery without contacting production services.
- Establish build, test, dependency, and release practices that do not require AI services.

**Exit criteria:** A minimal implementation passes the agreed protocol conformance suite using fake adapters, with no production credentials or live infrastructure access.

## Phase 3 — First local controller and safe manual lifecycle

- Implement the controller core and application services against the protocol.
- Implement a local CLI for inspection, configuration validation, plan/preview, and operation monitoring.
- Implement Vultr hosting adapter.
- Implement Wasabi (S3-compatible) storage adapter.
- Implement Minecraft Paper game adapter.
- Add dry-run behavior, explicit target confirmation, approvals, durable operation tracking, and audit.
- Validate provisioning, readiness, backup, restore, and destroy against isolated test resources only.
- Keep the management API documented and independently implementable; a hosted web panel remains out of scope.

## Phase 4 — Production lifecycle orchestration

- Coordinate disposable instance provisioning and restore.
- Add Velocity readiness and routing integration.
- Route waiting lobby players only after backend readiness is verified.
- Detect idle backend state.
- Stop Paper, back up, verify, then destroy.
- Fail closed if save, upload, verification, authorization, or cleanup preconditions fail.
- Reconcile ambiguous provider outcomes and recover interrupted operations safely.

## Phase 5 — Releases and rollback

- Define immutable game software release manifest.
- Store Paper/plugin/config artifacts separately from world backups.
- Add compatibility metadata and checksums.
- Implement release listing, status, rollback preview, and rollback execution.
- Implement infrastructure image references and rollback.
- Enforce retention without deleting active rollback dependencies.
- Test rollback using copied worlds and isolated instances.

## Phase 6 — Staging and automated updates

- Add update discovery and candidate release creation.
- Build candidate infrastructure images without modifying approved images.
- Validate candidate images using fresh isolated instances.
- Create staging deployments from timestamped copies of production backups.
- Enforce staging read-only access to production backups.
- Automatically validate routine compatible updates.
- Require explicit approval for new Minecraft major versions and world migrations.
- Keep staging available until candidate acceptance/rejection.

## Phase 7 — Additional providers, games, and community ecosystem

- Add a second hosting provider (OVH is a candidate) to validate provider independence.
- Add another storage backend if useful.
- Publish adapter development guide and protocol conformance test suite.
- Add a second game adapter after the game contract is stable.
- Encourage independent implementations and adapters in other programming languages.
- Review plugin/extension trust boundaries and compatibility.

## Future optional management clients

This is a parallel ecosystem opportunity, not a prerequisite for controller operation.

- Maintain a documented, versioned management API.
- Enable third parties to build web panels, desktop tools, or mobile clients without changing the controller core.
- Keep authentication, authorization, approvals, audit, and safety enforcement in the controller.
- Do not require the project maintainers to host a web panel.
- Do not expose provider credentials or permit clients to bypass lifecycle safeguards.

## Completion gates

A phase is not complete merely because code exists. It should include documentation, configuration examples, automated tests, failure-path tests, security and permission review, and isolated validation. No secrets or production data may enter the repository. Production-impacting changes require explicit operator approval.

Protocol completion additionally requires language-neutral schemas/examples, compatibility rules, conformance fixtures, and evidence that independent implementations can interoperate.

## Initial reference deployment

The first end-to-end target is Vultr + Wasabi + Paper + Velocity on Debian. Existing infrastructure remains operational while the controller is developed and tested separately. Existing production scripts should not be replaced until equivalent functionality is implemented, tested, and deliberately migrated.

## Current status and next work

The project is in **Phase 1 — Language-independent protocol design**. The protocol principles, common types, and first durable operation protocol draft are published. The protocol is not stable, and no implementation language, canonical serialization format, or wire transport has been selected.

The next planned specification is the lifecycle model: server states, permitted transitions, operation preconditions and postconditions, and safety behavior. Common types and operation semantics will continue to be reviewed alongside it.
