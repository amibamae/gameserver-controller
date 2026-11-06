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
- [x] Publish the durable operation protocol draft covering acceptance, legal operation/step status transitions, idempotency, timeouts, retries, cancellation, reconciliation, recovery, progress, and partial outcomes.
- [x] Publish the initial standard operation catalog and parameter schemas for logical-server definition, provision, start, stop, backup, restore, and destroy.
- [ ] Review and agree on common types and resolve remaining open decisions.
- [ ] Review and agree on operation semantics, legal status transitions, and cancellation/recovery rules.
- [x] Publish initial lifecycle states, allowed transitions, preconditions, postconditions, and failure semantics in draft form.
- [ ] Review and agree on lifecycle semantics and all recovery edge cases.
- [x] Publish draft hosting, storage, execution, game-runtime, and integration adapter contracts.
- [ ] Review and agree on each adapter contract and capability operation.
- [x] Publish initial adapter descriptors, capability declarations, and protocol negotiation draft.
- [ ] Finalize compatibility negotiation, capability constraints, and permission semantics.
- [x] Publish a draft management API contract and OpenAPI description for independent clients; no hosted panel required.
- [ ] Review API resource semantics, authentication profile, pagination, event delivery, and compatibility.
- [x] Publish initial protocol security, authorization, audit, privacy, consent, and environment-isolation requirements.
- [ ] Review threat model, permission scopes, approval representation, and audit integrity/retention.
- [x] Select draft standards: JSON, JSON Schema 2020-12, JSON-RPC 2.0, OpenAPI 3.2.1, RFC 3339, and RFC 8785 where canonicalization is required.
- [ ] Validate the selected standards, validators, generators, and transport binding across target languages.
- [x] Publish an initial set of language-neutral schemas, examples, test vectors, and conformance requirements.
- [x] Publish a canonical durable-operation result schema, typed results for all seven standard operations, and Velocity player-transfer request/result schemas.
- [x] Publish game backup compatibility schemas and conformance vectors for compatible, migration-required, incompatible, unknown, and mismatched-target outcomes.
- [x] Add CI validation and a behavioral conformance runner; extend the suite as protocol fixtures are added.
- [x] Publish registered input/output schemas for the hosting instance-create capability.
- [x] Publish input/output schemas and conformance examples for the initial storage object capabilities.
- [ ] Specify a bounded data-transfer binding for process adapters; `jsonrpc-stdio-v1` does not yet support storage put/get.
- [ ] Expand capability-specific input/output schemas across execution, game, and integration operations; complete hosting coverage.
- [x] Publish initial draft compatibility rules for negotiation, schema identity, evolution, and in-flight operations.
- [ ] Review the complete protocol and freeze compatibility rules.
- [ ] Build independent implementations in at least two programming languages and pass the same conformance suite.
- [ ] Publish stable protocol v1.0 only after independent interoperability evidence.

**Exit criteria:** An implementer in a different programming language can understand the specification and build a conforming implementation without relying on undocumented behavior or language-specific interfaces.

## Phase 2 — Implementation technology selection and reference test harness

- [x] Select Go for the controller core and repository-owned protocol tooling while keeping the wire contract language-neutral.
- Finalize the minimum supported Go version, dependency policy, packaging model, and supported Debian versions before the first controller release.
- Select the initial state persistence and configuration-validation approach.
- Extend the language-neutral behavioral suite and test harness as additional protocol contracts are specified.
- Implement fake hosting, storage, execution, game, and integration adapters.
- Test protocol compatibility, error behavior, idempotency, timeout handling, and recovery without contacting production services.
- Establish build, test, dependency, and release practices that do not require AI services.

**Exit criteria:** A minimal implementation passes the agreed protocol conformance suite using fake adapters, with no production credentials or live infrastructure access.

## Phase 3 — First local controller and safe manual lifecycle

- Implement the controller core and application services against the protocol.
- Implement a local CLI for inspection, configuration validation, plan/preview, and operation monitoring.
- Implement Vultr hosting adapter.
- Implement provider-standard OS selection and versioned bootstrap execution for new instances.
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
- Record and validate the exact bootstrap revision and configuration used by each instance; support returning to a previously approved bootstrap revision.
- Enforce retention without deleting active rollback dependencies.
- Test rollback using copied worlds and isolated instances.

## Phase 6 — Staging and automated updates

- Add update discovery and candidate release creation.
- Validate candidate bootstrap revisions by provisioning fresh isolated instances from standard provider OS offerings.
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

The project is in **Phase 1 — Language-independent protocol design**. The protocol principles, common types, and first durable operation protocol draft are published. The protocol remains 0.10.0-draft. Initial JSON, JSON Schema, JSON-RPC, OpenAPI, timestamp, and canonicalization choices, lifecycle model, five adapter contracts, security draft, management API, schemas, examples, and test vectors are published. They have not all been independently validated or reviewed, and no controller or production adapter implementation has been built.

Initial lifecycle, five adapter, RPC, management API, security, schema, example, and conformance drafts have been published. CI validates the schema package and executes all thirty-six current conformance fixtures. The hosting instance-create capability now has registered input and output schemas, and typed result schemas cover all seven standard operations. Game readiness, release description, install, configuration, installed-artifact validation, persistent data sets, save, backup preparation, health, backup validation, and backup compatibility checks have registered schemas and conformance coverage. Initial execution diagnostics/deletion and proxy backend lifecycle schemas are also registered; remaining game capabilities are still in progress. The storage capability schemas are published, while process-based bulk transfer still needs a binding. Next, specify migration approval and rollback semantics, add the bounded process-adapter transfer binding, and run independent cross-language interoperability tests.
