# Protocol Security and Privacy Requirements

**Status:** Draft, version 0.21.0-draft. These are protocol requirements, not a security certification.

## 1. Trust boundaries

The design separates:

- Operator/client and controller.
- Controller core and adapter.
- Adapter and hosting/storage provider.
- Controller/game adapter and game process/filesystem.
- Controller and proxy/integration.
- Production and staging.
- Source/release artifacts and privileged bootstrap execution.
- Controller and optional telemetry destination.

Treat adapters, plugins, game files, archives, provider responses, and user configuration as untrusted until validated.

## 2. Authentication and actor identity

The controller establishes the caller's identity at the client boundary. A client MUST NOT assert its own role or permission scope in an ordinary request.

Deployments choose the authentication mechanism. A local controller MAY use OS identity and local access controls. A remote management API MUST use an authenticated channel and document its method and token/session lifecycle. A public network deployment MUST use TLS.

The controller identifies an adapter process using its configured executable, OS identity, configuration, and validated descriptor. A descriptor describes an adapter; it does not authorize it.

## 3. Authorization and scopes

The core checks authorization before accepting a mutating operation and again before a sensitive or destructive step when relevant.

Permission scopes name an action, resource category, and environment. Examples:

- `server.inspect`
- `server.define`
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
- `proxy.session.inspect`

An adapter capability describes what that adapter can do. It does not grant the caller permission. The core intersects caller authorization, environment policy, adapter capability, and available secret scopes.

## 4. Approvals and destructive work

Bind each approval to the exact action, logical server, environment, provider/adapter, concrete instance/resource, plan fingerprint, expiry, and approver. Immediately before each sensitive or destructive step, check that binding and expiry against the exact request. Any material change invalidates approval.

Before destroying an instance with persistent game data, the core MUST confirm:

- A verified backup for the current data generation.
- The exact target identity, rechecked immediately before destruction.
- No unresolved conflicting operation.
- Current authorization and required approval.
- Durable operation and audit records.

If a precondition fails or remains unclear, fail closed. A generic force/yes flag cannot bypass production safeguards. Permanent backup deletion is a separate, more restrictive action.

## 5. Secret references and credential flow

Ordinary protocol messages carry secret references, never secret values. A reference does not itself grant permission to resolve a secret.

The controller MUST:

- Resolve secrets only for the component and step that need them.
- Use least-privilege scopes and separate production/staging credentials.
- Minimize how long secrets remain in memory and keep them out of operation records.
- Redact them from logs, errors, event payloads, process diagnostics, and audit.
- Reject literal values where a secret reference is required.
- Allow credential rotation without changing protocol object identity.

Never put raw credentials in bootstrap source, command-line arguments, provider user-data, release archives, source control, or logs. An implementation MAY define a separate protected channel for short-lived credential delivery.

## 6. Environment isolation and data protection

Every resource and operation belongs to an environment. Deny cross-environment actions by default.

Production backup credentials MUST NOT let staging write or delete. Staging MAY get narrow read access to selected production objects, and writes only to staging-owned destinations. Even if a staging actor mistakenly receives production write/delete credentials, enforce the boundary in policy: backup writes and deletes require both an exact environment match and the action-specific scope.

Restore MUST reject path traversal, absolute paths, unsafe symbolic links, hard-link escapes, device nodes, and archive entries outside the approved data root. Extraction MUST NOT follow attacker-controlled links.

## 7. Adapter isolation and supply chain

Adapters are executable code. Review and version them, and install them from trusted sources. The controller SHOULD isolate adapters by OS user, filesystem, network policy, resource limits, and narrowly scoped credentials. A compromised adapter MUST NOT inherit every controller or provider secret.

Bootstrap scripts are privileged code. Record the exact source revision and digest, non-secret inputs, execution identity, and validation result. Review and test changes on fresh, isolated instances before production.

## 8. Audit and event privacy

For each security-relevant action, audit the actor, action, target, environment, authorization/approval decision, operation ID, timestamp, and result. Restrict access to audit records and protect them from unauthorized changes.

Minimize player identifiers and telemetry. Identifiable optional telemetry starts disabled and requires separate informed consent from the server owner and each affected player. Routine health checks MUST NOT silently become gameplay profiling.

Operator notifications (integrations section 7) leave the controller for third-party destinations such as Discord, Slack, an email provider, or an ntfy server. They carry only the notification-event fields: the event type and severity, environment, adapter and proxy instance IDs, times, failure count, and a fixed-template summary. They never carry player data, session IDs, addresses, raw errors, URLs, or credentials. Destination secrets (webhook URLs, SMTP passwords, ntfy tokens) are systemd credentials, are passed to the notifier only as authorized secret references, and never appear in logs or events.

## 9. Security conformance

Test that:

- A caller cannot grant itself permissions.
- An adapter cannot bypass core policy.
- Destruction approval binds the exact action, server, environment, adapter, instance, plan fingerprint, expiry, and approver; stale or mismatched approval blocks the provider call.
- Failed backup verification blocks destruction.
- Staging cannot write to or delete production backups.
- Logs and errors contain no secret values.
- Unsafe archive paths are rejected.
- Unsupported capabilities fail explicitly.
- Adapter crashes do not erase durable operations.
- The controller reconciles uncertain effects before retrying.