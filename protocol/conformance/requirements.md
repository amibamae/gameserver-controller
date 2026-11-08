# Conformance and Interoperability Requirements

**Status:** Draft, version 0.21.0-draft. The initial Go implementation and validation tools exist, but these requirements remain a future release gate; this document does not certify an implementation.

## 1. Purpose

Use conformance tests to check whether independent implementations follow the same protocol behavior. A test suite MUST be language-neutral and MUST NOT assume a specific database, framework, operating system, or UI.

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
- Destroyed logical server can be reprovisioned without changing server ID or reusing the old concrete instance reference; a replacement starts a new data generation.
- Instance replacement changes instance reference.
- `ready` requires one fresh, exact-target result for every check in the game's declared versioned readiness set; missing, duplicate, extra, unknown, failed, stale, or mismatched results block readiness.
- Non-ready/degraded/draining runtime is not routable.
- Failed backup verification blocks destruction.

### Protocol compatibility
- Negotiation selects exactly one mutually supported protocol version; no common version fails with `core.compatibility.unsupported_version` and a validation/authorization error cannot trigger a lower-version retry.
- Unknown optional top-level fields on read-only Server and Operation resources are ignored, while unknown request fields and unknown nested resource fields are rejected.
- Unknown lifecycle or operation status values fail safely with `core.compatibility.unknown_state`; they never authorize a side effect.
- An in-flight operation preserves its accepted protocol and exact adapter capability versions across restart/upgrade, reconciles uncertain steps, and never replays completed effects; unavailable versions without a tested migration require operator intervention.

### Operations
- Event sequence numbers increase monotonically per operation; consumers ignore identical duplicate deliveries without reapplying them or regressing their cursor, detect gaps, and recover by reading the operation resource.
- Successful standard operations validate their typed result schema and bind result server/environment/instance fields to the durable operation target.
- Successful `server.backup` operations preserve core-owned `backup_initial_lifecycle_state` in the durable operation. The captured state, request-parameter presence, and explicit value determine the final state: ready resumes by default, degraded stays stopped by default but may explicitly resume under current policy and fresh readiness, and stopped remains stopped. Missing or unknown initial state and contradictory final states are rejected. Any readiness assertion is bound to that target and valid at operation completion. Successful stop results accept proxy-drain outcomes only when they match the request and configured profile.
- `server.restore` selects a verified backup whose manifest server and environment match the logical target. The physical target instance may differ from the backup source instance, and the successful result binds the requested backup and exact target while assigning a new data generation.
- Go vectors cover result-to-target binding for all seven standard operations, readiness target and freshness checks, and request-consistent stop drain outcomes.
- Restart requires the exact selected game adapter's `game.save.request` capability version and schema identity, a `succeeded` save step in the same durable operation, and inspection of the expected running incarnation no later than the save's `saved_at` time.
- The enclosing operation, immutable request snapshot, and any present result use the same `operation_type`, including results that preserve partial effects after failure or cancellation.
- An uncertain adapter result's capability output is validated but not persisted as completed evidence. A capability `reconciliation_ref` and any envelope `external_operation_ref` normalize into the durable step reference only when they match; a conflict blocks retry and requires operator review.
- Implementations that compute request fingerprints reproduce the canonical UTF-8 JSON and SHA-256 outputs in [request-fingerprint-jcs.json](test-vectors/request-fingerprint-jcs.json), including property ordering, number formatting, string escaping, and rejection of duplicate members, non-object parameters, and unsafe integers.
- `server.define` leaves the logical server `unprovisioned` and creates no external resource; `server.start` includes a readiness assertion bound to the exact instance; `server.stop` confirms game save and runtime termination.
- Operation record commits before side effect.
- Same idempotency key and same request returns the same stable operation reference and does not dispatch a second side effect.
- Same key and changed request is conflict.
- A matching actor/key with an expired operation record and retained tombstone returns `core.operation.idempotency_key_expired`; the old operation is not replayed and no new operation or side effect is created.
- Unknown external outcome enters reconciliation.
- Cancellation does not imply rollback. An undispatched step with no possible external effect can be cancelled without a provider call; a mutating call that may have been dispatched without a definitive response remains `reconciling` or `needs_operator`, is not reported `cancelled`, and is not retried before reconciliation.
- Controller restart preserves non-terminal operations and resource locks; an unresolved non-terminal operation restores its owned lock before conflicting work can dispatch, and elapsed time alone does not release that lock.
- Partial effects remain visible.

