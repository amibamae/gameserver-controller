# Security Model

This document outlines the security model, threat boundaries, and controls for Game Server Controller. It describes how the system protects against common attack vectors while maintaining operational safety.

---

## Overview

The controller applies defense-in-depth with:

- **Least privilege** - Adapters operate with minimal permissions
- **Secret separation** - Credentials use systemd credential store
- **Safe failure** - Uncertain outcomes trigger reconciliation before retry
- **Auditable operations** - Every action is logged with context

---

## Threat Model

### Asset Categories

1. **Player Data** - Worlds, inventory, chat history
2. **Server Credentials** - Cloud provider tokens, API keys
3. **Operational State** - bbolt database with server definitions
4. **Destructive Actions** - VM destruction, data deletion

### Threat Actors

- **External attackers**: Compromised servers or storage buckets
- **Misconfigured operators**: Accidental destructive commands
- **Malicious adapters**: Provider implementations with unexpected behavior
- **Environment issues**: Cloud provider outages, credential rotation

---

## Key Security Controls

### Secret Management

Credentials never appear in:
- Configuration files (use `secret_refs` to systemd credentials)
- Logs or audit trails
- Memory after use

Example:
```yaml
adapters:
  - secret_refs:
      access_key_id:
        secret_ref: systemd-credential://wasabi-access-key-id
```

### Environment Isolation

Staging and production environments are strictly separated:

- **Production adapters**: Only in config for `environment: production` servers
- **Staging credentials**: Separate from production, even if same provider
- **No cross-write**: Staging cannot write to production storage

### Safe Failure Pattern

Timeouts don't assume success:

```go
// Pseudocode
if operation timed out {
  // Don't assume it succeeded or failed
  reconcile actual state with provider
  if uncertain {
    retry or mark as error
  }
}
```

### Destructive Action Gating

Before destroying a server:

1. Backup uploaded to storage?
2. Backup verified (checksum match)?
3. Staging has no data in that bucket?

All three must be true before destruction proceeds.

---

## Security Boundaries

### Core vs Adapter

| Boundary | Core Controller | Adapter |
|----------|-----------------|---------|
| Makes safety decisions | **Yes** | No |
| Calls vendor APIs | **No** | Yes |
| Knows about staging/prod boundaries | Enforces boundary | Reports environment |
| Owns durable operations | Yes | No |

Adapters report what they observe; core decides policy. Adapters cannot override authorization or bypass lifecycle rules.

### Credential Accessor

The controller reads secrets only through the systemd credential accessor:

```go
secret, err := credentialAccessor.Get(ctx, "wasabi-access-key-id")
```

This ensures:
- Secrets never in config files
- Proper isolation between operators
- Audit trail for secret access attempts

---

## Production Security Checklist

Before deploying to production:

- [ ] All secrets use `systemd-credential://` references
- [ ] Staging credentials separated from production
- [ ] bbolt state store on read-only or restricted filesystem
- [ ] Cloud provider API tokens have least privilege
- [ ] DNS handoff configured for direct connections
- [ ] Audit logging enabled and reviewed

---

## Incident Response

### Compromised Adapter

If an adapter is compromised:

1. Revoke its credentials immediately
2. Check audit logs for unauthorized operations
3. Restore state from last verified backup if needed
4. Update credential rotation policies

### Unauthorized Server Destruction

If a server was destroyed without proper backup verification:

1. Contact cloud provider to check billing and storage
2. Review audit logs for the destruction operation
3. If production data was lost, report incident with logs
4. Update lifecycle rules to prevent recurrence

---

## Privacy Considerations

Telemetry is off by default. Player-identifying telemetry (if needed) requires:

1. Separate informed consent from server owner
2. Separate opt-in from each affected player
3. Explicit data retention policy
4. No cross-server correlation without consent

---

*Last updated: November 2026*