# Management API Authorization

**Status:** Draft, protocol version 0.21.0-draft.

## 1. One authority

The controller core makes every authorization decision. HTTP handlers, CLI commands, UI clients, and adapters MUST call the same core policy. Hiding a button in a client does not protect an operation.

## 2. Scope permissions

Permissions name an action, resource, and environment. Initial names are in [security.md](../security.md):

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

Each implementation MUST document how principals, roles/groups, scopes, resources, and environments relate. Permission wildcards MUST be explicit; a wildcard MUST NOT silently include future destructive actions.

### 2.1 Submit decisions

The management API decides each define or submit request in this order, and stops at the first rule that applies. The rules are checked by [management-operations-submit-decision.json](../conformance/test-vectors/management-operations-submit-decision.json).

**`POST /servers` (define):**

1. No `Idempotency-Key`, or an operation type other than `server.define`: 400, `core.validation.invalid_request`.
2. The actor lacks `server.define` for that exact environment and server ID: 403, `core.authorization.denied`.
3. The key was already used by this actor: the same request replays the original operation (202); a different request is 409, `core.operation.idempotency_conflict`.
4. A server with that ID exists in any environment: 409, `core.operation.conflict`. Server IDs are unique across environments, because later routes name only the server.
5. Otherwise the operation is accepted (202).

**`POST /servers/{server_id}/operations` (submit):**

1. No `Idempotency-Key`: 400, `core.validation.invalid_request`.
2. The server does not exist, or the actor lacks `server.inspect` for it: 404. An inaccessible server is indistinguishable from a missing one.
3. The actor lacks the operation type's permission (for example `server.start`) for that server and environment: 403, `core.authorization.denied`.
4. The controller has no runnable workflow for the operation type: 422, `core.operation.unsupported_type`. Nothing is accepted that could never run.
5. The server's network profile needs a missing integration capability: 422, `core.network.capability_missing`.
6. Key reuse behaves as for define: replay (202) or 409, `core.operation.idempotency_conflict`.
7. Otherwise the operation is accepted (202).

## 3. Evaluate before accepting and dispatching

Before accepting an operation, the core checks:

1. The authenticated actor.
2. The operation type and exact parameters.
3. The logical server and environment.
4. Required adapter capabilities and adapter permission scopes.
5. Environment rules, including production/staging boundaries.
6. Required approvals and their expiry.
7. Current lifecycle state and lock/conflict state.

Check authorization again immediately before a sensitive step if permissions, policy, approval, or target may have changed since acceptance.

An adapter's capability does not authorize the caller. A caller's permission does not grant provider credentials. The core MUST check both.

## 4. Bind approval to the exact plan

An approval identifies the exact operation, action, target, environment, instance or resource reference, plan fingerprint, approver, and expiry. Any material change invalidates it.

The API exposes approval status or a reference without revealing approval secrets or allowing a client to fabricate approval state. The workflow and approver policy are implementation choices; their security guarantees are part of the protocol.

## 5. Protect resource visibility

Filter list and inspect results by authorization before returning resources. Avoid confirming that an inaccessible resource exists.

Bind each pagination cursor to the authenticated actor and canonical filter scope. Reject a cursor reused under another actor or filter with the generic `core.validation.invalid_request` error and return no resource items or metadata. The error MUST NOT reveal which scope check failed.

## 6. Conformance checks

Test unauthenticated and unauthorized access, cross-environment requests, privilege escalation, overly broad wildcards, stale approval, changed plan fingerprints, adapter capability without caller permission, and pagination cursor reuse by another actor.