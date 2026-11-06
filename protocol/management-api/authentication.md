# Management API Authentication

**Status:** Draft, protocol version 0.13.0-draft.

## 1. Purpose

Authentication establishes the identity of an operator or service calling the management API. Authentication does not itself grant permission to inspect or mutate a resource. Authorization is defined in [authorization.md](authorization.md) and enforced by the controller core.

## 2. Deployment profiles

### Local-only profile

A local controller MAY expose an API only through a protected local socket (such as a Unix-domain socket) or in-process application interface. It MAY derive the actor identity from the operating-system peer identity. This local profile is not represented by the remote bearer-authentication OpenAPI security scheme; an implementation MUST document how local peer identity is established and passed to the core. A local TCP listener without authenticated peer identity MUST NOT be treated as trusted merely because it listens on loopback; local process compromise is within the threat model.

### Remote profile

A remotely reachable management API MUST use TLS and authenticated requests. The initial OpenAPI binding describes HTTP bearer authentication as a placeholder. Token issuer, audience, signature/opaque-token validation, lifetime, rotation, revocation, and identity mapping are deployment configuration and MUST be documented by each implementation.

Bearer tokens MUST be transmitted only in the Authorization header over TLS. Tokens MUST NOT appear in URLs, query parameters, request bodies, operation records, or logs.

### Service-to-service profile

Automation identities SHOULD be separate from human operator identities. Credentials SHOULD be short-lived, audience-restricted, and scoped to the minimum actions/environment required. Client credentials MUST NOT be confused with adapter/provider credentials.

## 3. Actor identity

The controller establishes a trusted actor record from the authenticated principal. It records a stable actor identifier, authentication method, and relevant identity issuer or local OS principal.

The client MUST NOT submit a role or permission list that the controller trusts as authoritative. Any actor identifier in request metadata is informational only unless independently bound to the authenticated principal by the implementation.

## 4. Session and token handling

Implementations MUST document:
- Credential issuance and provisioning.
- Expiry and clock-skew behavior.
- Revocation and credential rotation.
- Audience and controller-instance binding.
- Authentication failure behavior and rate limiting.
- Redaction requirements.

Logout or token expiry MUST NOT be assumed to cancel already accepted durable operations. Operation authorization and cancellation are separate concerns.

## 5. Errors and information disclosure

Unauthenticated requests receive 401. Authenticated but unauthorized requests receive 403, subject to resource-existence privacy policy. Implementations MAY return 404 instead of 403 when revealing that a resource exists would disclose sensitive information.

Authentication errors MUST NOT reveal secret material, token validity internals, or unnecessary account details.

## 6. Conformance

Test invalid/expired credentials, wrong audience, revoked credentials, token leakage in logs/URLs, actor spoofing, local peer identity behavior, and operation continuation after client session expiry.
