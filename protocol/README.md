# Protocol Specification

**Status:** Reviewed Phase 1 draft, version `0.21.0-draft`. The repo includes normative draft text, JSON Schemas, RPC and management API bindings, examples, and conformance fixtures. Go CI checks schema structure and references, examples, OpenAPI, and fixtures. The Go operation store, acceptance service, local schema registry, and in-process adapter boundary exist. Durable workflow dispatch, the management runtime, and production adapters are still work to do.

## Purpose

Game Server Controller uses language-neutral contracts so the core, adapters, CLI, and future management clients can be replaced or built independently. The written protocol, schemas, and fixtures define interoperability; no one implementation language does.

## Project commitments

1. **Language-neutral contracts:** Protocol definitions do not depend on Go, Rust, Python, C, Bash, or another language.
2. **Independent implementations:** Two implementations of the same protocol version agree on message meaning, validation, transitions, errors, and safety behavior.
3. **No AI dependency:** Protocol tools, generation, validation, builds, and routine operation do not require an AI service or AI-generated artifact.
4. **No required web panel:** The controller works without a browser UI. A panel is not an initial release requirement.
5. **Independent clients:** The project should publish a versioned management API so others can build web, desktop, mobile, or command-line clients.
6. **Core authority:** Clients and adapters cannot bypass authorization, lifecycle policy, environment isolation, approval gates, audit, or consent.
7. **Declared capabilities:** Implementations state what they support. Unsupported work fails explicitly rather than being approximated.
8. **Safe failure:** Timeouts, interruptions, partial results, and ambiguous outcomes remain explicit. A timeout does not prove that a destructive action did or did not happen.
9. **Versioned evolution:** Document compatibility rules for protocol and schema changes. Before 1.0, each published draft snapshot gets a new minor version; after 1.0, breaking changes require a major version.
10. **Secret minimization:** Messages use secret references or protected channels, never serialized secret values. Logs omit secrets.
11. **Privacy by default:** Separate required operations from optional telemetry. Optional telemetry starts disabled; identifiable player telemetry requires separate informed consent from the server owner and each affected player.
12. **Transport independence:** Keep message meaning and schemas independent of framing and delivery wherever practical. A binding can define authentication, connection life, and delivery without redefining operations.

The terms MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY carry their usual standards meaning.

## Protocol areas

- **Common types:** identifiers, timestamps, versions, resource references, structured errors, pagination.
- **Identity and capabilities:** implementation identity, compatibility, declared features, constraints, and permissions.
- **Operations:** durable acceptance, steps, status, progress, idempotency, retries, cancellation, reconciliation, and recovery.
- **Lifecycle:** server states, transitions, preconditions, postconditions, and failure behavior.
- **Adapter contracts:** hosting, storage, execution, game runtime, integrations.
- **Management API:** versioned client operations, read models, authentication, authorization, and events.
- **Security and privacy:** trust boundaries, secrets, consent, audit, environment isolation.
- **Compatibility:** version negotiation, schema package identity, evolution, deprecation, safe recovery.
- **Conformance:** shared fixtures and behavioral requirements.

## Two communication surfaces

### Adapter protocol

This connects the core to hosting, storage, execution, game, and integration implementations. Adapters follow core policy and receive only the permissions and secret references required for their capabilities.

The first optional process binding is `jsonrpc-stdio-v1`, described in [rpc/transport.md](rpc/transport.md). In-process adapters may implement the same logical contracts without a wire transport, as long as they pass the same conformance requirements.

### Management API

This gives operators and independent clients authorized access to controller capabilities and operation state. It does not expose unrestricted provider credentials or adapter internals.

Document the API independently of any UI. A hosted web panel is out of scope for the initial project.

## Standards in draft 0.21.0

These language-neutral selections are recorded in [ADR 0004](../docs/decisions/0004-protocol-encoding-and-bindings.md). Review them again before 1.0 if interoperability evidence shows a material problem.

