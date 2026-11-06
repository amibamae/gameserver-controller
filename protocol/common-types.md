# Common Protocol Types

**Status:** Draft, version 0.15.0-draft. These definitions establish shared meanings and constraints for the JSON-based protocol profile.

This document is the common vocabulary used by adapter contracts, lifecycle operations, and the future management API. Language-specific implementations MUST preserve these meanings.

## 1. General rules

- Values have one documented meaning and unit.
- Identifiers are opaque unless a type explicitly defines their structure.
- A receiver MUST NOT infer missing values from local defaults when doing so could change safety, authorization, or resource targeting.
- Optional, absent, and explicitly empty values are distinct where the contract says they are.
- Timestamps represent instants, not local wall-clock times.
- Secret values MUST NOT appear in ordinary protocol data.
- The interchange representation is JSON (RFC 8259); structural schemas use JSON Schema Draft 2020-12.
- Duplicate object member names MUST be rejected.
- JSON numeric values MUST remain within the exact interoperable integer range ±9,007,199,254,740,991. Larger counters and byte quantities MUST be decimal strings.
- Protocol schemas use RFC 3339 date-time values for instants. Implementations MUST validate formats even when a JSON Schema validator treats format as annotation only.
- Canonical request fingerprints MUST use RFC 8785 JCS when a deterministic JSON digest is required; ordinary transmitted JSON does not need canonical property ordering.

## 2. Identifiers

### 2.1 General identifier

An identifier is a non-empty, bounded string that uniquely identifies an entity within its declared scope.

Implementations MUST treat identifiers received from another implementation as opaque. They MUST NOT parse, normalize, case-fold, or derive meaning from an identifier unless its specific type defines that behavior.

The protocol does not mandate UUID, ULID, or another generation algorithm. Implementations SHOULD use collision-resistant identifiers and MUST document their uniqueness scope. Unless a field schema specifies otherwise, identifiers are limited to 200 UTF-8 characters.

### 2.2 Identifier types

| Type | Identifies | Scope / notes |
|---|---|---|
| `server_id` | Logical game server | Stable across replacement instances |
| `environment_id` | Environment such as production or staging | Controller installation scope |
| `instance_id` | Concrete hosting instance | Adapter + environment scope |
| `backup_id` | A controller-tracked backup | Controller installation scope |
| `release_id` | Immutable software/configuration release | Controller installation scope |
| `operation_id` | One requested lifecycle operation | Unique within controller installation |
| `correlation_id` | Related messages/events across a workflow | Propagated unchanged |
| `request_id` | One request/response exchange | Unique within its communication context |
| `adapter_id` | Adapter implementation identity | Stable reverse-domain-style name recommended |
| `approval_id` | Explicit approval record | Controller installation scope |
| `audit_event_id` | One audit event | Controller installation scope |

A logical `server_id` MUST NOT be reused merely because its underlying provider instance was destroyed and recreated. An `instance_id` identifies the concrete instance and changes when a replacement instance is created.

### 2.3 External resource references

Provider-native IDs are represented as opaque external references, not controller identifiers.

An external resource reference contains:
- Adapter ID.
- Environment ID.
- Resource kind.
- Provider-native identifier as an opaque string.

The adapter and environment scope MUST be checked before an external reference is used. A provider-native identifier alone is insufficient to authorize an action.

Illustrative representation:

```json
{
  "adapter_id": "hosting.vultr",
  "environment_id": "production",
  "resource_kind": "instance",
  "external_id": "provider-assigned-value"
}
```

## 3. Time and duration

### 3.1 Timestamp

A timestamp represents an absolute instant.

The serialized representation MUST use RFC 3339 with an explicit UTC offset. Protocol-generated timestamps MUST be normalized to UTC (for example, `2026-11-05T14:30:00Z`). Leap-second handling MUST follow RFC 3339 and the implementation's documented time library behavior.

- A timestamp MUST include timezone/offset information.
- Local timestamps without an offset MUST be rejected for protocol instants.
- Implementations MUST NOT compare timestamps as localized display strings.
- Clock source and clock-skew assumptions must be documented for security-sensitive approval and expiry checks.

### 3.2 Duration and deadlines

