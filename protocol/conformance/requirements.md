# Conformance and Interoperability Requirements

**Status:** Draft, version 0.1.0-draft. Passing these requirements is a future release gate; no implementation is currently certified by this document.

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
- [velocity-transfer-partial-results.json](test-vectors/velocity-transfer-partial-results.json)

## 7. Certification status

There is no central certification authority or certification program in this draft. The project may publish test tooling and self-reported results. A future governance process may define conformance marks, but passing tests must never be represented as a formal security certification.