| Concern | Selection | Role |
|---|---|---|
| Interchange | [JSON / RFC 8259](https://www.rfc-editor.org/rfc/rfc8259) | Protocol objects and examples |
| Structural schemas | [JSON Schema 2020-12](https://json-schema.org/draft/2020-12) | Machine-readable validation |
| Timestamps | [RFC 3339](https://www.rfc-editor.org/rfc/rfc3339) | Absolute instants |
| Canonicalization | [RFC 8785 JCS](https://www.rfc-editor.org/rfc/rfc8785) | Deterministic fingerprints where required |
| Adapter RPC | [JSON-RPC 2.0](https://www.jsonrpc.org/specification) | Request/response semantics |
| Optional process binding | `jsonrpc-stdio-v1` | UTF-8 newline-delimited JSON on stdin/stdout |
| Management API | [OpenAPI 3.2.1](https://spec.openapis.org/oas/v3.2.1) | HTTP/JSON client API |

JSON Schema's `format` keyword is only an annotation unless assertion is enabled. Implementations must validate required protocol formats such as RFC 3339 timestamps and URIs through format assertion or equivalent checks.

Protocol integers stay within the exact interoperable JSON range, ±9,007,199,254,740,991. Represent larger counters, provider IDs, and byte quantities as decimal strings.

The adapter contract works in-process. The optional stdio profile does not require subprocess adapters. Any remote adapter transport needs its own reviewed binding.

## Unknown fields and extension rules

Request and security-sensitive command objects are closed. Reject unknown fields unless the object defines an explicit namespaced `extensions` map. Extensions cannot change standard fields.

Read-only responses may add optional fields in a compatible minor revision. Clients ignore unknown optional response fields and fail safely on unknown enum/state values that affect a decision. Schemas state which objects are closed or extensible. A minor release cannot change an existing field's meaning, type, or security effect.

## Conformance and compatibility

A conforming implementation:

- Identifies its supported protocol and schema versions.
- Validates messages before acting.
- Safely rejects malformed input and unsupported required fields.
- Preserves opaque IDs without parsing provider-specific formats.
- Returns documented structured errors.
- Honors deadlines, cancellation, idempotency, and recovery rules.
- Avoids side effects during inspection and validation unless the contract says otherwise.
- Passes language-neutral tests and shared fixtures.
- Meets the same safety and privacy rules as the core spec.

[Compatibility.md](COMPATIBILITY.md) defines negotiation and schema evolution. It is still a draft and needs review before protocol 1.0.

## Design constraints

- No web frontend or hosted panel is required.
- This specification does not choose a programming language, database, broker, or distributed-coordination service.
- No adapter decides on its own that an unverified backup is safe to destroy.
- No client directly invokes destructive provider operations outside core authorization and operation tracking.
- No operation weakens production/staging isolation or consent rules.
- Before a mutating external action, the controller durably records the operation.
- Reconcile an unknown outcome before retrying or starting dependent work.

## Specification layout

```text
protocol/
├── README.md
├── VERSION
├── common-types.md
├── operations.md
├── lifecycle.md
├── security.md
├── adapters/
│   ├── README.md
│   ├── hosting.md
│   ├── storage.md
│   ├── execution.md
│   ├── games.md
│   └── integrations.md
├── schemas/
│   ├── common/
│   ├── operations/
│   ├── lifecycle/
│   ├── hosting/
│   ├── execution/
│   ├── integrations/
│   └── storage/
├── examples/
├── rpc/
│   ├── methods.md
│   ├── errors.md
│   └── transport.md
├── management-api/
│   ├── overview.md
│   └── openapi.yaml
└── conformance/
    ├── requirements.md
    └── test-vectors/
```

The tree describes the draft package. [schemas/index.json](schemas/index.json) lists schemas; the operation catalog lists standard mutating operation types. Operation coverage and conformance gaps remain open review work.

## How the design got here

1. Set protocol principles and boundaries.
2. Draft and review IDs, timestamps, versions, errors, and capabilities.
3. Define durable operations, including recovery and unknown outcomes.
4. Define lifecycle states, transitions, preconditions, and safety behavior.
5. Specify hosting, storage, execution, game, and integration contracts.
6. Select and review adapter RPC and its optional process binding.
7. Define the management API for independent clients.
8. Set security, authorization, consent, audit, and environment-isolation rules.
9. Publish and validate schemas, examples, and conformance fixtures.
10. Validate the initial Go implementation against those schemas and fixtures; welcome other-language implementations.

The current untagged branch carries version `0.21.0-draft` and has completed the Phase 1 package review. It is development source, not an immutable compatibility target. A published snapshot must be immutable and increment the pre-1.0 minor version.

The protocol remains a draft. Phase 1 completion means the specification and Go validator/conformance suite are reviewed; it does not mean the full controller is ready. Stable protocol 1.0 remains gated on the Go controller and fake-adapter implementation passing the behavioral suite. Other-language implementations are welcome, but they are not required for release.

## Related decisions

- [Lifecycle protocol](lifecycle.md)
- [Adapter contracts](adapters/README.md)
- [RPC methods and transport](rpc/methods.md)
- [Management API](management-api/overview.md)
- [Authentication](management-api/authentication.md)
- [Authorization](management-api/authorization.md)
- [Standard operation catalog](operation-catalog.md)
- [Security requirements](security.md)
- [Compatibility and schema versioning](COMPATIBILITY.md)
- [Conformance requirements](conformance/requirements.md)
- [Execution-agnostic architecture](../docs/decisions/0003-execution-agnostic-runtime.md)
- [Encoding and bindings](../docs/decisions/0004-protocol-encoding-and-bindings.md)