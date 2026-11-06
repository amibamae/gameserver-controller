# Conformance and Interoperability Requirements

**Status:** Draft, version 0.18.0-draft. Passing these requirements is a future release gate; no implementation is currently certified by this document.

## 1. Purpose

Conformance tests let independent implementations determine whether they follow the same protocol behavior. A test suite MUST be language-neutral and MUST NOT assume a specific database, framework, operating system, or UI.

## 2. Conformance targets

The protocol distinguishes these conformance profiles:
- **Controller core:** lifecycle, operations, authorization, state persistence, safety policy, and audit.
- **Adapter:** descriptor, capabilities, request/result semantics, error behavior, and category contract.
- **Management client:** API versioning, request validation, operation monitoring, and safe handling of errors.
- **Protocol implementation:** wire representation, schema validation, compatibility behavior, and version negotiation.

An implementation claims only the profile and capabilities it has tested. A controller does not claim a storage adapter is conformant merely because it can load the adapter.

## 3. Test fixture format

Shared fixtures are UTF-8 JSON files. Each fixture SHOULD identify:
- Fixture ID and protocol version.
- Contract area and test purpose.
- Input message/request or initial resource state.
- Expected outcome or state transition.
- Required error code where applicable.
- Side-effect expectation (none, simulated, or expected).
- Notes on setup and assertions.

Fixtures MUST contain synthetic identifiers and credentials only. They MUST NOT use production IDs, IP addresses, tokens, player data, or real infrastructure references.

## 4. Mandatory test groups

### Schema and message validation
- The local schema registry resolves every declared `$ref` without arbitrary network retrieval.
- Every advertised adapter capability has registered input/output schemas.
- Valid examples pass the correct JSON Schema 2020-12 validator.
- Invalid types, missing required fields, invalid timestamps, and unknown critical fields fail.
- Duplicate JSON object member names are rejected.
- Identifiers remain opaque.
- Secret values are rejected in secret-reference fields.

### Lifecycle
- Every documented transition is accepted when preconditions pass.
- Every undocumented transition is rejected.
- Destroyed logical server can be reprovisioned without changing server ID.
- Instance replacement changes instance reference.
- Non-ready/degraded/draining runtime is not routable.
- Failed backup verification blocks destruction.

### Operations
- Event sequence numbers increase monotonically per operation; consumers detect gaps and recover by reading the operation resource.
- Successful standard operations validate their typed result schema and bind result server/environment/instance fields to the durable operation target.
- `server.define` leaves the logical server `unprovisioned` and creates no external resource; `server.start` includes a readiness assertion bound to the exact instance; `server.stop` confirms game save and runtime termination.
- Operation record commits before side effect.
- Same idempotency key and same request maps to the same operation.
- Same key and changed request is conflict.
- Unknown external outcome enters reconciliation.
- Cancellation does not imply rollback.
- Controller restart preserves non-terminal operations and resource locks.
- Partial effects remain visible.

### Adapter
- Descriptor and capability versions validate.
- Missing capability returns unsupported-capability.
- Mutating adapter calls include operation and step context.
- Adapter timeout/crash leaves outcome uncertain until reconciliation.
- Adapter cannot grant itself permissions or bypass core policy.
- Adapter logs and errors redact secrets.
- Hosting instance inspection is read-only and bound to the exact requested instance; `unknown` is not treated as `active` or `destroyed`. Start, stop, reboot, and destroy results bind the exact provider instance; accepted does not establish completion, completed requires the action's exact postcondition, reboot does not establish game readiness, and unknown requires inspection before retry. Destroy is blocked unless the core confirms authorization, stopped state, and a verified backup for the current data generation. Region, plan, and OS-offering catalog listings preserve opaque IDs, honor requested filters and page bounds, and never treat unknown availability as permission to create.
- Game readiness output is bound to the exact requested instance and release; every requested required check has exactly one result, and `unknown` never permits `ready`.
- Data-set descriptions are release-bound, use unique IDs and safe relative paths, and advertise supported consistency modes. Confirmed game saves require completion evidence; unknown save or backup preparation outcomes require reconciliation references.
- Game health results bind to the exact release and instance, contain exactly one result per requested check, and do not promote unknown evidence to healthy. Game backup validation uses bounded streams for the exact manifest objects; `unknown` validation never verifies a backup.
- Game compatibility checks bind the exact backup manifest and target release, contain exactly one result per requested check, and derive aggregate status from all results. Unknown or incomplete evidence cannot be compatible, and a compatibility result does not verify backup integrity or authorize restore.
- Game migration requires an exactly bound verified backup, a `migration_required` result, exact approval, a distinct stopped target, bounded streams for every manifest object, and new target data generation. The source remains unchanged, all requested migration checks must pass, and an unknown outcome requires reconciliation before retry.
- A game install verifies every pinned artifact checksum before starting installation; invalid configuration is rejected before application; unknown installation validation blocks start.
- Execution log reads are bounded and redacted, health `unknown` is not healthy, and deleting a unit preserves persistent data. Proxy registration requires an unexpired readiness assertion bound to the exact instance; accepted or unknown routing state is not routable.
- Systemd lifecycle results bind the exact unit. A completed start does not establish game readiness, and an unknown start or stop outcome requires inspection before retry. Force stop requires approval bound to the exact unit and reason; accepted is not complete, unknown requires inspection, and a completed result confirms stopped state only.