Durations are elapsed time intervals, not timestamps. Durations in JSON MUST be non-negative integer milliseconds unless a field explicitly defines another unit. Field names MUST include the unit. The value MUST remain within the interoperable JSON integer range. A deadline is an absolute RFC 3339 timestamp; elapsed-time measurement MUST use a monotonic clock locally.

A deadline is an absolute timestamp by which an operation or request should stop waiting or initiating further work. A deadline does not prove that an external provider operation has been cancelled or reversed.

## 4. Versions

### 4.1 Protocol version

The protocol has a version independent of the software release version.

The draft uses a SemVer-shaped compatibility model:
- Major: incompatible contract or behavior change.
- Minor: backward-compatible additions.
- Patch: clarifications or corrections that do not change interoperability.

The 0.15.0-draft protocol version is a SemVer 2.0.0 identifier with the `-draft` prerelease label. Implementations advertise supported exact protocol versions and negotiate one exact mutually supported version during initialization. For the 0.x draft series, no implicit compatibility across versions is assumed. A stable 1.x compatibility rule will be finalized before 1.0. Stable 1.0 compatibility rules require review.

### 4.2 Schema version

A schema version identifies the revision of a particular serialized data structure. It is distinct from protocol version and application version.

A schema MUST identify its owning protocol area and version. A schema change cannot be called backward-compatible merely because a particular language parser ignores unknown fields.

### 4.3 Implementation version

An implementation version identifies a controller or adapter software build. It MUST NOT be confused with the protocol version it supports.

### 4.4 Game and release versions

Game versions are game-specific values and MUST be treated as opaque strings by the common protocol unless a game adapter provides a documented comparison/compatibility rule. The core MUST NOT assume that every game's versions follow semantic versioning.

## 5. Adapter identity and descriptor

An adapter descriptor identifies an implementation and the contract surface it offers.

Required conceptual fields:

| Field | Meaning |
|---|---|
| `adapter_id` | Stable unique adapter identity |
| `display_name` | Human-readable name |
| `implementation_version` | Adapter software version |
| `protocol_versions` | Exact supported protocol version strings |
| `contract_versions` | Supported contract/schema versions |
| `capabilities` | Explicit list of supported capabilities |
| `required_permissions` | Permission scopes needed to perform declared capabilities |

Optional fields may include vendor, documentation reference, configuration schema reference, platform constraints, supported resource kinds, and diagnostic information.

A descriptor is descriptive, not an authorization grant. Core policy decides whether the adapter may be used and which permissions are actually provided.

## 6. Capabilities

A capability is a named, versioned declaration that an implementation supports a defined operation or behavior.

Capability names MUST be stable, namespaced identifiers, such as:
- `hosting.instance.create`
- `hosting.instance.inspect`
- `hosting.instance.destroy`
- `storage.object.put`
- `storage.object.verify_checksum`
- `execution.unit.graceful_stop`
- `game.readiness.check`
- `integration.proxy.backend.drain`

A capability declaration SHOULD include:
- Capability name and version.
- Supported resource kinds.
- Constraints and limits.
- Required permission scope.
- Whether the capability is read-only or mutating.
- Idempotency and cancellation characteristics where relevant.

The presence of a capability does not authorize its use. The core MUST separately validate actor permissions, environment policy, approvals, and operation preconditions.

Capabilities MUST NOT be inferred from adapter identity or vendor name. Optional features must be explicitly declared. If a requested capability is absent, the result is a structured unsupported-capability error.

## 7. Structured errors

Errors are machine-readable outcomes, not merely human-readable strings.

A protocol error SHOULD contain:
- Stable `code`.
- Safe human-readable `message`.
- `category`.
- `retryable` indication.
- Optional `details` containing documented, non-secret fields.
- Optional `correlation_id` and `operation_id`.
- Optional recovery guidance or operator action.

Error messages and details MUST NOT contain credentials, secret values, private keys, raw authorization headers, or unnecessary player data.

### 7.1 Initial error categories

