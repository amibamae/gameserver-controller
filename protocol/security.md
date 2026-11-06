# Protocol Security and Privacy Requirements

**Status:** Draft, version 0.17.0-draft. This document specifies protocol-level security requirements; it is not a security certification.

## 1. Trust boundaries

The design distinguishes:
- Operator/client to controller.
- Controller core to adapter.
- Adapter to hosting/storage provider.
- Controller/game adapter to game process and filesystem.
- Controller to proxy/integration.
- Production to staging.
- Source repository/release artifact to privileged bootstrap execution.
- Optional telemetry destination.

Adapters, plugins, game files, archives, provider responses, and user-supplied configuration are untrusted inputs until validated.

## 2. Authentication and actor identity

The controller MUST establish the trusted actor identity at the client boundary. Clients MUST NOT self-assert roles or permission scopes in ordinary request payloads.

Authentication mechanism is deployment-specific. The initial local controller MAY use operating-system identity and local access controls. A remote management API MUST use an authenticated channel and MUST document its authentication method and token/session lifecycle. A public network deployment MUST use TLS.

Adapter process identity is established by the controller's configured executable, operating-system identity, configuration, and validated descriptor. A descriptor is not an authorization grant.

## 3. Authorization and scopes

Authorization is enforced by the controller core before accepting a mutating operation and again before sensitive/destructive steps where relevant.

Permission scopes MUST identify action, resource category, and environment. Examples:
- `server.inspect`
- `server.provision`
- `server.start`
- `server.stop`
- `server.backup`
- `server.restore`
- `server.destroy`
- `provider.instance.create`
- `provider.instance.destroy`
- `storage.backup.read`
- `storage.backup.write`
- `storage.backup.delete`
- `proxy.backend.manage`

A capability declaration describes what an adapter can do; it does not grant a caller permission to request it. The controller MUST intersect caller authorization, environment policy, adapter capability, and available secret scopes.

## 4. Approvals and destructive actions

An approval MUST be bound to an exact action, logical server, environment, provider/adapter, concrete instance/resource reference, plan fingerprint, expiry, and approver identity. Any material change invalidates the approval.

Destroy of an instance containing persistent game data MUST be blocked unless:
- A verified backup is associated with the current data generation.
- The exact target identity is confirmed immediately before destruction.
- No unresolved conflicting operation exists.
- Authorization and required approval are current.
- The operation and audit record are durable.

Failure or uncertainty in any precondition MUST fail closed. A generic force/yes flag MUST NOT bypass production safeguards. Permanent deletion of backups is a separate, more restrictive action.

## 5. Secret references and credential flow

Ordinary protocol messages contain references to secrets, not secret values. A secret reference is not permission to resolve the secret.

The controller MUST:
- Resolve secrets only for the component/step requiring them.
- Use least-privilege scopes and separate production/staging credentials.
- Minimize secret lifetime in memory and avoid persistence in operation records.
- Redact secrets from logs, errors, event payloads, process diagnostics, and audit.
- Reject literal values where a secret reference is required.
- Support credential rotation without changing protocol object identity.

Bootstrap source, command-line arguments, provider user-data, release archives, source control, and logs MUST NOT contain raw credentials. Short-lived credential delivery MAY use a protected channel separately defined by the implementation.

## 6. Environment isolation and data protection

Every resource and operation belongs to an environment. Cross-environment operations are denied by default.

Production backup credentials MUST NOT be available to staging for write/delete. Staging may receive narrowly scoped read access to selected production backup objects, and writes only to staging-owned destinations.

Restore must reject path traversal, absolute paths, unsafe symbolic links, hard-link escapes, device nodes, and archive entries outside the approved data root. Extraction must not follow attacker-controlled links.

## 7. Adapter isolation and supply chain

Adapters are executable code and must be reviewed, versioned, and installed from trusted sources. The controller SHOULD isolate adapters by OS user, filesystem, network policy, resource limits, and narrowly scoped credentials. A compromised adapter must not automatically inherit every controller/provider secret.

Bootstrap scripts are privileged code. Record exact source revision, digest, non-secret inputs, execution identity, and validation result. Review and test changes on fresh isolated instances before production use.

## 8. Audit and event privacy

Audit records MUST include actor, action, target, environment, authorization/approval decision, operation ID, timestamp, and outcome for security-relevant operations. Audit records MUST be access controlled and protected from unauthorized modification.

Player identifiers and telemetry MUST be minimized. Optional identifiable player telemetry is disabled by default and requires separate informed consent from the server owner and each affected player. Ordinary operational health checks must not silently become gameplay profiling.

## 9. Security conformance

An implementation MUST pass tests proving:
- Caller cannot self-elevate permissions.
- Adapter cannot bypass core policy.
- Missing/invalid approval blocks destruction.
- Failed backup verification blocks destruction.
- Staging cannot write/delete production backups.
- Secret values are absent from logs/errors/events.
- Unsafe archive paths are rejected.
- Unknown capabilities fail explicitly.
- Adapter crashes do not erase durable operations.
- Uncertain side effects are reconciled before retry.