### Proxy routing and player transfer
- Backend registration requires a current readiness assertion bound to the exact server, environment, and instance.
- An expired or mismatched readiness assertion causes rejection before any transfer side effect.
- A transfer request names one source, one destination, one waiting cohort, and an explicit session list.
- A session that has left the cohort or source backend is not transferred.
- Every requested session receives exactly one outcome; aggregate counts equal the per-session results.
- A partial transfer remains visible as a partial result rather than being reported as full success.
- An `outcome_unknown` session is inspected at the proxy before a retry is attempted.
- Transfer messages and logs do not require or expose player UUIDs, account names, IP addresses, or chat.

### Storage and security
- Every advertised storage capability validates against its registered input and output schemas.
- Put/get transfers use one-use, operation-scoped, direction-bound handles with an enforced byte limit and expiry.
- A completed object upload is not evidence that a backup is verified.
- Manifest object references match the manifest environment and authorized storage profile.
- Exact object/version identity is preserved for head, get, copy, delete, and checksum verification.
- ETag is not treated as a content checksum.
- A partial or missing object, length mismatch, or checksum mismatch blocks backup verification, restore, and destruction as applicable.
- A checksum-valid backup from an older data generation does not authorize destruction of the current instance.
- A forced stop request without an approval reference is rejected by schema/policy.
- Incomplete upload cannot be marked verified.
- Checksum/length mismatch blocks restore and destruction.
- Staging cannot write/delete production objects.
- Unsafe archive paths and symlinks are rejected.
- Destruction requires exact target, verified backup, and authorization.

### Management API and client
- Async mutating calls return an operation reference only after durable acceptance.
- Client can poll operation state after disconnect.
- Client treats unknown status values safely.
- Client never assumes request timeout means operation failure.
- Authentication and authorization failures do not disclose protected resource details.

## 5. Cross-language interoperability gate

Before a stable 1.0 release, at least two independently built implementations in different programming languages MUST:
1. Validate the same published schemas.
2. Parse and emit the same canonical examples.
3. Execute the same lifecycle and operation test vectors.
4. Agree on structured error codes and legal transitions.
5. Demonstrate an end-to-end adapter exchange using the selected transport or in-process contract.
6. Demonstrate compatibility behavior for unknown optional fields and unsupported capabilities.

At least one implementation MUST be developed without importing code generated from the other implementation. Generated bindings MAY be used after the language-neutral schemas are authoritative.

Test results, implementation versions, protocol version, schema registry digest, and fixture suite digest MUST be published. A self-reported checklist alone is not evidence of interoperability.

## 6. Test vector index

