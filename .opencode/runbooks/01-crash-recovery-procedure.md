# Crash Recovery Procedure Runbook

**Document Version:** 0.1.0  
**Last Updated:** 2026-11-21  
**Target Availability:** 99.99% (four nines)

---

## Quick Reference

| Scenario | Action | Expected Result |
|----------|--------|-----------------|
| **Process crashes during operation** | Restart binary → Call `Recover()` | Operations restored from WAL replay |
| **Disk corruption detected** | Restore from last verified backup | Data integrity preserved |
| **Multiple operations pending recovery** | Review each in order of age | Safe completion or fencing |
| **Uncertain step requires manual intervention** | Use `Resume()` with operator authorization | Reconciliation and safe retry/finish |

---

## Procedure 1: Normal Restart Recovery

### When to Execute

- Controller process terminated unexpectedly (OOM, signal, power loss)
- No disk errors reported in system logs
- Database file readable but contains incomplete operations

### Steps

```bash
# 1. Verify database is accessible
cd /path/to/controller
ls -la *.db

# 2. Restart the Go binary
go run ./cmd/server.go > controller.log 2>&1 &

# 3. Run recovery to list interrupted operations
go run ./cmd/recover --output recover-report.json

# 4. Review report for operations needing attention
cat recover-report.json | jq '.[] | select(.status == "needs_review")'
```

### Expected Behavior

- **Recovery scan:** 2-5 seconds for typical database size
- **Operations listed:** Only non-terminal operations (Running/Reconciling states)
- **Succeeded/Failed operations:** Ignored by recovery (already terminal)

### Verification

```bash
# Count recovered operations
go run ./cmd/recover --count | grep "Total:"

# List by operation type
go run ./cmd/recover --type=backup_captures
```

---

## Procedure 2: Operator Intervention for Uncertain Steps

### When to Execute

- Operation marked `outcome_unknown` after crash
- Step dispatched but response not recorded in store
- External side effect may have occurred (adapter call completed but result lost)

### Decision Tree

```
┌─────────────────────────────┐
│  Does step have external     │
│  side effects?              │
└────────────┬────────────────┘
             │ YES
             ▼
    ┌─────────────────────┐
    │ Can you verify      │
    │ external state?     │
    └──────────┬───────────┘
               │ YES → Proceed to step 3
               │ NO → Proceed to manual verification (step 4)
             ▼ YES
    ┌─────────────────────┐
    │ Call adapter directly│
    │ with idempotency key│
    │ from store record    │
    └──────────┬───────────┘
               │
               ▼
        Record result in store
```

### Manual Verification Steps

```bash
# 1. Get operation details
go run ./cmd/recover --operation-id <ID>

# 2. Check adapter status (for backup capture)
go run ./cmd/hosting-instance-status --instance-id <INSTANCE_ID>

# 3. Verify game state integrity
go run ./cmd/game-verify --world-id <WORLD_ID>

# 4. Document findings and decide:
#    - External side effect occurred → Complete operation
#    - No evidence of side effect → Safe to retry or finish
```

---

## Procedure 3: Backup Verification Before Destruction

### When to Execute

- About to delete older backups (retention policy enforcement)
- Periodic backup health check (recommended daily)
- After successful crash recovery from recent backup

### Pre-Destruction Checklist

```bash
# CRITICAL: Never delete backup without verification

# 1. Run integrity check on latest production backup
go run ./cmd/backup-verify --path /backups/latest --checksum

# 2. Verify backup contains complete operation history
go run ./cmd/backup-inspect --path /backups/latest | grep "Operations:"

# 3. Confirm recent operations are in backup (last 24 hours)
go run ./cmd/backup-inspect --path /backups/latest \
  --after "$(date -u -d '24 hours ago' +%s)"

# 4. Only proceed if all checks pass
```

### Verification Commands

```bash
# List backup files and sizes
ls -lh /backups/*.tar.gz

# Check most recent backup timestamp
stat /backups/latest.tar.gz | grep Modify

# Validate backup integrity (no checksum file available)
tar -tzf /backups/latest.tar.gz | wc -l
```

---

## Procedure 4: Disk Corruption Recovery

### When to Execute

- Filesystem reports errors in journal/log
- Database file shows corruption indicators
- bbolt recovery mode triggers on open

### Steps

```bash
# 1. Stop write operations immediately
go run ./cmd/server.go --disable-writes &

# 2. Check for filesystem errors
dmesg | tail -50 | grep -i error

# 3. Attempt bbolt recovery (read-only mode)
mv *.db *.db-old
bbolt db.db open=false --copy --recovery db-recovered.db

# 4. Verify recovered database is readable
ls -lh db-recovered.db

# 5. Restore from latest verified backup if recovery fails
go run ./cmd/restore-from-backup \
  --path /backups/latest.tar.gz \
  --destination *.db
```

### Post-Recovery

- **If successful:** Resume normal operations, monitor for corruption indicators
- **If failed:** Restore from backup and investigate root cause

---

## Emergency Contacts

| Role | Contact | Availability |
|------|---------|--------------|
| On-call SRE | PagerDuty: gameserver-controller-oncall | 24/7 |
| Database Team | Teams channel: #database-support | Business hours |
| Security Response | GitHub private security reporting | Anytime |

---

## Escalation Matrix

| Issue Severity | Response Time | Escalate To |
|----------------|---------------|-------------|
| Data loss detected (critical) | Immediate | Security team + Platform engineering |
| Multiple operations unrecoverable (high) | 15 minutes | Reliability engineer |
| Single operation needs review (medium) | 4 hours | On-call developer |

---

## Appendix A: Recovery Command Reference

```bash
# List all recoverable operations
go run ./cmd/recover --list

# Recover specific operation by ID
go run ./recover --operation-id <ID> --force-completion

# Export recovery report as JSON
go run ./recover --output recovery-report.json

# Verify backup integrity
go run ./backup-verify --path /backups/latest --checksum

# Restore from backup (emergency)
go run ./restore-from-backup \
  --path /backups/latest.tar.gz \
  --destination *.db
```

---

**Document Status:** Initial version  
**Review Date:** Next quarter review or after incident  
