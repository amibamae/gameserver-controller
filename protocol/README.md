# Protocol Specification

**Status:** Initial design principles. This document establishes direction; it does not define a finalized wire protocol or implemented API.

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
9. **Versioned evolution:** Protocol and schema changes MUST have documented compatibility rules. Breaking changes require a new major protocol version.
10. **Secret minimization:** Protocol messages MUST use secret references or protected credential channels, never ordinary serialized secret values. Secrets MUST be excluded from logs and diagnostic output.
11. **Privacy by default:** Protocols MUST distinguish necessary operational data from optional telemetry. Optional telemetry is disabled by default; identifiable player telemetry requires separate informed consent from the server owner and each affected player.
12. **Transport independence:** Message semantics and schemas SHOULD remain independent of the transport wherever practical. A transport binding may define framing, authentication, connection lifecycle, and delivery behavior without redefining operation meaning.

The terms MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY are used in their ordinary standards-style normative sense.

## Protocol areas

The specification is divided into these areas:

- **Common types:** identifiers, timestamps, version identifiers, resource references, structured errors, and pagination.
- **Identity and capabilities:** implementation identity, protocol compatibility, declared features, constraints, and required permissions.
- **Operations:** durable operation identity, status, progress, cancellation rules, idempotency, deadlines, and recovery.
- **Lifecycle:** server states, permitted transitions, preconditions, postconditions, and failure behavior.
- **Adapter contracts:** hosting, storage, game runtime, and integration responsibilities.
- **Management API:** versioned client-facing operations, read models, authentication, authorization, and event access.
- **Security and privacy:** trust boundaries, secret handling, consent, audit, and environment isolation.
- **Conformance:** shared test vectors and behavioral requirements for independent implementations.

## Separate communication surfaces

The project has two related but distinct communication surfaces.

### Adapter protocol

The adapter protocol connects the core to provider-, storage-, game-, and integration-specific implementations. Adapters are subordinate to core policy and receive only the permissions and secret references needed for their declared capabilities.

A local process transport (for example, structured messages over standard input/output) is a possible initial binding. It is not yet selected. In-process adapters MAY also implement the same logical contracts, provided they satisfy the same conformance requirements.

### Management API

The management API is for operators and independently developed clients. It exposes authorized controller capabilities and operation status; it does not expose unrestricted provider credentials or direct access to adapter internals.

The API should be documented independently of any user interface. An initial CLI can use application services directly or consume a local API, as decided during implementation. A hosted web panel is explicitly out of scope for the initial project.

## Candidate standards

The following are candidates, not finalized decisions:

| Standard | Possible role | Status |
|---|---|---|
| JSON | Interchange representation | Candidate |
| JSON Schema | Data structure validation | Candidate |
| OpenAPI | Management HTTP API description | Candidate |
| JSON-RPC 2.0 | Adapter request/response envelope | Candidate |
| Semantic Versioning | Protocol versioning | Candidate |

We should prefer established standards where they fit, but avoid adopting a standard merely because it is familiar. Each choice must be evaluated against interoperability, security, testability, streaming/progress needs, and implementation support.

## Conformance and compatibility

A conforming implementation must:

- Identify the protocol and schema versions it supports.
- Validate messages before acting on them.
- Reject malformed or unsupported required fields safely.
- Preserve opaque identifiers without interpreting provider-specific formats.
- Return documented structured errors.
- Respect deadlines, cancellation semantics, and idempotency rules.
- Avoid side effects for validation and inspection operations unless explicitly specified.
- Pass language-neutral conformance tests and shared test vectors.
- Enforce the same safety and privacy requirements as the core specification.

Compatibility policy must define handling of unknown fields, enum values, optional capabilities, deprecated fields, and major/minor/patch changes before the protocol is declared stable.

## Design constraints

- No web frontend or hosted panel is required.
- No specific programming language is selected by this specification.
- No specific database, message broker, or distributed coordination service is selected here.
- No adapter may independently decide that an unverified backup is safe for destruction.
- No client may directly invoke provider destructive operations outside core authorization and operation tracking.
- No protocol operation may silently weaken production/staging isolation or consent requirements.

## Proposed specification layout

```text
protocol/
├── README.md
├── VERSION
├── schemas/
│   ├── common/
│   ├── operations/
│   ├── hosting/
│   ├── storage/
│   ├── games/
│   └── integrations/
├── rpc/
│   ├── methods.md
│   ├── errors.md
│   └── transport.md
├── lifecycle/
│   ├── states.md
│   └── transitions.md
├── management-api/
│   ├── overview.md
│   ├── authentication.md
│   └── authorization.md
└── conformance/
    ├── requirements.md
    └── test-vectors/
```

This layout is provisional and may be adjusted as the contracts become concrete.

## Initial design sequence

1. Agree on protocol principles and boundaries.
2. Define common identifiers, timestamps, versioning, errors, and capability descriptors.
3. Define durable operation messages and lifecycle semantics.
4. Define adapter contracts and their transport binding.
5. Define the optional management API for external clients.
6. Define security, authorization, consent, and audit requirements across both surfaces.
7. Create schemas, examples, test vectors, and conformance requirements.
8. Evaluate implementation languages against the completed specification.

The protocol remains in draft until its semantics, schemas, compatibility rules, and conformance tests have been reviewed.