# Management API Authorization

**Status:** Draft, protocol version 0.21.0-draft.

## 1. One authority

The controller core makes every authorization decision. HTTP handlers, CLI commands, UI clients, and adapters must call the same core policy. Hiding a button in a client does not protect an operation.

## 2. Scope permissions

Permissions name an action, resource, and environment. Initial names are in [security.md](../security.md):

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

Each implementation must document how principals, roles/groups, scopes, resources, and environments relate. Permission wildcards must be explicit; a wildcard must not silently include future destructive actions.

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

An adapter's capability does not authorize the caller. A caller's permission does not grant provider credentials. The core must check both.

## 4. Bind approval to the exact plan

An approval identifies the exact operation, action, target, environment, instance or resource reference, plan fingerprint, approver, and expiry. Any material change invalidates it.

The API exposes approval status or a reference without revealing approval secrets or allowing a client to fabricate approval state. The workflow and approver policy are implementation choices; their security guarantees are part of the protocol.

## 5. Protect resource visibility

Filter list and inspect results by authorization before returning resources. Avoid confirming that an inaccessible resource exists.

Bind each pagination cursor to the authenticated actor and canonical filter scope. Reject a cursor reused under another actor or filter with the generic `core.validation.invalid_request` error and return no resource items or metadata. The error must not reveal which scope check failed.

## 6. Conformance checks

Test unauthenticated and unauthorized access, cross-environment requests, privilege escalation, overly broad wildcards, stale approval, changed plan fingerprints, adapter capability without caller permission, and pagination cursor reuse by another actor.