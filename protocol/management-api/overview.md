# Management API Overview

**Status:** Draft, version 0.19.0-draft.

## 1. Purpose

The management API exposes authorized controller operations to independent operator clients. It is separate from adapter RPC and is not a web panel.

A local CLI, web panel, desktop application, automation, or another controller can use the same API. The API MUST NOT expose provider credentials, secret values, direct unrestricted adapter methods, or a way to bypass core lifecycle policy.

## 2. Binding

The initial management binding is HTTP with JSON request/response bodies. The machine-readable interface is [openapi.yaml](openapi.yaml), using OpenAPI 3.2.1. That document describes the remote bearer-authenticated profile. A local-only implementation MAY expose the same application operations through an OS-authenticated local socket or in-process API as defined in [authentication.md](authentication.md).

The base path is `/v1`. Implementations MUST identify supported API versions and MUST NOT silently serve an incompatible request under a different version.

Mutating operations are asynchronous: validate the request; authenticate and authorize; durably record the operation; return HTTP 202 with the operation resource and Location header; then perform work asynchronously. HTTP 202 means accepted and durably recorded, not completed.

## 3. Initial endpoints

- `GET /v1/capabilities`: discover controller and adapter capabilities.
- `GET /v1/servers`: list authorized logical servers.
- `POST /v1/servers`: define a logical server using the durable `server.define` operation.
- `GET /v1/servers/{server_id}`: inspect a logical server and current references/states.
- `POST /v1/servers/{server_id}/operations`: request a typed operation (provision, start, stop, backup, restore, or destroy).
- `GET /v1/operations/{operation_id}`: inspect durable operation status and result.
- `POST /v1/operations/{operation_id}/cancel`: request cancellation under operation-specific rules.
- `GET /v1/operations/{operation_id}/events?after_sequence={n}`: optional Server-Sent Events stream where supported. Events use the canonical operation event schema; polling the operation resource remains authoritative.

Implementations MUST enforce per-resource authorization and environment scoping on every endpoint. List endpoints MUST NOT reveal the existence of unauthorized resources.

## 4. Operation request

The request identifies an operation type and a type-specific parameter object. It includes an idempotency key in a documented header or request field. The server binds that key to actor, operation type, target, environment, and canonical validated parameters. A changed request with the same key returns conflict. An identical retry returns the original operation.

For server-specific operations, the path server ID MUST match the corresponding target ID in the validated parameters. A restore request MUST identify one immutable backup. A destroy request MUST identify the intended target or bind to the current instance through a plan that is rechecked immediately before execution.

## 5. Authentication and authorization

A remote deployment MUST use TLS and authenticated requests. Bearer tokens are one supported binding; local installations MAY use operating-system identity. The controller remains authoritative for actor identity, permission checks, approval requirements, environment restrictions, and audit.

The OpenAPI security definition is a placeholder for deployment-specific bearer authentication; it does not mandate a particular identity provider or token issuer.

## 6. Errors

Errors before operation acceptance use RFC 9457 Problem Details with protocol error extensions. Errors after acceptance are stored in the operation resource. A client MUST read the operation after an ambiguous disconnect or timeout before submitting a new mutating request.

## 7. Compatibility

API paths carry major version `v1`. Minor additions MUST be backward compatible and define how clients handle unknown fields and enum values. Breaking changes require a new major path. API version and core protocol version are related but not identical.

## 8. Non-goals

- A required browser interface.
- Direct raw provider API access.
- Direct adapter invocation by untrusted clients.
- Synchronous completion of long-running lifecycle operations.
- Exposing secrets in configuration reads or operation history.