### Adapter
- Descriptor and capability versions validate.
- Missing capability returns unsupported-capability.
- Mutating adapter calls include operation and step context.
- Adapter timeout/crash leaves outcome uncertain until reconciliation.
- Adapter cannot grant itself permissions or bypass core policy.
- Adapter logs and errors redact secrets.
- Velocity `outcome_unknown` results require a fresh, exact proxy-session inspection before retry; only the same session still connected to the requested source backend and a current readiness assertion permit retry. A destination observation resolves without retry; disconnected, unknown, stale, future-dated, mismatched, and other-backend observations fail closed.
- Hosting instance inspection is read-only and bound to the exact requested instance; `unknown` is not treated as `active` or `destroyed`. Start, stop, reboot, and destroy results bind the exact provider instance; accepted does not establish completion, completed requires the action's exact postcondition, reboot does not establish game readiness, and unknown requires inspection before retry. Before rebooting an instance with persistent or unknown data status, the core requires confirmed save or clean-stop evidence bound to the exact instance; bypass requires separate valid approval bound to the emergency reboot action and exact instance. Destroy is blocked unless the core confirms authorization, stopped state, and a verified backup for the current data generation. Any required approval must bind the exact action, logical server, environment, adapter, instance, plan fingerprint, expiry, and approver; expired or mismatched approvals produce no provider call. Provider waits bind the exact operation and instance, remain bounded, do not cancel provider work on timeout, and require the expected observed state before confirming the action; unknown outcomes, terminal failures, or mismatched instance/operation references require inspection before retry. Region, plan, and OS-offering catalog listings preserve opaque IDs, honor requested filters and page bounds, and never treat unknown availability as permission to create.
- Game readiness output is bound to the exact requested instance and release; every requested required check has exactly one result, and `unknown` never permits `ready`.
- Data-set descriptions are release-bound, use unique IDs and safe relative paths, and advertise supported consistency modes. Confirmed game saves require completion evidence; unknown save, backup preparation, or capture outcomes require reconciliation references. Backup capture binds exact preparation, target, release, data generation, data set, and stable capture ID; the one-use adapter-to-controller stream appears only in the transient invocation context, never the persisted step input or operation result. Its authorization scope expires within five minutes, respects its byte limit, and has independently verified byte count and SHA-256. A consumed or expired handle cannot be replayed, and game-native watermarks remain informational because this draft defines no interoperable verifier.
- Game health results bind to the exact release and instance, contain exactly one result per requested check, and do not promote unknown evidence to healthy. Game backup validation uses bounded streams for the exact manifest objects; `unknown` validation never verifies a backup.
- Game compatibility checks bind the exact backup manifest and target release, contain exactly one result per requested check, and derive aggregate status from all results. Unknown or incomplete evidence cannot be compatible, and a compatibility result does not verify backup integrity or authorize restore.
- Game migration requires an exactly bound verified backup, a `migration_required` result, exact approval, a distinct stopped target, bounded streams for every manifest object, and new target data generation. The source remains unchanged, all requested migration checks must pass, and an unknown outcome requires reconciliation before retry.
- A `game.configure` result confirms only the exact submitted configuration when its SHA-256 RFC 8785 JCS digest covers both `configuration` and the secret-reference map; secret values never enter the digest. A digest mismatch after dispatch requires reconciliation before retry.
- `game.release.describe` returns the exact requested release ID, unique artifact IDs, and declared SHA-256 checksums for immutable artifacts. `game.install` binds the request and result to the exact target and release; `installed` and `already_installed` require the exact pinned artifact set with verified checksums and accurate start status.
- An unknown install result or contradictory result after installation starts requires exact-target reconciliation before retry. A `valid` installation check binds the exact target/release and contains exactly one `verified` result with matching expected/observed checksums per pinned artifact. Missing or contradictory artifact evidence, invalid configuration, and unknown installation validation block start.
- Execution log reads are bounded and redacted, health `unknown` is not healthy, and deleting a unit preserves persistent data. Proxy registration requires an unexpired readiness assertion bound to the exact instance; accepted or unknown routing state is not routable.
- Proxy player-count inspection binds the exact backend and instance. The core uses a count only when its observation timestamp is not in the future and is within the request's `maximum_age_seconds`; `unknown`, stale, future-dated, missing, and mismatched observations cannot prove zero sessions or complete a drain. Go vectors cover the inclusive freshness boundary, stale and future timestamps, unknown counts, incomplete observations, and target mismatch.
- Systemd lifecycle results bind the exact unit. A completed start does not establish game readiness, and an unknown start or stop outcome requires inspection before retry. Force stop requires approval bound to the exact unit and reason; accepted is not complete, unknown requires inspection, and a completed result confirms stopped state only.

