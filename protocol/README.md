# Protocol Specification

**Status:** Draft specification, version 0.2.0-draft. The repository now contains initial normative prose, JSON Schemas, RPC and management API bindings, examples, and conformance vectors. It is not a stable 1.0 release, and no implementation has yet demonstrated cross-language conformance.

## Purpose

Game Server Controller is designed around language-independent contracts. The controller core, adapters, command-line tools, and future management clients should be replaceable or independently implemented without requiring the whole project to be rewritten in one programming language.

The protocol specifications—not a particular language implementation—are the authoritative source for interoperability.

## Project commitments

1. **Language neutrality:** Protocol definitions MUST NOT depend on Go, Rust, Python, C, Bash, or any other implementation language.
2. **Implementation independence:** Independent implementations that conform to the same protocol version MUST agree on message meaning, validation, state transitions, errors, and safety behavior.
3. **No AI requirement:** Protocol tooling, generation, validation, builds, and normal operation MUST NOT require AI services or AI-generated artifacts.
4. **No mandatory web panel:** The controller MUST be usable without a web panel or browser interface. A web panel is not part of the initial implementation requirement.
5. **Future client support:** The project SHOULD expose a documented, versioned management API so third parties can build web, desktop, mobile, or other clients without modifying the controller core.
6. **Core authority:** Clients and adapters MUST NOT bypass core authorization, lifecycle policy, environment isolation, approval gates, audit, or consent enforcement.
7. **Explicit capabilities:** Implementations MUST declare supported capabilities. Unsupported operations MUST fail explicitly rather than being silently approximated.
8. **Safe failure:** Ambiguous results, timeouts, interrupted operations, and partial failures MUST be represented explicitly. A timeout MUST NOT be treated as proof that a destructive action did or did not happen.
9. **Versioned evolution:** Protocol and schema changes MUST have documented compatibility rules. Before 1.0, each published draft snapshot gets a new minor protocol version; after 1.0, breaking changes require a new major version.
10. **Secret minimization:** Protocol messages MUST use secret references or protected credential channels, never ordinary serialized secret values. Secrets MUST be excluded from logs and diagnostic output.
11. **Privacy by default:** Protocols MUST distinguish necessary operational data from optional telemetry. Optional telemetry is disabled by default; identifiable player telemetry requires separate informed consent from the server owner and each affected player.
12. **Transport independence:** Message semantics and schemas SHOULD remain independent of the transport wherever practical. A transport binding may define framing, authentication, connection lifecycle, and delivery behavior without redefining operation meaning.

The terms MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY are used in their ordinary standards-style normative sense.

## Protocol areas

The specification is divided into these areas:

- **Common types:** identifiers, timestamps, version identifiers, resource references, structured errors, and pagination.
- **Identity and capabilities:** implementation identity, protocol compatibility, declared features, constraints, and required permissions.
- **Operations:** durable operation identity, acceptance, steps, status, progress, idempotency, retries, cancellation, reconciliation, and recovery.
- **Lifecycle:** server states, permitted transitions, preconditions, postconditions, and failure behavior.
- **Adapter contracts:** hosting, storage, execution, game runtime, and integration responsibilities.
- **Management API:** versioned client-facing operations, read models, authentication, authorization, and event access.
- **Security and privacy:** trust boundaries, secret handling, consent, audit, and environment isolation.
- **Compatibility:** protocol negotiation, schema package identity, version evolution, deprecation, and safe operation recovery.
- **Conformance:** shared test vectors and behavioral requirements for independent implementations.

## Separate communication surfaces

The project has two related but distinct communication surfaces.

### Adapter protocol

The adapter protocol connects the core to hosting, storage, execution, game-, and integration-specific implementations. Adapters are subordinate to core policy and receive only the permissions and secret references needed for their declared capabilities.

The initial optional process binding is `jsonrpc-stdio-v1`, defined in [rpc/transport.md](rpc/transport.md). In-process adapters MAY implement the same logical contracts without using a wire transport, provided they satisfy the same conformance requirements.

### Management API

The management API is for operators and independently developed clients. It exposes authorized controller capabilities and operation status; it does not expose unrestricted provider credentials or direct access to adapter internals.

The API should be documented independently of any user interface. An initial CLI can use application services directly or consume a local API, as decided during implementation. A hosted web panel is explicitly out of scope for the initial project.

## Selected 0.2.0-draft standards

The initial draft adopts the following language-neutral standards and bindings. These choices are recorded in [ADR 0004](../docs/decisions/0004-protocol-encoding-and-bindings.md) and may be revised before 1.0 if interoperability testing demonstrates a material problem.

