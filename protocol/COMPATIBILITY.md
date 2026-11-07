# Protocol and Schema Compatibility

**Status:** Draft, protocol version 0.21.0-draft.

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

Peers MUST advertise the protocol versions they support and MUST select exactly one version present in both sets. The selected version MUST be recorded in the session and in every durable operation created through that session. A peer MUST NOT retry initialization with a lower version after any failed negotiation response to bypass validation, authorization, or policy checks; the caller must correct the cause or report incompatibility.

A peer MUST NOT silently:
- Select the newest version without checking the other peer's supported set.
- Downgrade to an older version after a validation or authorization error.
- Interpret an unknown protocol, lifecycle, operation, or capability version as the nearest known version.
- Change message meaning based on implementation version.

If no mutually supported version exists, initialization MUST fail with a structured protocol-version compatibility error. A deployment MAY require a configured minimum version stricter than the negotiated minimum.

Capability negotiation is independent. An adapter advertises exact capability names and versions; the controller invokes only a version it has validated and authorized. Missing or incompatible capabilities fail as unsupported rather than being emulated silently.

## 3. Draft and stable compatibility policy

The current `0.21.0-draft` is an unstable pre-1.0 protocol. It does not provide a stable compatibility guarantee, but `protocol_version` still identifies the wire contract used during negotiation. Peers MUST NOT advertise the same protocol version for different accepted messages or required behavior.

While the protocol is in design, the moving, untagged branch is development source and MUST NOT be used as a compatibility target between independent implementations. Each published draft snapshot MUST be immutable and MUST use a new SemVer-shaped `0.y.0-draft` version; this project increments the minor component for each published pre-1.0 snapshot. A breaking change MUST use a new protocol version and update all version-scoped schema IDs in the same change. For example, an incompatible change after `0.9.0-draft` uses `0.21.0-draft`. Examples, conformance fixtures, and the schema inventory MUST be updated with the snapshot.

A `-draft` prerelease label is used while protocol semantics are being designed and no runnable implementation has demonstrated conformance. The project reserves `-alpha` for a runnable early controller or adapter implementation under evaluation; these labels are project maturity signals, not different compatibility guarantees. Both remain unstable prereleases.

A development build from an untagged source tree MUST retain its exact commit or source revision in deployment metadata and SHOULD store it with each durable operation. The exact revision or package digest identifies artifact contents for reproducibility; it MUST NOT substitute for the negotiated `protocol_version`. Implementations intended to interoperate MUST pin the same published protocol package or another explicitly agreed immutable snapshot.

After protocol 1.0, versions follow SemVer: a major version MUST increment for backward-incompatible changes; a minor version MUST increment for backward-compatible public additions or deprecations; a patch version MUST increment only for backward-compatible fixes or clarifications. A change that causes a previously valid message to be rejected, narrows a safety-critical enum, makes an optional field required, or changes a field's meaning is breaking.


## 4. Schema package rules

The schema index is the authoritative list of schemas shipped in a protocol package. Implementations MUST validate against the schema package selected for the negotiated protocol version; they MUST NOT mix schemas from different protocol versions to make a message pass.

Schema IDs MUST be unique within a package. A schema reference MUST resolve through the package's local registry or an explicitly declared in-package reference. Validators MUST NOT perform arbitrary network retrieval to resolve protocol schema IDs.

Once a protocol package is tagged as a release, its schema content and IDs are immutable. A correction creates a new package revision and, where its validation semantics change, a new version-scoped schema ID. A consumer that must reproduce an earlier operation SHOULD retain the package version or schema-package digest used when that operation was accepted.

Generated language bindings MAY be produced from the schemas, but generated code is not authoritative and MUST NOT introduce semantics absent from the protocol documents.

## 5. Object and enum evolution

Each schema MUST state whether an object is closed or extensible and the scope of that extensibility.

- Request and command objects, including nested request parameters, MUST be closed unless they define a specific namespaced `extensions` map. Unknown fields in those objects are rejected.
- The read-only `lifecycle:server` and `operations:operation` resource schemas are extensible only at their top level. They MAY add unknown optional top-level properties; their nested targets, steps, observations, progress, and other objects remain closed unless their own schema explicitly says otherwise.
- Clients MUST ignore unknown optional fields on an extensible response resource when those fields are not needed for a safety decision. Unknown fields MUST NOT change the meaning of known fields or supply a required safety precondition.
- Lifecycle, operation, instance, runtime, and other safety-critical status/state enums remain closed. An unknown value that affects routing, authorization, destructive actions, or recovery MUST fail safely and SHOULD be reported with `core.compatibility.unknown_state`; it MUST NOT be guessed, coerced, or mapped to a known value.
- A new required behavior MUST be represented by a new protocol/capability contract version or a new required capability, not inferred from implementation version.

## 6. Durable operations across upgrades

An accepted operation is bound to the protocol version and validated request under which it was created. The controller MUST retain enough version information and request metadata to resume or reconcile it after a process restart or software upgrade.

An implementation MUST NOT reinterpret an in-flight operation using newer defaults. Every dispatched adapter step MUST retain the exact adapter identity and capability name and version used when it was accepted. The implementation MUST either:
- Continue using the recorded operation semantics and that exact capability version;
- Reconcile the external state and migrate the operation through an explicitly documented, tested migration; or
- Suspend the operation for operator intervention when the recorded semantics or capability version cannot be honored.

It MUST NOT substitute a newer or older capability version by inference. If no explicit tested migration exists, it MUST suspend rather than replay or reinterpret the step.

A migration MUST preserve operation identity, idempotency binding, completed steps, external references, warnings, errors, and audit history. It MUST NOT repeat an external side effect merely because the implementation changed.

## 7. Management API compatibility

The management API's URL version (currently `/v1`) is independent of the protocol and schema package versions. API responses that expose durable operations MUST preserve the operation's own `protocol_version` and operation identity.

A client-facing API revision MUST NOT silently change adapter RPC semantics. A controller MAY support multiple management API versions over one protocol implementation, provided authorization and safety policy remain centralized in the controller.

## 8. Deprecation and removal

Before a stable contract is removed, maintainers MUST provide:
- A deprecation notice identifying the exact field, state, method, or capability.
- A replacement and migration example.
- The first version in which deprecation applies and the earliest version in which removal is permitted.
- A conformance fixture proving the replacement behavior.

Security fixes and changes needed to prevent data loss or unsafe routing MAY require accelerated removal. Such changes MUST document the risk and migration impact, the affected versions, and the safest available migration path.

## 9. Compatibility release gate

Every protocol release MUST validate:
- Schema index uniqueness, IDs, paths, and local references.
- All published examples against their declared schemas.
- Conformance fixture structure and expected safety behavior.
- Exact protocol negotiation, no-overlap failure, and refusal to downgrade after an error.
- Tolerance of unknown optional top-level fields on read-only server/operation resources, while requests and nested closed objects reject them.
- Safe refusal for unknown safety-critical enum/state values.
- Destroyed-server replacement preserving logical identity while using a distinct infrastructure identity.
- Required readiness completeness and exact target binding.
- In-flight operation recovery across controller/adapter upgrades using the recorded protocol and exact capability versions without replaying completed effects.

The initial Go implementation MUST pass the published conformance suite before release. Other-language implementations are welcome community contributions and MAY run the same suite, but a second language is not a release requirement. A schema-valid message alone is not evidence of behavioral interoperability.