- [valid-provision-request.json](test-vectors/valid-provision-request.json)
- [invalid-lifecycle-transition.json](test-vectors/invalid-lifecycle-transition.json)
- [backup-verification-blocks-destroy.json](test-vectors/backup-verification-blocks-destroy.json)
- [idempotency-key-conflict.json](test-vectors/idempotency-key-conflict.json)
- [unknown-provider-outcome.json](test-vectors/unknown-provider-outcome.json)
- [staging-production-write-denied.json](test-vectors/staging-production-write-denied.json)
- [backup-stale-generation-blocks-destroy.json](test-vectors/backup-stale-generation-blocks-destroy.json)
- [operation-event-sequence.json](test-vectors/operation-event-sequence.json)
- [readiness-required-before-transfer.json](test-vectors/readiness-required-before-transfer.json)
- [game-readiness-unknown-blocks-ready.json](test-vectors/game-readiness-unknown-blocks-ready.json)
- [game-release-install-configure-fail-closed.json](test-vectors/game-release-install-configure-fail-closed.json)
- [game-install-repeat-same-release.json](test-vectors/game-install-repeat-same-release.json)
- [game-validate-installation-unknown-blocks-start.json](test-vectors/game-validate-installation-unknown-blocks-start.json)
- [execution-logs-health-delete-safety.json](test-vectors/execution-logs-health-delete-safety.json)
- [proxy-register-expired-readiness.json](test-vectors/proxy-register-expired-readiness.json)
- [proxy-register-accepted-not-routable.json](test-vectors/proxy-register-accepted-not-routable.json)
- [proxy-unknown-drain-unregister-requires-inspection.json](test-vectors/proxy-unknown-drain-unregister-requires-inspection.json)
- [velocity-transfer-partial-results.json](test-vectors/velocity-transfer-partial-results.json)
- [storage-partial-upload-not-verified.json](test-vectors/storage-partial-upload-not-verified.json)
- [storage-checksum-mismatch-blocks-restore.json](test-vectors/storage-checksum-mismatch-blocks-restore.json)
- [storage-etag-is-not-checksum.json](test-vectors/storage-etag-is-not-checksum.json)
- [storage-staging-delete-denied.json](test-vectors/storage-staging-delete-denied.json)

- [game-compatibility-check-compatible.json](test-vectors/game-compatibility-check-compatible.json)
- [game-compatibility-check-migration-required.json](test-vectors/game-compatibility-check-migration-required.json)
- [game-compatibility-check-incompatible.json](test-vectors/game-compatibility-check-incompatible.json)
- [game-compatibility-check-unknown.json](test-vectors/game-compatibility-check-unknown.json)
- [game-compatibility-check-incomplete.json](test-vectors/game-compatibility-check-incomplete.json)
- [game-compatibility-check-unknown-reported-compatible.json](test-vectors/game-compatibility-check-unknown-reported-compatible.json)
- [game-compatibility-check-target-mismatch.json](test-vectors/game-compatibility-check-target-mismatch.json)
- [game-migrate-approved-copy-success.json](test-vectors/game-migrate-approved-copy-success.json)
- [game-migrate-approval-required.json](test-vectors/game-migrate-approval-required.json)
- [game-migrate-verified-backup-required.json](test-vectors/game-migrate-verified-backup-required.json)
- [game-migrate-target-stopped.json](test-vectors/game-migrate-target-stopped.json)
- [game-migrate-backup-result-binding.json](test-vectors/game-migrate-backup-result-binding.json)
- [game-migrate-distinct-target-required.json](test-vectors/game-migrate-distinct-target-required.json)
- [game-migrate-target-binding.json](test-vectors/game-migrate-target-binding.json)
- [game-migrate-complete-checks-required.json](test-vectors/game-migrate-complete-checks-required.json)
- [game-migrate-compatibility-required.json](test-vectors/game-migrate-compatibility-required.json)
- [game-migrate-unknown-requires-reconciliation.json](test-vectors/game-migrate-unknown-requires-reconciliation.json)

