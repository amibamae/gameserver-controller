# Protocol and Schema Compatibility

**Status:** Draft, protocol version 0.21.0-draft.

This document tells controllers, adapters, and clients when they can safely communicate. The words MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY are normative.

## 1. Keep version numbers separate

These version fields name different contracts. Never substitute one for another:

- `protocol_version` identifies the language-neutral semantics a peer negotiated.
- `contract_versions` identifies adapter-category contracts.
- A capability's `version` identifies its input, output, and behavior contract.
- `implementation_version` identifies a controller or adapter build.
- A schema `$id` identifies one machine-readable schema. Its file path only tells you where it is distributed.
- The management API version identifies the client-facing HTTP API. It evolves independently of RPC and protocol versions.

A build number or implementation version cannot stand in for protocol compatibility.

## 2. Negotiate explicitly and fail closed

Peers advertise supported protocol versions and select exactly one version present in both sets. Record that version in the session and every durable operation created through it.

After a failed negotiation response, do not retry initialization with an older version to bypass validation, authorization, or policy. Fix the cause or report incompatibility.

A peer MUST NOT:

- Pick the newest version without checking the other peer's supported list.
- Downgrade after a validation or authorization error.
- Interpret an unknown protocol, lifecycle, operation, or capability version as the nearest known version.
- Change message meaning based on software version.

If no common version exists, initialization fails with a structured protocol-version compatibility error. A deployment MAY set a stricter minimum than the negotiated minimum.

Capability negotiation is separate. An adapter advertises exact capability names and versions; the controller invokes only versions it has validated and authorized. Missing or incompatible capabilities return an unsupported result. Do not emulate them silently.

## 3. Draft and stable policy

Version `0.21.0-draft` is unstable and pre-1.0. It provides no stable compatibility guarantee, but `protocol_version` still identifies the negotiated wire contract. Peers MUST NOT advertise the same version for different accepted messages or required behavior.

While the protocol is in design, the untagged branch is development source, not a compatibility target for independent implementations. Each published draft snapshot MUST be immutable and use a new SemVer-shaped `0.y.0-draft` version. The project increments the minor component for each pre-1.0 snapshot. A breaking change uses a new protocol version and updates every version-scoped schema ID in the same change. For example, an incompatible change after `0.9.0-draft` uses `0.21.0-draft`. Update examples, fixtures, and the schema inventory with each snapshot.

Use `-draft` while designing protocol semantics and before a runnable implementation demonstrates conformance. The project reserves `-alpha` for an early controller or adapter under evaluation. These are maturity labels, not different compatibility guarantees; both are unstable prereleases.

A build from an untagged tree MUST record its exact commit or source revision in deployment metadata and SHOULD store it with each durable operation. The revision or package digest identifies the artifact for reproducibility; it does not replace the negotiated protocol version. Interoperating implementations pin the same published protocol package or another explicitly agreed immutable snapshot.

After 1.0, follow SemVer: increment major for incompatible changes, minor for compatible additions or deprecations, and patch for compatible fixes or clarifications. A change is breaking if it rejects a previously valid message, narrows a safety-critical enum, makes an optional field required, or changes a field's meaning.

## 4. Schema package rules

The schema index lists every schema in a protocol package. Implementations validate against the package selected for the negotiated version; do not mix schemas from different protocol versions to make a message pass.

Schema IDs are unique within a package. A schema reference resolves through the package's local registry or an explicitly declared in-package reference. Validators MUST NOT make arbitrary network requests to resolve protocol schema IDs.

Once a package is tagged, its schema content and IDs are immutable. A correction creates a new package revision and, if validation meaning changes, a new version-scoped schema ID. To reproduce an earlier operation, retain the package version or digest used when it was accepted.

Generated bindings MAY come from schemas, but the schemas and protocol text remain authoritative. Generated code cannot add semantics absent from them.

## 5. Evolve objects and enums safely

Every schema states whether its object is closed or extensible, and where.

- Request and command objects, including nested parameters, are closed unless they define a namespaced `extensions` map. Reject unknown fields otherwise.
- Read-only `lifecycle:server` and `operations:operation` resources are extensible only at the top level. They MAY gain optional top-level properties; nested targets, steps, observations, progress, and other objects stay closed unless their own schema says otherwise.
- On an extensible response resource, clients ignore unknown optional fields if those fields are not needed for a safety decision. Unknown fields cannot change known meanings or supply a safety precondition.
- Keep lifecycle, operation, instance, runtime, and other safety-critical enums closed. An unknown value affecting routing, authorization, destruction, or recovery fails safely and SHOULD use `core.compatibility.unknown_state`. Never guess or coerce it.
- Represent new required behavior with a new protocol/capability version or a new required capability. Do not infer it from implementation version.

## 6. Recover operations across upgrades

An accepted operation stays bound to the protocol version and validated request under which it began. Keep enough version and request metadata to resume or reconcile it after restart or upgrade.

Every dispatched adapter step retains the exact adapter identity, capability name, and capability version. The implementation MUST do one of these:

- Continue with the recorded semantics and exact capability version.
- Reconcile external state and migrate through an explicit, tested migration.
- Suspend the operation for operator action when the recorded contract cannot be honored.

Do not substitute a different capability version by inference. Without an explicit tested migration, suspend rather than replay or reinterpret the step.

A migration preserves operation identity, idempotency binding, completed steps, external references, warnings, errors, and audit history. It MUST NOT repeat an external effect because the implementation changed.

## 7. Management API compatibility

The management API path version, currently `/v1`, is independent of protocol and schema package versions. API responses exposing durable operations preserve each operation's `protocol_version` and identity.

A management API revision cannot silently change adapter RPC semantics. A controller MAY support multiple management API versions over one protocol implementation, provided the controller still centralizes authorization and safety policy.

## 8. Deprecate and remove deliberately

Before removing a stable contract, maintainers document:

- The exact field, state, method, or capability being deprecated.
- Its replacement and a migration example.
- The first deprecated version and earliest version that may remove it.
- A conformance fixture proving the replacement behavior.

Security fixes and changes needed to prevent data loss or unsafe routing may require faster removal. Document the risk, migration effect, affected versions, and safest available path.

## 9. Release gate

For every protocol release, validate:

- Schema-index uniqueness, IDs, paths, and local references.
- Every published example against its declared schema.
- Fixture structure and expected safety behavior.
- Exact negotiation, no-overlap failure, and no downgrade after an error.
- Unknown optional top-level fields on read-only server/operation resources are tolerated; requests and nested closed objects reject them.
- Unknown safety-critical enum/state values fail safely.
- Replacing a destroyed instance preserves logical identity while using a distinct infrastructure identity.
- Required readiness is complete and bound to the exact target.
- In-flight operations recover across controller/adapter upgrades using recorded protocol and capability versions, without replaying completed effects.

The initial Go implementation MUST pass the published conformance suite before release. Other-language implementations are welcome and may run the same suite, but a second language is not a release requirement. A schema-valid message alone does not prove behavioral interoperability.