| Concern | Selection | Role |
|---|---|---|
| Interchange | [JSON / RFC 8259](https://www.rfc-editor.org/rfc/rfc8259) | Protocol objects and examples |
| Structural schemas | [JSON Schema 2020-12](https://json-schema.org/draft/2020-12) | Machine-readable validation |
| Timestamps | [RFC 3339](https://www.rfc-editor.org/rfc/rfc3339) | Absolute instants |
| Canonicalization | [RFC 8785 JCS](https://www.rfc-editor.org/rfc/rfc8785) | Deterministic fingerprints where required |
| Adapter RPC | [JSON-RPC 2.0](https://www.jsonrpc.org/specification) | Adapter request/response semantics |
| Initial optional adapter process binding | `jsonrpc-stdio-v1` | UTF-8 newline-delimited JSON over stdin/stdout |
| Management API | [OpenAPI 3.2.1](https://spec.openapis.org/oas/v3.2.1) | HTTP/JSON client API |

JSON Schema `format` is an annotation unless format assertion is enabled. Implementations MUST validate required protocol formats such as RFC 3339 timestamps and URIs through format-assertion support or equivalent explicit validation.

Protocol integers MUST remain within the exact interoperable JSON integer range (absolute value no greater than 9,007,199,254,740,991). Larger counters, provider identifiers, and byte quantities MUST be represented as decimal strings.

The in-process adapter contract is transport-independent. The stdio RPC profile is optional and does not make subprocess adapters mandatory. Remote adapter communication requires a separately reviewed binding.

## Extensibility and unknown fields

For this draft, request objects and security-sensitive command objects are closed: unknown fields are rejected unless the object defines an explicit `extensions` map. Extension names MUST be namespaced and MUST NOT change the meaning of standard fields.

Read-only response objects MAY gain optional fields in a compatible minor revision. Clients MUST ignore unknown optional response fields and MUST fail safely on an unknown enum/state value that affects a decision. An implementation MUST NOT silently reinterpret an unknown lifecycle state or capability.

Schemas MUST state whether an object is closed or extensible. A minor revision MUST NOT change the meaning, type, or security effect of an existing field.

## Conformance and compatibility

A conforming implementation must:

- Identify the protocol and schema versions it supports.
- Validate messages before acting on them.
- Reject malformed or unsupported required fields safely.
- Preserve opaque identifiers without interpreting provider-specific formats.
- Return documented structured errors.
- Respect deadlines, cancellation semantics, idempotency rules, and operation recovery behavior.
- Avoid side effects for validation and inspection operations unless explicitly specified.
- Pass language-neutral conformance tests and shared test vectors.
- Enforce the same safety and privacy requirements as the core specification.

The initial negotiation and evolution policy is defined in [Protocol and Schema Compatibility](COMPATIBILITY.md). It remains a draft and must be reviewed before protocol 1.0.

## Design constraints

- No web frontend or hosted panel is required.
- No specific programming language is selected by this specification.
- No specific database, message broker, or distributed coordination service is selected here.
- No adapter may independently decide that an unverified backup is safe for destruction.
- No client may directly invoke provider destructive operations outside core authorization and operation tracking.
- No protocol operation may silently weaken production/staging isolation or consent requirements.
- A controller MUST NOT perform a mutating external action without a durable operation record.
- An unknown external outcome MUST be reconciled before retrying or starting dependent work.

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

The tree reflects the initial draft package. The schema inventory is indexed in [schemas/index.json](schemas/index.json). The operation catalog defines the initial standard mutating operation types; operation-specific coverage and cross-language conformance remain review work.

## Initial design sequence

1. Establish protocol principles and boundaries.
2. Draft and review common identifiers, timestamps, versioning, errors, and capability descriptors.
3. Draft and review durable operation semantics, including recovery and uncertainty handling.
4. Draft and review lifecycle states, transitions, preconditions, postconditions, and safety behavior.
5. Define hosting, storage, execution, game, and integration adapter contracts.
6. Select and review the adapter RPC and optional process binding.
7. Define the optional management API for independent clients.
8. Define security, authorization, consent, audit, and environment isolation across both surfaces.
9. Publish and validate language-neutral schemas, examples, and conformance vectors.
10. Demonstrate interoperability using independent implementations in at least two programming languages before a stable 1.0 release.

This draft snapshot is version 0.2.0-draft. It remains draft until semantics, schemas, compatibility rules, security requirements, and conformance tests are reviewed and at least two independent language implementations pass the published interoperability suite.

## Core design decisions

- [Lifecycle protocol](lifecycle.md)
- [Adapter contracts](adapters/README.md)
- [RPC methods and transport](rpc/methods.md)
- [Management API](management-api/overview.md)
- [Management API authentication](management-api/authentication.md)
- [Management API authorization](management-api/authorization.md)
- [Standard operation catalog](operation-catalog.md)
- [Security requirements](security.md)
- [Compatibility and schema versioning](COMPATIBILITY.md)
- [Conformance requirements and test vectors](conformance/requirements.md)
- [Execution-agnostic architecture decision](../docs/decisions/0003-execution-agnostic-runtime.md)
- [Encoding and binding decision](../docs/decisions/0004-protocol-encoding-and-bindings.md)
