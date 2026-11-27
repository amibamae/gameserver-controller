# Recovery Staging Runbook

**Date:** 2026-11-26  
**Issue:** #425 - Durable Paper Recovery  
**Environment:** Isolated Vultr staging VM + Wasabi storage  

## Purpose

This runbook documents test vectors for validating the `server.recover` operation on an isolated disposable Vultr staging VM with separate Wasabi objects. It tests failure modes including false positives, failed saves, early returns, restart timeouts, stale Done markers, controller crashes, backup corruption, and actual restored scenarios.

## Safety Constraints

- No production fault injection
- No restart bypassing save contract
- No automatic VM create/destroy without explicit approval
- No exposure of Wasabi/Vultr secrets or raw player logs
- All testing uses isolated credentials and objects

## Setup

```bash
# Create a disposable staging server
gsc serve -config configs/controller.example.yaml \
  --controller.version=0.21.0-draft \
  --controller.state-store.path=/tmp/gsc-staging-state.db \
  --controllersetup.vultr.host=https://api.staging-vultr.amibamae.dev \
  --controllersetup.vultr.token-file=./staging-vultr-token \
  --controllersetup.vultr.ssh-key-file=./staging-vultr-ssh

# Create a staging server definition
gsc api post /v1/servers \
  --server.id=test-recover-staging \
  --server.environment=staging \
  --server.lifecycle-state=unprovisioned
```

## Test Vectors

### T1: Healthy recovery (baseline)

- **Intent:** revalidate
- **Precondition:** Server in `degraded` state with actual health issues that resolve
- **Expected:** Transition to `ready`, result shows `recovery_outcome: success`

### T2: False positive recovery detection

- **Setup:** Server actually healthy but marked degraded (simulated via API)
- **Action:** Attempt revalidate recovery
- **Expected:** Controller should detect server is already healthy; remain in current state or transition safely

### T3: Failed save request

- **Setup:** Server running but unable to save (e.g., disk full)
- **Action:** Request save, then restart
- **Expected:** Save fails, operation enters `failed` state, no data loss until verified backup exists

### T4: Early return detection

- **Setup:** Server process exits immediately after starting
- **Action:** Attempt restart recovery
- **Expected:** Controller detects early exit via readiness check; transition to `failed` or `backing_up`

### T5: Restart timeout

- **Setup:** JVM takes too long to start (memory pressure)
- **Action:** Execute recovery with default budget
- **Expected:** Operation respects recovery_budget.max_duration, transitions appropriately when timeout fires

### T6: Stale Done marker

- **Setup:** Controller crashes mid-operation after partial progress
- **Action:** Restart controller and inspect state
- **Expected:** Recovery does not replay unknown operations; reconciles before reattempting

### T7: Backup readback corruption

- **Setup:** Intentionally corrupt a Wasabi object in staging bucket
- **Action:** Attempt restore from corrupted backup
- **Expected:** Restore fails with integrity error, transition to `failed`

### T8: Actual isolated restore validation

- **Setup:** Create fresh staging VM, run full lifecycle including backup
- **Action:** Attempt restore and verify world data matches expected state
- **Expected:** Restore succeeds with verified checksums

## Running Tests

```bash
# Run all test vectors sequentially
gsc pilot recovery-test \
  --test-vectors=T1,T2,T3,T4,T5,T6,T7,T8 \
  --environment=staging \
  --server=test-recover-staging

# Or run individual tests
gsc pilot recovery-test T3 --force-save-failure

# View test results
gsc api get /v1/servers/test-recover-staging
```

## Cleanup

```bash
# Destroy staging after testing (with verified backup)
gsc server destroy \
  --server=test-recover-staging \
  --environment=staging \
  --force=true
```

## Results Report

After each test vector, record:
- Operation ID and result
- Recovery steps taken
- Any warnings emitted
- Final lifecycle state
- Evidence for CI verification (logs, metrics)