### Proxy routing and player transfer
- Backend registration requires a current readiness assertion bound to the exact server, environment, and instance.
- Confirmation requires a schema-valid result bound to the requested backend and instance, with `request_outcome` set to `confirmed` and the exact postcondition. Before retrying an unconfirmed drain or unregister, inspect the exact requested backend.
- Routing requires a schema-valid inspection for the exact backend and instance and a matching environment; only `registered` and `routable` permits routing, and contradictory state pairs are schema-invalid.
- An expired or mismatched readiness assertion causes rejection before any transfer side effect.
- A transfer request names one source, one destination, one waiting cohort, and an explicit session list.
- A session that has left the cohort or source backend is not transferred.
- Every requested session receives exactly one outcome; aggregate counts equal the per-session results.
- Go conformance validates both schemas, request/result target, cohort, and backend binding, the exact unique session set, and aggregate counts.
- A partial transfer remains visible as a partial result rather than being reported as full success.
- An `outcome_unknown` session is inspected at the proxy before a retry is attempted.
- Transfer messages and logs do not require or expose player UUIDs, account names, IP addresses, or chat.
- Go vectors use synthetic canaries in requests, protocol-error messages/details, and diagnostic logs; schema-valid leaks still fail the privacy case.

### Storage and security
- Every advertised storage capability validates against its registered input and output schemas.
- Put/get transfers use one-use, operation-scoped, direction-bound handles with an enforced byte limit and expiry. Process adapters advertise put/get only after negotiating a declared stream profile; local stream handshakes reject reused, expired, or out-of-scope handles, and interrupted transfers are never treated as complete.
- A completed object upload is not evidence that a backup is verified.
- Manifest object references match the manifest environment and authorized storage profile.
- Exact object/version identity is preserved for head, get, copy, delete, and checksum verification.
- ETag is not treated as a content checksum.
- A partial or missing object, length mismatch, or checksum mismatch blocks backup verification, restore, and destruction as applicable.
- A checksum-valid backup from an older data generation does not authorize destruction of the current instance.
- Game backup capture is dispatched only for a schema-valid, completed preparation that exactly binds target, release, backup, generation, data set, and consistency mode.
- A capture result is accepted only when its complete binding and bounded stream integrity are verified; any schema-invalid result after dispatch requires reconciliation. A valid exact-bound `rejected` result proves no capture side effect, while an invalid or mismatched rejection requires reconciliation.
- A game-native write watermark without a standardized verifier contract cannot establish freshness or authorize destruction; game-native backups remain recovery-only for destructive decisions in this draft.
- A forced stop request without an approval reference is rejected by schema/policy.
- Incomplete upload cannot be marked verified.
- Checksum/length mismatch blocks restore and destruction.
- Staging cannot write/delete production objects, even if a production write/delete scope is attached to the staging actor; same-environment backup writes and deletes require their exact action scope.
- Unsafe archive paths and symlinks are rejected.
- Destruction requires exact target, verified backup, and authorization.

