# ADR 0004: Initial Protocol Encoding and Communication Bindings

- **Status:** Accepted for the 0.1.0-draft design
- **Date:** 2026-11-05
- **Decision owners:** Project maintainers

## Context

Independent implementations need the same message representation and validation rules. The project needs separate contracts for core-to-adapter calls and operator/client-to-controller management. Common libraries should exist across languages, and the protocol should not choose an implementation language.

## Decision

The 0.1.0-draft protocol uses these standards and bindings:

| Concern | Selection | Scope |
|---|---|---|
| Interchange format | JSON, RFC 8259 | Protocol objects and examples |
| Structural schemas | JSON Schema Draft 2020-12 | Requests, responses, descriptors, configuration, and fixtures |
| Timestamps | RFC 3339 date-time | Protocol instants; generated values use UTC |
| Canonical request fingerprints | RFC 8785 JSON Canonicalization Scheme | Only when deterministic JSON hashing or signing is required |
| Adapter RPC semantics | JSON-RPC 2.0 | Adapter request and response meaning |
| Optional adapter process binding | jsonrpc-stdio-v1 | UTF-8, newline-delimited JSON on stdin and stdout |
| Management API description | OpenAPI 3.2.1 | HTTP/JSON client API |

These choices do not select the controller’s language, database, process framework, or packaging.

## Compatibility and validation

- Schema files identify JSON Schema Draft 2020-12 using its standard $schema URI.
- Protocol versions and schema revisions remain distinct.
- Validators must check protocol timestamp and URI rules. Do not assume JSON Schema format annotations are assertions unless the validator enables assertion behavior or the implementation checks formats itself.
- Reject duplicate JSON object property names.
- The JSON-RPC process profile uses string request IDs and one message per line.
- Transfer large binary data outside JSON-RPC messages.
- Mutating management API operations are asynchronous and return durable operation resources.
- Canonicalize JSON only when the contract requires a deterministic fingerprint. Wire messages do not need canonical property order.

## Why these standards

These standards have broad language support and independent specifications. JSON Schema gives portable structural checks; OpenAPI supports independent HTTP clients; JSON-RPC supplies a request/response envelope without requiring a particular network transport.

## Consequences

- Commit schemas, examples, and fixtures with the protocol.
- Use language-neutral tests for structural and behavioral rules.
- Keep the adapter process binding optional so in-process adapters remain possible.
- Review and specify a separate binding before adding remote adapter transport.
- Treat these selections as draft choices. Revise them before stable 1.0 if interoperability testing shows a material problem.