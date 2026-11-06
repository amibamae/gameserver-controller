# Management API Authorization

**Status:** Draft, protocol version 0.17.0-draft.

## 1. Authority

The controller core is the sole authority for authorization decisions. HTTP handlers, CLI commands, UI clients, and adapters MUST call the same core authorization policy. A client-side hidden button is not an authorization control.

## 2. Permission model

Permissions are action/resource/environment scoped. Initial permission names are defined in [security.md](../security.md), including:
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

Implementations MUST document the relationship between principals, roles/groups, scopes, resources, and environments. Permission wildcards MUST be explicit and MUST NOT silently include future destructive permissions.

## 3. Authorization evaluation

Before accepting an operation, the core MUST evaluate:
1. Authenticated actor identity.
2. Requested operation type and exact parameters.
3. Logical server and environment scope.
4. Required adapter capabilities and adapter-granted permission scopes.
5. Environment policy and production/staging boundaries.
6. Required approvals and expiry.
7. Current lifecycle state and lock/conflict state.

Sensitive steps MUST be re-authorized immediately before dispatch when policy, target, approval, or permissions could have changed since acceptance.

An adapter capability is not caller authorization. A caller permission is not a grant of provider credentials. Both checks are required.

## 4. Approvals

An approval is bound to an exact operation, action, target, environment, instance/resource reference, plan fingerprint, approver, and expiry. Material changes invalidate the approval.

The API MUST expose approval status/reference without exposing approval secrets or permitting a client to fabricate approval state. The approval workflow and approver policy are implementation-defined, but their security guarantees are protocol-defined.

## 5. Resource visibility

List and inspect endpoints MUST apply authorization filters before returning resources. An implementation SHOULD avoid confirming the existence of an inaccessible resource. Pagination cursors MUST be bound to the authenticated actor and filter scope so they cannot be reused to enumerate another principal's resources.

## 6. Conformance

Test unauthenticated access, unauthorized environment access, cross-environment requests, privilege escalation, wildcard overreach, stale approval, changed plan fingerprint, adapter-capability without caller permission, and pagination cursor reuse by a different actor.