### Management API and client
- Async mutating calls return an operation reference only after durable acceptance.
- Client can poll operation state after disconnect.
- Client treats unknown status values safely.
- Client never assumes request timeout means operation failure.
- Authentication and authorization failures do not disclose protected resource details.
- Remote management authentication rejects invalid signatures, expired credentials, wrong audiences, and revoked credentials with 401; denied requests establish no actor, return no protected resource data, create no operation, and dispatch no adapter call. A valid control request maps the actor only from the authenticated principal.
- Local authentication derives the actor only from a verified operating-system peer identity; unavailable peer identity and loopback TCP without authenticated peer identity are denied. A client-supplied actor claim cannot replace the peer identity.
- A synthetic bearer token appears only in the Authorization header over TLS and is absent from URLs, query parameters, request bodies, operation records, logs, errors, events, and audit records.
- Client-supplied actor, role, or permission claims cannot elevate access; accepted operations use the authenticated principal as `actor_id`, and denied attempts return no protected resource data.
- Client session expiry alone does not cancel or pause an already accepted durable operation or require a client token refresh; independent reauthorization for sensitive steps still evaluates current permissions, approvals, target, and policy.
- A list cursor is bound to the authenticated actor and canonical filter scope; reuse under another actor or filter is rejected with a generic invalid-request error and returns no resource items or metadata.

## 5. Optional implementations in other languages

The initial Go implementation MUST pass the published conformance suite before release. Implementations in other languages are welcome as community contributions and MAY run the same suite, but a second language is not a release requirement.

A contributed implementation SHOULD:

1. Validate the same published schemas.
2. Parse and emit the same canonical examples.
3. Execute the same lifecycle and operation test vectors.
4. Agree on structured error codes and legal transitions.
5. Demonstrate an adapter exchange using the selected transport or in-process contract.
6. Handle unknown optional fields and unsupported capabilities compatibly.

A contributed implementation SHOULD preserve the published protocol meaning. Generated bindings MAY be used after the language-neutral schemas are authoritative. Contributors claiming conformance SHOULD publish the implementation and protocol versions, schema registry digest, and fixture-suite digest. Generate the machine-readable package manifest with `go run ./cmd/protocol-check -manifest`. The schema digest covers `protocol/schemas/index.json` and every indexed schema; the fixture digest covers every JSON file under `protocol/conformance/test-vectors`. Algorithm `sha256-path-filehash-v1` sorts repository-relative UTF-8 paths, hashes each file's exact bytes with SHA-256, then hashes the concatenated records `path NUL lowercase-file-digest LF`. The committed manifest must match the command output; publish a finalized package from an immutable version tag.
 
## 6. Test vector index

- [raw-json-wire-interoperability.json](test-vectors/raw-json-wire-interoperability.json)

