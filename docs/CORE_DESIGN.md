# Core Design and Lifecycle Model

**Status:** Phase 1 specification. These are design targets, not implemented APIs.

## Purpose

The controller coordinates disposable game-server infrastructure through provider-independent contracts. Core owns orchestration policy, state transitions, authorization, recovery, and audit. Adapters own vendor/game-specific API calls and report observed facts; they do not override core policy.

## Layers

1. Operator surface: CLI initially; future API/UI may use the same application services.
2. Application services: provision, inspect, start, backup, restore, stage, release, rollback, destroy.
3. Domain core: lifecycle state machine, policy, capability checks, compatibility, operation records.
4. Ports: hosting, storage, game runtime, proxy/integration contracts.
5. Adapters: Vultr, Wasabi, Minecraft/Paper, Velocity and future implementations.
6. Infrastructure: configuration, secret resolution, durable state, audit, process supervision.

Core must not import provider SDKs or game-specific libraries directly.

## Domain concepts

- **Environment:** isolation boundary such as production, staging, or image-build.
- **Logical server:** deployment identity independent of a VM.
- **Instance:** concrete provider resource hosting a server.
- **Backup:** timestamped data snapshot with integrity metadata.
- **Release:** immutable software/configuration artifacts and manifest.
- **Image:** provider-specific infrastructure image.
- **Operation:** durable record of a requested action.
- **Capability:** operation or feature declared by an adapter.
- **Approval:** explicit authorization for a gated action.
- **Secret reference:** pointer to protected secret storage, never the secret value.

External resource IDs are opaque and scoped to adapter and environment.

## Lifecycle states

Proposed states: `unprovisioned`, `provisioning`, `preparing`, `starting`, `validating`, `ready`, `draining`, `stopping`, `backing_up`, `stopped`, `destroying`, `destroyed`, `degraded`, and `failed`.

Core validates transitions. Normal startup is provision → prepare → start → validate → ready. Routing is allowed only in `ready`. Shutdown drains players before stopping. Destruction is allowed only after a verified backup and all authorization/target checks pass. A failed backup blocks destruction and leaves the instance allocated. A failed health check blocks readiness.

## Durable operation model

Every mutating use case creates an operation record before its first external side effect. Minimum fields:
- Unique operation ID, type, schema version, actor, target server, and environment.
- Requested parameters with secret references only.
- Start/update/completion timestamps.
- Current step and status.
- Idempotency key where supported.
- Adapter identity and opaque external resource/operation references.
- Retry count, deadline, structured outcome, error category, recovery guidance.
- Related audit event IDs.

Statuses: `pending`, `running`, `waiting`, `succeeded`, `failed`, `cancelled`, `needs_operator`.

Use bounded retries, deadlines, and backoff. Distinguish transient transport errors from confirmed provider failures. A restart must recover or reconcile an interrupted operation, not pretend it never happened.

## Main workflows

### Provision and start
1. Validate request, environment, policy, and adapter capabilities.
2. Acquire an exclusive per-server operation lock.
3. Persist operation and audit record.
4. Create/allocate instance through hosting adapter.
5. Wait for provider completion and persist resource reference.
6. Prepare instance and restore only the explicitly selected verified backup, if required.
7. Start game runtime.
8. Run game-specific readiness and health checks.
9. Mark ready only after all required checks pass.
10. Release lock and report outcome.

Provisioning never implies permission to overwrite or migrate an existing world.

### Backup and destroy
1. Validate exact target and environment.
2. Stop new routing and drain players when proxy integration exists.
3. Save and stop game cleanly.
4. Capture a consistent backup.
5. Upload through storage adapter.
6. Verify object existence, metadata, size, and checksum per policy.
7. Persist verified backup record.
8. Re-check target identity, authorization, and backup association.
9. Request provider destruction and confirm absence.
10. Mark destroyed while retaining backup/release references.

Any failure in save, upload, verification, authorization, or target validation halts the sequence. Never destroy as best-effort cleanup.

### Restore and staging
Restore resolves a specific backup ID, validates integrity and version compatibility, and requires approval when replacing data or crossing a major-version boundary. Extraction must prevent path traversal and unsafe symlinks.

Staging records the exact production backup and timestamp, uses read-only production backup access, restores to staging-owned storage, and cannot sync data back into production. Keep staging available until explicitly accepted, rejected, or retired.

## Concurrency and recovery

Use an exclusive per-server lock for operations that mutate runtime or data. Backup, restore, upgrade, stop, and destroy must not race. Locks have an owner operation ID and timeout. A stale lock requires operation inspection before release. Deduplicate repeated requests with idempotency keys where supported. State storage must support atomic operation/state updates or a documented recovery mechanism.

Errors are structured: validation, authorization, unsupported capability, not found, conflict, transient, provider failure, integrity failure, compatibility failure, safety blocked, or internal. Errors and logs must redact secrets and unnecessary player data.

## State and audit

Persist environment/server definitions, resource references, lifecycle state, operations and recovery markers, backup/release manifests, approvals, adapter versions/capabilities, and audit events. Audit records include actor, action, target, time, decision, and outcome. Retention and integrity requirements must be defined before production.

## Phase 1 non-goals

No production credentials; no calls to Vultr, Wasabi, Velocity, or live Minecraft; no real provisioning, backup, restore, routing, or destroy; no automatic update promotion; no major-version migration; no telemetry implementation; no selected language/database/job queue.

Phase 1 is complete when contracts, schemas, state model, fake adapters, and contract tests are reviewed and agreed.
