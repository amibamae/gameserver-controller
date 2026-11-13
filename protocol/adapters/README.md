# Adapter Contracts

**Status:** Draft protocol contracts, version 0.21.0-draft.

Adapters connect the controller core to infrastructure and other services. The contracts work across programming languages. An adapter MAY run in-process or as a separate process under the optional RPC binding.

## Five adapter categories

| Category | Example ID | Responsibility |
|---|---|---|
| Hosting | `hosting.vultr` | Provider instance lifecycle and observations |
| Storage | `storage.s3-compatible` | Persistent objects, backups, integrity checks |
| Execution | `execution.systemd` | Run and inspect a game unit on an allocated environment |
| Game | `game.minecraft-paper` | Game installation, configuration, saves, compatibility, health |
| Integration | `integration.velocity` | Routing, backend registration/drain, external events |

Adapters implement a category contract. The category does not pick a vendor, language, process model, or deployment topology.

## What the Go code supports

The repo now has an in-process Go adapter interface and registry. It validates descriptors and capability inputs/outputs against bundled local schemas, and refuses unadvertised capabilities before invoking an adapter. Scripted fakes cover all five categories without making network or provider calls.

This boundary is not a production adapter and does not implement workflow policy, durable step dispatch, or reconciliation. The optional stdio process binding is also future work. Passing fake-adapter tests does not prove that a production adapter conforms.

## Shared requirements

Every adapter MUST:

1. Expose a descriptor and explicit capabilities.
2. Declare supported protocol and contract versions.
3. Declare configuration schema and required permission scopes.
4. Validate each request and explicitly reject unsupported capabilities.
5. Accept operation, step, correlation, and deadline context for mutations.
6. Preserve opaque identifiers and environment scope.
7. Return structured outcomes with safe diagnostics.
8. State its idempotency, cancellation, timeout, reconciliation, and side-effect behavior.
9. Leave core authorization and destructive-safety decisions to the controller.
10. Never log or return secret values.
11. Distinguish accepting a request from confirming its postcondition.
12. Support read-only inspection and diagnostics where the category defines them.

A descriptor describes the adapter; it does not authorize it. The core grants permission separately and passes only the capabilities and secret references needed for the operation.

## Adapter descriptors

The canonical descriptor schema is [capability-descriptor.schema.json](../schemas/common/capability-descriptor.schema.json). Each advertised capability MUST publish input and output JSON Schemas, list their references in its descriptor, and pass its conformance tests.

Bundle schemas with the adapter or register them in the controller's trusted local schema registry. Implementations MUST NOT fetch arbitrary schemas during a capability call.

The descriptor's conceptual fields are:

- Adapter ID and category.
- Display name and implementation version.
- Exact protocol and contract versions supported.
- Configuration schema reference.
- Capabilities, constraints, resource kinds, and stream transport profiles.
- Required permission scopes.
- Optional platform limits and documentation URI.

Capability IDs use lowercase reverse-domain or dotted namespaces. Declare capability versions and constraints explicitly. Do not infer capabilities from the vendor name.

## Calls and results

Every mutating call carries an operation ID and stable step ID. Before dispatch, the controller durably records step intent. The adapter returns a definitive result or an outcome the core can reconcile.

Do not run untracked background work after the operation context expires unless the contract explicitly defines the ongoing external operation and provides its reference.

Success means the operation-specific postcondition is confirmed. Adapter acceptance alone does not count as success.

## Category contracts

- [Hosting](hosting.md)
- [Storage](storage.md)
- [Execution](execution.md)
- [Game](games.md)
- [Integration](integrations.md)

## Conformance

Every adapter passes the generic requirements in [conformance/requirements.md](../conformance/requirements.md) and tests for every capability it advertises. Do not advertise a capability unless it passes that capability's contract tests.