- [valid-provision-request.json](test-vectors/valid-provision-request.json)
- [invalid-lifecycle-transition.json](test-vectors/invalid-lifecycle-transition.json)
- [lifecycle-replacement-identity.json](test-vectors/lifecycle-replacement-identity.json)
- [lifecycle-required-readiness-set.json](test-vectors/lifecycle-required-readiness-set.json)
- [protocol-version-negotiation-no-downgrade.json](test-vectors/protocol-version-negotiation-no-downgrade.json)
- [compatibility-response-evolution.json](test-vectors/compatibility-response-evolution.json)
- [operation-upgrade-recovery.json](test-vectors/operation-upgrade-recovery.json)
- [backup-verification-blocks-destroy.json](test-vectors/backup-verification-blocks-destroy.json)
- [idempotency-key-conflict.json](test-vectors/idempotency-key-conflict.json)
- [idempotency-key-replay-same-request.json](test-vectors/idempotency-key-replay-same-request.json)
- [idempotency-key-expired-reuse.json](test-vectors/idempotency-key-expired-reuse.json)
- [operation-lock-survives-restart.json](test-vectors/operation-lock-survives-restart.json)
- [typed-define-start-stop-results.json](test-vectors/typed-define-start-stop-results.json)
- [operation-type-binding.json](test-vectors/operation-type-binding.json)
- [operation-unknown-result-reference-projection.json](test-vectors/operation-unknown-result-reference-projection.json)
- [request-fingerprint-jcs.json](test-vectors/request-fingerprint-jcs.json)
- [typed-define-start-stop-target-mismatch.json](test-vectors/typed-define-start-stop-target-mismatch.json)
- [typed-define-start-stop-future-readiness.json](test-vectors/typed-define-start-stop-future-readiness.json)
- [typed-define-start-stop-no-drain.json](test-vectors/typed-define-start-stop-no-drain.json)
- [typed-define-start-stop-no-proxy-drain.json](test-vectors/typed-define-start-stop-no-proxy-drain.json)
- [typed-provision-backup-restore-destroy-results.json](test-vectors/typed-provision-backup-restore-destroy-results.json)
- [restore-result-final-state-policy.json](test-vectors/restore-result-final-state-policy.json)
- [backup-result-no-resume-final-state.json](test-vectors/backup-result-no-resume-final-state.json)
- [backup-initial-state-resume-policy.json](test-vectors/backup-initial-state-resume-policy.json)
- [typed-provision-backup-restore-destroy-target-mismatch.json](test-vectors/typed-provision-backup-restore-destroy-target-mismatch.json)
- [typed-restore-data-generation-not-rotated.json](test-vectors/typed-restore-data-generation-not-rotated.json)
- [typed-provision-backup-restore-readiness-binding.json](test-vectors/typed-provision-backup-restore-readiness-binding.json)
- [typed-provision-backup-restore-readiness-freshness.json](test-vectors/typed-provision-backup-restore-readiness-freshness.json)
- [unknown-provider-outcome.json](test-vectors/unknown-provider-outcome.json)
- [staging-production-write-denied.json](test-vectors/staging-production-write-denied.json)
- [backup-stale-generation-blocks-destroy.json](test-vectors/backup-stale-generation-blocks-destroy.json)
- [operation-event-sequence.json](test-vectors/operation-event-sequence.json)
- [operation-event-duplicate-delivery.json](test-vectors/operation-event-duplicate-delivery.json)
- [operation-survives-client-session-expiry.json](test-vectors/operation-survives-client-session-expiry.json)
- [management-authentication-actor-spoofing.json](test-vectors/management-authentication-actor-spoofing.json)
- [management-authentication-credential-profiles.json](test-vectors/management-authentication-credential-profiles.json)
- [management-pagination-cursor-scope.json](test-vectors/management-pagination-cursor-scope.json)
- [readiness-required-before-transfer.json](test-vectors/readiness-required-before-transfer.json)
- [readiness-target-mismatch-before-transfer.json](test-vectors/readiness-target-mismatch-before-transfer.json)
- [readiness-server-mismatch-before-transfer.json](test-vectors/readiness-server-mismatch-before-transfer.json)
- [game-readiness-unknown-blocks-ready.json](test-vectors/game-readiness-unknown-blocks-ready.json)
- [game-release-install-configure-fail-closed.json](test-vectors/game-release-install-configure-fail-closed.json)
- [game-release-describe-integrity-and-identity.json](test-vectors/game-release-describe-integrity-and-identity.json)
- [game-install-release-binding-and-reconciliation.json](test-vectors/game-install-release-binding-and-reconciliation.json)
- [game-configure-pinned-schema-and-result-binding.json](test-vectors/game-configure-pinned-schema-and-result-binding.json)
- [game-backup-capture-binding-and-stream-integrity.json](test-vectors/game-backup-capture-binding-and-stream-integrity.json)
- [game-install-repeat-same-release.json](test-vectors/game-install-repeat-same-release.json)
- [game-install-completed-verifies-exact-artifacts.json](test-vectors/game-install-completed-verifies-exact-artifacts.json)
- [game-install-incomplete-artifact-set-requires-reconciliation.json](test-vectors/game-install-incomplete-artifact-set-requires-reconciliation.json)
- [game-validate-installation-unknown-blocks-start.json](test-vectors/game-validate-installation-unknown-blocks-start.json)
- [game-validate-installation-valid-exact-artifacts.json](test-vectors/game-validate-installation-valid-exact-artifacts.json)
- [game-validate-installation-valid-incomplete-artifacts-blocks-start.json](test-vectors/game-validate-installation-valid-incomplete-artifacts-blocks-start.json)
- [game-validate-installation-valid-claim-with-unverified-artifact.json](test-vectors/game-validate-installation-valid-claim-with-unverified-artifact.json)
- [game-validate-installation-valid-claim-with-checksum-mismatch.json](test-vectors/game-validate-installation-valid-claim-with-checksum-mismatch.json)
- [execution-logs-health-delete-safety.json](test-vectors/execution-logs-health-delete-safety.json)
- [proxy-register-expired-readiness.json](test-vectors/proxy-register-expired-readiness.json)
- [proxy-backend-drain-unregister-state-confirmation.json](test-vectors/proxy-backend-drain-unregister-state-confirmation.json)
- [proxy-backend-inspect-target-state-and-routability.json](test-vectors/proxy-backend-inspect-target-state-and-routability.json)
- [proxy-register-accepted-not-routable.json](test-vectors/proxy-register-accepted-not-routable.json)
- [proxy-unknown-drain-unregister-requires-inspection.json](test-vectors/proxy-unknown-drain-unregister-requires-inspection.json)
- [velocity-transfer-partial-results.json](test-vectors/velocity-transfer-partial-results.json)
- [velocity-transfer-count-mismatch.json](test-vectors/velocity-transfer-count-mismatch.json)
- [velocity-transfer-context-mismatch.json](test-vectors/velocity-transfer-context-mismatch.json)
- [velocity-transfer-duplicate-session-result.json](test-vectors/velocity-transfer-duplicate-session-result.json)
- [velocity-transfer-target-mismatch.json](test-vectors/velocity-transfer-target-mismatch.json)
- [storage-partial-upload-not-verified.json](test-vectors/storage-partial-upload-not-verified.json)
- [storage-checksum-mismatch-blocks-restore.json](test-vectors/storage-checksum-mismatch-blocks-restore.json)
- [storage-etag-is-not-checksum.json](test-vectors/storage-etag-is-not-checksum.json)
- [storage-staging-delete-denied.json](test-vectors/storage-staging-delete-denied.json)
- [transport-storage-profile-negotiated.json](test-vectors/transport-storage-profile-negotiated.json)
- [transport-storage-profile-not-selected.json](test-vectors/transport-storage-profile-not-selected.json)
- [transport-storage-selection-not-offered.json](test-vectors/transport-storage-selection-not-offered.json)

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