| Category | Meaning |
|---|---|
| `validation` | Request or configuration is invalid |
| `authorization` | Actor or adapter lacks permission |
| `unsupported_capability` | Required capability is not declared/supported |
| `not_found` | Target resource does not exist in the stated scope |
| `conflict` | Request conflicts with current state or another operation |
| `transient` | Temporary condition may permit retry |
| `provider_failure` | External provider reports a failure |
| `integrity_failure` | Data or artifact verification failed |
| `compatibility_failure` | Versions or formats are incompatible |
| `safety_blocked` | Policy or safety precondition prevented the action |
| `timeout` | Local wait/deadline expired; external outcome may be unknown |
| `cancelled` | Operation was cancelled according to its defined cancellation semantics |
| `internal` | Unexpected controller/adapter failure |

Stable error codes SHOULD be namespaced by protocol area, for example `core.validation.invalid_request` or `hosting.instance.not_found`. Human-readable messages may change; clients MUST branch on stable codes, not message text.

A timeout on a mutating external call MUST be treated as an uncertain outcome until the adapter reconciles the provider state. It MUST NOT be blindly retried if doing so could duplicate or broaden a destructive effect.

## 8. Operation and request context

The common request context is the shared metadata used to correlate work. Exact wire placement is deferred.

It may include:
- `request_id`: one request/response exchange.
- `correlation_id`: end-to-end workflow correlation.
- `operation_id`: durable lifecycle operation, when the request belongs to one.
- `server_id` and `environment_id`: target scope where applicable.
- `deadline`: absolute time limit.
- `idempotency_key`: deduplication key for a supported mutating operation.
- Actor/authorization context supplied by the trusted core, not self-asserted by an untrusted adapter or client.

An idempotency key is scoped to the authenticated caller, operation type, and target unless a contract defines a narrower scope. Reusing a key with different parameters MUST be rejected as a conflict.

## 9. Secret references

A secret reference identifies where an authorized component can resolve a secret without embedding the secret in protocol data. The JSON structure is defined by [secret-reference.schema.json](schemas/common/secret-reference.schema.json); it contains a URI-like opaque reference, purpose, optional permission scopes, and optional version reference.

A secret reference is not itself proof of permission to read the secret. Resolution is governed by the controller's secret policy and least-privilege rules.

Implementations MUST:
- Avoid logging secret references if their names reveal sensitive infrastructure details.
- Never serialize resolved secret values in ordinary requests, responses, operation records, or audit events.
- Restrict resolution to the component and operation that require the secret.
- Fail closed if resolution or authorization fails.

## 10. JSON and schema profile

JSON is encoded as UTF-8 and follows RFC 8259. Duplicate object member names, invalid Unicode, NaN, and infinity MUST be rejected. Identifiers remain strings even when a provider uses numeric identifiers.

JSON Schema Draft 2020-12 files are stored under [schemas](schemas/README.md). Schema `format` values such as `date-time` and `uri` MUST be actively validated by a conforming protocol implementation. A validator configured only for format annotations is insufficient unless the implementation performs equivalent checks.

Protocol integers MUST remain within the exact interoperable JSON integer range, ±9,007,199,254,740,991. Larger counters and byte quantities MUST be represented as decimal strings. Durations use non-negative integer milliseconds with the unit in the field name.

Canonical request fingerprints MUST use RFC 8785 JCS when a deterministic JSON digest is required. Ordinary transmitted JSON need not be canonicalized.

## 11. Extensibility and unknown fields

Request/control objects are closed by default; unknown fields are rejected unless the schema defines an explicit `extensions` map. Extension names MUST be namespaced and MUST NOT change standard field meaning or security effect.

Read-only response objects MAY gain optional fields in a compatible minor revision. Clients MUST ignore unknown optional response properties but MUST fail safely on unknown lifecycle states, operation statuses, capability semantics, or enum values that affect a decision.

Schemas MUST state whether an object is closed or extensible. A minor revision MUST NOT change the meaning, type, or security effect of an existing field.

## 12. Open decisions

The following remain intentionally unresolved:
- Identifier generation algorithm; identifiers remain opaque and must be collision-resistant within their declared scope.
- Retention duration and conflict behavior for expired idempotency keys.
- Final capability-version compatibility and deprecation policy.
- Full stable error-code registry and extension governance.
- Maximum lengths and retention rules for operation/event/audit histories.
- Final remote adapter authentication profile.
- Whether an optional remote adapter binding is needed beyond local in-process and stdio profiles.

These decisions MUST be resolved or explicitly declared out of scope before stable 1.0. They MUST NOT be decided implicitly by an implementation language.
