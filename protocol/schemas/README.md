# JSON Schema Package

**Status:** Draft, protocol version 0.17.0-draft.

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

See [index.json](index.json) for the machine-readable list of schema files and their IDs. The current draft includes common identifiers, resource references, request contexts, structured errors, secret references and capability descriptors; operation, lifecycle, execution, hosting, game, storage, integration, and adapter RPC schemas. The hosting package defines region, plan, and OS-offering catalog-list schemas plus instance-create, inspect, start, stop, and reboot input/output schemas. Storage schemas cover all nine initial object capabilities, exact container/object references, checksums, and bounded stream handles. The optional stdio transport still needs a separate byte-stream binding before it can support storage put/get. The game package defines exact-target readiness, release description, installation, configuration, installed-artifact validation, persistent data-set, save, backup-preparation, health, backup-validation, backup-compatibility, and game-migration schemas; the execution package defines bounded redacted logs, health inspection, unit deletion, typed start/graceful-stop outputs, and approval-bound force-stop input/output; the integration package defines proxy backend lifecycle and Velocity player-transfer requests/results. The package is intentionally a draft and may gain or revise schemas before 1.0.