- [execution-unit-create-exact-definition-no-autostart.json](test-vectors/execution-unit-create-exact-definition-no-autostart.json)
- [execution-unit-restart-clean-stop-distinct-runtime.json](test-vectors/execution-unit-restart-clean-stop-distinct-runtime.json)
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
- [hosting-instance-wait-pending-remains-pending.json](test-vectors/hosting-instance-wait-pending-remains-pending.json)
- [hosting-instance-wait-succeeded-postcondition-confirmed.json](test-vectors/hosting-instance-wait-succeeded-postcondition-confirmed.json)
- [hosting-instance-wait-succeeded-state-mismatch.json](test-vectors/hosting-instance-wait-succeeded-state-mismatch.json)
- [hosting-instance-wait-unknown-requires-inspection.json](test-vectors/hosting-instance-wait-unknown-requires-inspection.json)
- [hosting-instance-wait-operation-reference-mismatch.json](test-vectors/hosting-instance-wait-operation-reference-mismatch.json)
- [hosting-instance-wait-failed-terminal-requires-inspection.json](test-vectors/hosting-instance-wait-failed-terminal-requires-inspection.json)
- [hosting-regions-list-filtered.json](test-vectors/hosting-regions-list-filtered.json)
- [hosting-plans-list-resource-filter.json](test-vectors/hosting-plans-list-resource-filter.json)
- [hosting-plans-list-minimum-filter-mismatch.json](test-vectors/hosting-plans-list-minimum-filter-mismatch.json)
- [hosting-os-offerings-list-unknown-not-available.json](test-vectors/hosting-os-offerings-list-unknown-not-available.json)

## 7. Certification status

There is no central certification authority or certification program in this draft. The project may publish test tooling and self-reported results. A future governance process may define conformance marks, but passing tests must never be represented as a formal security certification.
