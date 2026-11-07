# Management API Authentication

**Status:** Draft, protocol version 0.21.0-draft.

## 1. What authentication establishes

Authentication tells the controller which operator or service called the management API. It does not grant permission to inspect or change a resource. The controller core makes that decision under [authorization.md](authorization.md).

## 2. Deployment profiles

### Local-only controller

A local controller may expose its API through a protected local socket, such as a Unix-domain socket, or through an in-process interface. It may identify the caller from the operating-system peer identity. This profile is not the remote bearer-authentication scheme in OpenAPI, so an implementation must document how it establishes and passes local identity to the core.

A loopback TCP listener is not trusted by default. Without authenticated peer identity, it cannot establish who the caller is; a compromised local process is within the threat model.

### Remote controller

A remotely reachable management API must use TLS and authenticate each request. The initial OpenAPI contract shows HTTP bearer authentication as a placeholder. Each deployment must document its token issuer, audience, signature or opaque-token validation, lifetime, rotation, revocation, and identity mapping.

Send bearer tokens only in the Authorization header over TLS. Never put one in a URL, query parameter, request body, operation record, or log.

### Service-to-service calls

Keep automation identities separate from human operators. Use short-lived, audience-restricted credentials with only the actions and environments the service needs. Do not confuse a management client credential with an adapter or provider credential.

## 3. Trusted actor identity

The controller maps the authenticated principal to a trusted actor record. It records a stable actor ID, authentication method, and relevant issuer or local OS principal.

A client may include an actor ID in request metadata, but the controller must not trust a client-supplied role or permission list. Treat actor metadata as informational unless the implementation independently binds it to the authenticated principal.

## 4. Token and session handling

Every implementation documents:

- How it issues and provisions credentials.
- Expiry and clock-skew behavior.
- Revocation and rotation.
- Audience and controller-instance binding.
- Authentication failures and rate limits.
- Redaction rules.

A logout or expired token does not, by itself, cancel or pause a durable operation already accepted by the controller. That work continues under its recorded request and actor context. But the controller must check current permissions, approvals, target, and policy again before a sensitive step when required. If that check fails, pause or reject the step. Expiry is neither a cancellation request nor fresh authorization.

## 5. Errors and protected information

Return 401 for unauthenticated requests. Return 403 for authenticated requests that lack permission, subject to resource-privacy rules. An implementation may return 404 instead of 403 when confirming that a resource exists would reveal protected information.

Authentication errors must not expose secret material, token-validation details, or unnecessary account information.

## 6. Conformance checks

Test remote requests with invalid signatures, expired credentials, wrong audiences, and revoked credentials. Each must return 401, establish no actor, reveal no protected data, create no operation, and dispatch no adapter call. A valid control request must establish its actor from the authenticated principal.

For local access, test a verified OS peer identity, missing peer identity, and loopback TCP without authenticated peer identity. Only the verified peer may establish the actor; a client-supplied actor claim is never authoritative.

Use a synthetic bearer token and confirm it appears only in the Authorization header over TLS, never in URLs, query parameters, request bodies, operation records, logs, errors, events, or audit records. Also test actor spoofing and the continuation of an accepted operation after the client's session expires, using the shared conformance vectors.