- [execution-unit-start-accepted-not-ready.json](test-vectors/execution-unit-start-accepted-not-ready.json)
- [execution-unit-start-unknown-requires-inspection.json](test-vectors/execution-unit-start-unknown-requires-inspection.json)
- [execution-unit-start-target-mismatch.json](test-vectors/execution-unit-start-target-mismatch.json)
- [execution-unit-graceful-stop-completed-stopped.json](test-vectors/execution-unit-graceful-stop-completed-stopped.json)
- [execution-unit-force-stop-approval-required.json](test-vectors/execution-unit-force-stop-approval-required.json)
- [execution-unit-force-stop-approval-scope-denied.json](test-vectors/execution-unit-force-stop-approval-scope-denied.json)
- [execution-unit-force-stop-completed-stopped.json](test-vectors/execution-unit-force-stop-completed-stopped.json)
- [execution-unit-force-stop-accepted-not-stopped.json](test-vectors/execution-unit-force-stop-accepted-not-stopped.json)
- [execution-unit-force-stop-target-mismatch.json](test-vectors/execution-unit-force-stop-target-mismatch.json)
- [execution-unit-force-stop-unknown-requires-inspection.json](test-vectors/execution-unit-force-stop-unknown-requires-inspection.json)

- [hosting-instance-inspect-exact-active.json](test-vectors/hosting-instance-inspect-exact-active.json)
- [hosting-instance-inspect-unknown-not-active.json](test-vectors/hosting-instance-inspect-unknown-not-active.json)
- [hosting-instance-inspect-target-mismatch.json](test-vectors/hosting-instance-inspect-target-mismatch.json)
- [hosting-instance-start-completed-active.json](test-vectors/hosting-instance-start-completed-active.json)
- [hosting-instance-start-accepted-not-active.json](test-vectors/hosting-instance-start-accepted-not-active.json)
- [hosting-instance-start-unknown-requires-inspection.json](test-vectors/hosting-instance-start-unknown-requires-inspection.json)
- [hosting-instance-start-target-mismatch.json](test-vectors/hosting-instance-start-target-mismatch.json)
- [hosting-instance-stop-completed-stopped.json](test-vectors/hosting-instance-stop-completed-stopped.json)
- [hosting-instance-stop-accepted-not-stopped.json](test-vectors/hosting-instance-stop-accepted-not-stopped.json)
- [hosting-instance-stop-unknown-requires-inspection.json](test-vectors/hosting-instance-stop-unknown-requires-inspection.json)
- [hosting-instance-stop-target-mismatch.json](test-vectors/hosting-instance-stop-target-mismatch.json)
- [hosting-instance-reboot-completed-active.json](test-vectors/hosting-instance-reboot-completed-active.json)
- [hosting-instance-reboot-accepted-not-complete.json](test-vectors/hosting-instance-reboot-accepted-not-complete.json)
- [hosting-instance-reboot-unknown-requires-inspection.json](test-vectors/hosting-instance-reboot-unknown-requires-inspection.json)
- [hosting-instance-reboot-target-mismatch.json](test-vectors/hosting-instance-reboot-target-mismatch.json)
- [hosting-instance-destroy-authorization-required.json](test-vectors/hosting-instance-destroy-authorization-required.json)
- [hosting-instance-destroy-verified-backup-required.json](test-vectors/hosting-instance-destroy-verified-backup-required.json)
- [hosting-instance-destroy-current-generation-backup-required.json](test-vectors/hosting-instance-destroy-current-generation-backup-required.json)
- [hosting-instance-destroy-stopped-required.json](test-vectors/hosting-instance-destroy-stopped-required.json)
- [hosting-instance-destroy-completed-destroyed.json](test-vectors/hosting-instance-destroy-completed-destroyed.json)
- [hosting-instance-destroy-accepted-not-destroyed.json](test-vectors/hosting-instance-destroy-accepted-not-destroyed.json)
- [hosting-instance-destroy-unknown-requires-inspection.json](test-vectors/hosting-instance-destroy-unknown-requires-inspection.json)
- [hosting-instance-destroy-target-mismatch.json](test-vectors/hosting-instance-destroy-target-mismatch.json)
- [hosting-regions-list-filtered.json](test-vectors/hosting-regions-list-filtered.json)
- [hosting-plans-list-resource-filter.json](test-vectors/hosting-plans-list-resource-filter.json)
- [hosting-plans-list-minimum-filter-mismatch.json](test-vectors/hosting-plans-list-minimum-filter-mismatch.json)
- [hosting-os-offerings-list-unknown-not-available.json](test-vectors/hosting-os-offerings-list-unknown-not-available.json)

## 7. Certification status

There is no central certification authority or certification program in this draft. The project may publish test tooling and self-reported results. A future governance process may define conformance marks, but passing tests must never be represented as a formal security certification.
