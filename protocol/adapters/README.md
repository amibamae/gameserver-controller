# Adapter Contracts

**Status:** Draft protocol contracts, version 0.1.0-draft.

Adapters connect the controller core to infrastructure and external systems. Adapter contracts are language-neutral. An adapter may be implemented in-process or as a separate process using the optional RPC binding.

## Adapter categories

| Category | Stable ID example | Responsibility |
|---|---|---|
| Hosting | `hosting.vultr` | Infrastructure instance lifecycle and provider observations |
| Storage | `storage.s3-compatible` | Persistent object storage, backup objects, integrity verification |
| Execution | `execution.systemd` | Run and inspect a game execution unit on an allocated environment |
| Game | `game.minecraft-paper` | Game-specific installation, configuration, save, compatibility, health semantics |
| Integration | `integration.velocity` | Routing, backend registration, drain, and external events |

Adapters are replaceable implementations of a contract. A category does not imply a particular vendor, programming language, process model, or deployment topology.

## Common requirements

Every adapter MUST:
1. Expose a descriptor and explicit capability set.
2. Declare protocol and contract versions it supports.
3. Declare configuration schema and required permission scopes.
4. Validate every request and reject unsupported capabilities explicitly.
5. Accept operation, step, correlation, and deadline context for mutating calls.
6. Preserve opaque identifiers and environment scope.
7. Return structured outcomes and safe diagnostics.
8. State idempotency, cancellation, timeout, reconciliation, and side-effect semantics.
9. Never make core authorization or destructive-safety decisions.
10. Never log or return secret values.
11. Distinguish request acceptance from confirmed postcondition.
12. Support read-only inspect/diagnostic operations where the category defines them.

A descriptor is descriptive, not permission. The core grants permissions independently and passes only the capabilities and secret references necessary for an operation.

## Adapter descriptor

The canonical descriptor is defined by [capability-descriptor.schema.json](../schemas/common/capability-descriptor.schema.json). Each advertised capability MUST publish input and output JSON Schemas, include their schema references in the descriptor, and pass the capability's conformance tests. Schemas MUST be bundled with the adapter or registered in the controller's trusted local schema registry; implementations MUST NOT fetch arbitrary schemas over the network at invocation time. Its conceptual fields are:
- Adapter ID and category.
- Display name and implementation version.
- Exact supported protocol versions and contract versions.
- Configuration schema reference.
- Capabilities, constraints, and resource kinds.
- Required permission scopes.
- Optional platform constraints and documentation URI.

Capability identifiers use lower-case reverse-domain or dotted namespaced strings. Capability versions and constraints MUST be explicit. An implementation MUST NOT infer capabilities from a vendor name.

## Calls and results

Each mutating call carries an operation ID and stable step ID. Before dispatch, the controller durably records step intent. The adapter MUST return either a definitive result or an outcome that the core can reconcile. It MUST NOT perform untracked background work after its operation context expires unless the contract explicitly defines the continuing external operation and its external reference.

Success means the operation-specific postcondition is confirmed, not merely that the adapter accepted a request.

## Category contracts

- [Hosting adapter](hosting.md)
- [Storage adapter](storage.md)
- [Execution adapter](execution.md)
- [Game adapter](games.md)
- [Integration adapter](integrations.md)

## Conformance

All adapters must pass the generic requirements in [../conformance/requirements.md](../conformance/requirements.md) and the category-specific tests for every capability they advertise. An adapter MUST NOT advertise a capability unless it passes that capability's contract tests.
