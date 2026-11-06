# Protocol and Schema Compatibility

**Status:** Draft, protocol version 0.1.0-draft.

This document defines how independent controllers, adapters, and clients decide whether they can communicate safely. It is normative where it uses MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY.

## 1. Keep version domains separate

The following versions describe different contracts and MUST NOT be treated as interchangeable:

- `protocol_version` identifies the language-neutral protocol semantics negotiated by a peer.
- `contract_versions` identifies the adapter-category contract revisions implemented by an adapter.
- A capability's `version` identifies that capability's input, output, and behavior contract.
- `implementation_version` identifies a particular controller or adapter build.
- A schema `$id` identifies one machine-readable schema. Its package path is a distribution location, not its identity.
- The management API version identifies the client-facing HTTP API. It evolves independently of adapter RPC and protocol versions.

A build number or implementation version MUST NOT be used as a substitute for protocol compatibility.

## 2. Negotiation is explicit and fail-closed

Peers MUST advertise the protocol versions they support and MUST select a version present in both sets. The selected version MUST be recorded in the session and in every durable operation created through that session.

A peer MUST NOT silently:
- Select the newest version without checking the other peer's supported set.
- Downgrade to an older version after a validation or authorization error.
- Interpret an unknown protocol, lifecycle, operation, or capability version as the nearest known version.
- Change message meaning based on implementation version.

If no mutually supported version exists, initialization MUST fail with a structured protocol-version compatibility error. A deployment MAY require a configured minimum version stricter than the negotiated minimum.

Capability negotiation is independent. An adapter advertises exact capability names and versions; the controller invokes only a version it has validated and authorized. Missing or incompatible capabilities fail as unsupported rather than being emulated silently.

## 3. Draft and stable compatibility policy

The current `0.1.0-draft` is pre-1.0. It is not covered by a stable compatibility guarantee. During the draft, maintainers MAY make incompatible changes, but MUST:
1. Update the protocol version and all version-scoped schema IDs together when the change is incompatible.
2. Document the change and migration impact.
3. Update examples, conformance fixtures, and the schema inventory in the same change.
4. Preserve a tagged or commit-pinned copy of each published draft snapshot so an implementation can validate the exact contract it negotiated.

The moving, untagged `0.1.0-draft` working branch is not an immutable package: maintainers may revise it, including incompatibly, while design is in progress. The protocol version string alone therefore does not identify the exact working-tree contents. An implementation built from an untagged draft MUST retain its exact source/specification revision in deployment metadata and SHOULD store that revision with each durable operation. Consumers MUST NOT assume that two builds advertising the same draft string necessarily use identical schema content.

Before publishing a versioned draft snapshot, maintainers MUST assign a protocol version to that snapshot and update version-scoped schema IDs together. A published tag or package is immutable. Any later incompatible change requires a new protocol version and new version-scoped schema IDs. Draft consumers SHOULD pin a release tag, package digest, or commit and MUST NOT rely on the moving default branch.

After protocol 1.0:
- A **major** version change may remove or change the meaning of fields, values, states, capability behavior, or required safety guarantees.
- A **minor** version may add optional fields, new operation types, new capabilities, and new enum values only where the relevant object is explicitly extensible and consumers are required to handle unknown values safely.
- A **patch** version may clarify text or fix an implementation-neutral defect only when it does not change accepted messages, required behavior, or safety outcomes.

A change that causes a previously valid message to be rejected is not a patch-level change. Narrowing an enum, making an optional field required, or changing a field's meaning is breaking.

## 4. Schema package rules

The schema index is the authoritative list of schemas shipped in a protocol package. Implementations MUST validate against the schema package selected for the negotiated protocol version; they MUST NOT mix schemas from different protocol versions to make a message pass.

Schema IDs MUST be unique within a package. A schema reference MUST resolve through the package's local registry or an explicitly declared in-package reference. Validators MUST NOT perform arbitrary network retrieval to resolve protocol schema IDs.

Once a protocol package is tagged as a release, its schema content and IDs are immutable. A correction creates a new package revision and, where its validation semantics change, a new version-scoped schema ID. A consumer that must reproduce an earlier operation SHOULD retain the package version or schema-package digest used when that operation was accepted.

Generated language bindings MAY be produced from the schemas, but generated code is not authoritative and MUST NOT introduce semantics absent from the protocol documents.

## 5. Object and enum evolution

Each schema MUST state whether an object is closed or extensible.

- Closed request and command objects reject unknown fields. An extension MUST be introduced by a reviewed protocol change or an explicitly defined namespaced `extensions` map.
- Extensible read-only response objects MAY add optional fields. Clients MUST ignore unknown optional fields when the field is not needed for a safety decision.
- Unknown enum values or lifecycle states that affect routing, authorization, destructive actions, or recovery MUST cause a safe refusal or operator-visible compatibility error. They MUST NOT be guessed or mapped to a known value.
- A new required behavior MUST be represented by a new protocol/capability contract version or a new required capability, not inferred from implementation version.

## 6. Durable operations across upgrades

An accepted operation is bound to the protocol version and validated request under which it was created. The controller MUST retain enough version information and request metadata to resume or reconcile it after a process restart or software upgrade.

An implementation MUST NOT reinterpret an in-flight operation using newer defaults. It MUST either:
- Continue using the recorded operation semantics and compatible adapter capability versions;
- Reconcile the external state and migrate the operation through an explicitly documented, tested migration; or
- Suspend the operation for operator intervention.

A migration MUST preserve operation identity, idempotency binding, completed steps, external references, warnings, errors, and audit history. It MUST NOT repeat an external side effect merely because the implementation changed.

## 7. Management API compatibility

The management API's URL version (currently `/v1`) is independent of the protocol and schema package versions. API responses that expose durable operations MUST preserve the operation's own `protocol_version` and operation identity.

A client-facing API revision MUST NOT silently change adapter RPC semantics. A controller MAY support multiple management API versions over one protocol implementation, provided authorization and safety policy remain centralized in the controller.

## 8. Deprecation and removal

Before a stable contract is removed, maintainers SHOULD provide:
- A deprecation notice identifying the exact field, state, method, or capability.
- A replacement and migration example.
- The first version in which deprecation applies and the earliest version in which removal is permitted.
- A conformance fixture proving the replacement behavior.

Security fixes and changes needed to prevent data loss or unsafe routing MAY require accelerated removal. Such changes MUST document the risk and migration impact.

## 9. Compatibility release gate

Every protocol release MUST validate:
- Schema index uniqueness, IDs, paths, and local references.
- All published examples against their declared schemas.
- Conformance fixture structure and expected safety behavior.
- Negotiation success and no-overlap failure.
- Unknown optional fields and unknown safety-critical enum/state values.
- In-flight operation recovery when the controller or adapter implementation version changes.

Before 1.0, at least two independently built implementations in different languages MUST pass the same published conformance suite. A schema-valid message alone is not evidence of behavioral interoperability.
