# JSON Schema Package

**Status:** Draft, protocol version 0.1.0-draft.

All schemas in this directory use JSON Schema Draft 2020-12 and UTF-8 JSON. The schema identifiers use stable URNs so that validators do not need to fetch executable or mutable content from the network.

## Registry and references

The schema package is a closed local registry. Implementations MUST load the schemas listed in [index.json](index.json) and register each schema by its declared `$id` before validating schemas that reference another schema. A validator MUST NOT attempt to resolve an unknown URN by making an uncontrolled network request.

Schema IDs are protocol identifiers, not web URLs. The package-relative file path is the distribution locator; the declared `$id` is the reference identity.

## Validation requirements

- Validate against JSON Schema Draft 2020-12.
- Enforce `format` assertions for `date-time` and `uri`, or perform equivalent protocol-level validation.
- Reject duplicate JSON object member names before schema validation.
- Reject invalid UTF-8, non-finite numbers, and integer values outside the protocol's interoperable safe-integer range.
- Enforce semantic rules that JSON Schema cannot express, including legal lifecycle transitions, capability support, authorization, approval, and backup verification.
- Validate the exact protocol/schema version selected by the peer.

Passing schema validation does not prove authorization, a legal lifecycle transition, external side-effect success, backup integrity, or protocol conformance.

## Schema inventory

See [index.json](index.json) for the machine-readable list of schema files and their IDs. The current draft includes common identifiers, resource references, request contexts, structured errors, secret references and capability descriptors; operation, lifecycle, and execution schemas; hosting, storage, integration, and adapter RPC schemas. The initial integration package defines Velocity player-transfer requests and results. The package is intentionally a draft and may gain or revise schemas before 1.0.
