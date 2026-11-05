# Development Roadmap

**Status:** Planning roadmap. Phases are ordered to reduce production risk; dates are not commitments.

## Phase 0 — Repository and design foundation

- [x] Establish license and project policies.
- [x] Document initial architecture direction and safety boundaries.
- [ ] Confirm repository name and public description.
- [ ] Define threat model and configuration/secrets model.
- [ ] Agree on initial language, runtime, and supported operating systems.

## Phase 1 — Core contracts and local test harness

- Define typed hosting, storage, game, and integration adapter contracts.
- Define capability discovery and unsupported-operation behavior.
- Define lifecycle state machine and operation/audit records.
- Define configuration schema and secret references.
- Build fake adapters and contract tests.
- Do not introduce production provisioning or destructive actions in this phase.

## Phase 2 — First adapters and safe manual lifecycle

- Implement Vultr hosting adapter.
- Implement Wasabi storage adapter.
- Implement Minecraft Paper game adapter.
- Implement local CLI for inspect, plan/preview, provision, readiness, backup, restore, and destroy.
- Add dry-run and explicit target confirmation for destructive actions.
- Validate with isolated staging resources.

## Phase 3 — Production lifecycle orchestration

- Coordinate disposable instance provisioning and restore.
- Add Velocity readiness and routing integration.
- Route waiting lobby players only after backend readiness is verified.
- Detect idle backend state.
- Stop Paper, back up, verify, then destroy.
- Fail closed if save, upload, verification, or cleanup preconditions fail.

## Phase 4 — Releases and rollback

- Define immutable game software release manifest.
- Store Paper/plugin/config artifacts separately from world backups.
- Add compatibility metadata and checksums.
- Implement release listing, status, rollback preview, and rollback execution.
- Implement infrastructure image references and rollback.
- Enforce retention without deleting active rollback dependencies.
- Test rollback using copied worlds and isolated instances.

## Phase 5 — Staging and automated updates

- Add update discovery and candidate release creation.
- Build candidate infrastructure images without modifying approved images.
- Validate candidate images using fresh isolated instances.
- Create staging deployments from timestamped copies of production backups.
- Enforce staging read-only access to production backups.
- Automatically validate routine compatible updates.
- Require explicit approval for new Minecraft major versions and world migrations.
- Keep staging available until candidate acceptance/rejection.

## Phase 6 — Additional providers, games, and community adapters

- Add a second hosting provider (OVH is a candidate) to validate provider independence.
- Add another storage backend if useful.
- Publish adapter development guide and contract test suite.
- Add a second game adapter after the game interface is stable.
- Review plugin/extension trust boundaries and compatibility.

## Completion gates

A phase is not complete merely because code exists. It should include documentation, configuration examples, automated tests, failure-path tests, security and permission review, staging validation, and no secrets or production data in the repository. Production-impacting changes require explicit operator approval.

## Initial reference deployment

The first end-to-end target is Vultr + Wasabi + Paper + Velocity on Debian. Existing infrastructure remains operational while the controller is developed and tested separately. Existing production scripts should not be replaced until equivalent functionality is implemented, tested, and deliberately migrated.
