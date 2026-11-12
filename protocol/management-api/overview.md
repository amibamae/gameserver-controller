# Management API Overview

**Status:** Draft, version 0.21.0-draft.

## 1. Purpose

The management API gives independent operator clients authorized access to controller operations. It is separate from adapter RPC, and it does not require a web panel.

A local CLI, web panel, desktop application, automation, or another controller may use the same API. The API must not expose provider credentials or secret values, offer unrestricted adapter calls, or let a client bypass core lifecycle rules.

## 2. HTTP binding

The first binding uses HTTP with JSON request and response bodies. [openapi.yaml](openapi.yaml) describes OpenAPI 3.2.1 and the remote bearer-authenticated profile. A local deployment may expose the same application operations through an OS-authenticated local socket or in-process API; see [authentication.md](authentication.md).

The base path is `/v1`. Implementations must identify supported API versions and reject incompatible requests rather than silently serving them under another version.

Mutating requests run asynchronously:

1. Validate, authenticate, and authorize the request.
2. Commit the durable operation.
3. Return HTTP 202 with the operation resource and a Location header.
4. Perform the work asynchronously.

HTTP 202 means the controller accepted and durably recorded the request. It does not mean the operation finished.

## 3. Initial endpoints

- `GET /v1/capabilities`: list controller and adapter capabilities.
- `GET /v1/servers`: list logical servers the caller may inspect.
- `POST /v1/servers`: define a logical server through durable `server.define`.
- `GET /v1/servers/{server_id}`: inspect one logical server and its current references/states.
- `POST /v1/servers/{server_id}/operations`: request provision, start, stop, backup, restore, or destroy.
- `GET /v1/operations/{operation_id}`: read durable operation status and result.
- `POST /v1/operations/{operation_id}/cancel`: request cancellation under that operation's rules.
- `GET /v1/operations/{operation_id}/events?after_sequence={n}`: optional Server-Sent Events stream. Events use the canonical operation-event schema; polling the operation resource remains authoritative.

Every endpoint enforces authorization and environment scope. A list endpoint must not reveal inaccessible resources.

A controller that does not yet support cancellation or the event stream returns 501 with `core.api.not_implemented`. A server operation type the controller cannot run to completion is refused with 422 and `core.operation.unsupported_type` before anything is recorded. The decision order for define and submit is in [authorization section 2.1](authorization.md#21-submit-decisions).

## 4. Operation requests and idempotency

Each request names an operation type and its type-specific parameters. Every mutating acceptance request must include the `Idempotency-Key` header. The value is case-sensitive and contains 1–128 visible ASCII characters (U+0021 through U+007E). It is scoped to the authenticated actor and is not a credential.

The controller binds the key to actor, operation type, target, environment, and canonical validated parameters. If the same key arrives with changed parameters, return `core.operation.idempotency_conflict`. If the request is identical and its full record remains, return the original operation. After that record expires, a retained tombstone returns `core.operation.idempotency_key_expired` and prevents a new operation or dispatch. See the [operation idempotency rules](../operations.md#7-idempotency-and-duplicate-requests).

For a server operation, the path's server ID must match the target in the validated parameters. Restore names one immutable backup. Destroy identifies its target or binds to the current instance through a plan that the controller rechecks immediately before execution.

## 5. Authentication and authorization

A remote deployment must use TLS and authenticated requests. Bearer tokens are one supported binding; a local installation may use OS identity. The controller remains responsible for actor identity, permission checks, approvals, environment limits, and audit.

The OpenAPI bearer scheme is a placeholder. It does not require a particular identity provider or token issuer.

## 6. Errors

Before operation acceptance, return RFC 9457 Problem Details with protocol error extensions. After acceptance, store errors in the operation resource. If a disconnect or timeout makes acceptance ambiguous, read the operation before sending another mutating request.

## 7. Compatibility

API paths carry the major version, `v1`. Minor additions must remain backward-compatible and define how clients handle unknown fields and enum values. Breaking changes require a new major path. The API version and core protocol version are related, but they are not the same version.

## 8. Out of scope

- A required browser interface.
- Raw provider API access.
- Direct adapter calls from untrusted clients.
- Synchronous completion of long-running lifecycle work.
- Secrets in configuration reads or operation history.