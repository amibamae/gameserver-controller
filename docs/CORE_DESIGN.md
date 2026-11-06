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

- **Environment:** isolation boundary such as production, staging, or a provider-backed test environment.
- **Logical server:** deployment identity independent of a VM.
- **Instance:** concrete provider resource hosting a server.
- **Backup:** timestamped data snapshot with integrity metadata.
- **Release:** immutable software/configuration artifacts and manifest.
- **Bootstrap revision:** versioned source-code and configuration reference used to prepare an instance created from a provider-supported standard OS offering.
- **Operation:** durable record of a requested action, as defined in the [durable operation protocol](../protocol/operations.md).
- **Capability:** operation or feature declared by an adapter.
- **Approval:** explicit authorization for a gated action.
- **Secret reference:** pointer to protected secret storage, never the secret value.

External resource IDs are opaque and scoped to adapter and environment.

## Lifecycle states

Proposed server states: `unprovisioned`, `provisioning`, `preparing`, `starting`, `validating`, `ready`, `draining`, `stopping`, `backing_up`, `stopped`, `destroying`, `destroyed`, `degraded`, and `failed`.

These server lifecycle states are separate from operation statuses. An operation can be `running` while a server is `starting`, for example. The controller validates server-state transitions and records the operation responsible for each mutating transition.

Normal startup is provision → prepare → start → validate → ready. Routing is allowed only in `ready`. Shutdown drains players before stopping. Destruction is allowed only after a verified backup and all authorization/target checks pass. A failed backup blocks destruction and leaves the instance allocated. A failed health check blocks readiness.

The final legal transition table, including restart, degraded recovery, and replacement-instance behavior, remains to be specified in the protocol lifecycle document.

## Durable operation model

The [durable operation protocol](../protocol/operations.md) is the detailed draft contract for acceptance, status, steps, idempotency, timeouts, retries, cancellation, progress, partial outcomes, reconciliation, and recovery.

At a minimum, the controller persists:
- Unique operation ID, type, schema/protocol version, trusted actor, target server, and environment.
- Accepted request parameters with secret references only.
- Creation, update, start, and completion timestamps as applicable.
- Current operation status and step.
- Idempotency key and request fingerprint where supported.
- Adapter identity and opaque external resource/operation references.
- Retry/attempt history, deadline, structured outcome, error category, and recovery guidance.
- Related audit event IDs.

The operation record MUST be durably committed before any external side effect. A timeout or lost connection does not establish whether an external action succeeded. Unknown outcomes require reconciliation before retry or dependent action. The protocol does not assume exactly-once execution across provider boundaries.

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

Provisioning never implies permission to overwrite or migrate an existing world. Each external step has durable intent and recorded outcome. An ambiguous result is reconciled before the workflow continues.

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

Any failure in save, upload, verification, authorization, or target validation halts the sequence. Never destroy as best-effort cleanup. If the destroy request outcome is ambiguous, reconcile the exact provider instance before taking further action.

### Restore and staging

Restore resolves a specific backup ID, validates integrity and version compatibility, and requires approval when replacing data or crossing a major-version boundary. Extraction must prevent path traversal and unsafe symlinks.

Staging records the exact production backup and timestamp, uses read-only production backup access, restores to staging-owned storage, and cannot sync data back into production. Keep staging available until explicitly accepted, rejected, or retired.

## Concurrency and recovery

Use an exclusive per-server operation lock for operations that mutate runtime or data. Backup, restore, upgrade, stop, and destroy must not race. Locks have an owner operation ID and timeout. A stale lock requires inspection and reconciliation of its owning operation before release. Deduplicate repeated requests with idempotency keys where supported. State storage must support atomic operation/state updates or a documented recovery mechanism.

After a controller restart, every non-terminal operation must be inspected and either safely resumed, reconciled, or suspended for operator intervention. An unresolved operation blocks conflicting mutations on the affected resource.

Errors use the structured categories in [common protocol types](../protocol/common-types.md): validation, authorization, unsupported capability, not found, conflict, transient, provider failure, integrity failure, compatibility failure, safety blocked, timeout, cancelled, or internal. Errors and logs must redact secrets and unnecessary player data.

## State and audit

Persist environment/server definitions, resource references, lifecycle state, operations and recovery markers, backup/release manifests, approvals, adapter versions/capabilities, and audit events. Audit records include actor, action, target, time, decision, and outcome. Retention and integrity requirements must be defined before production.

Operation history and security audit are related but distinct records; they should be linked while retaining the audit trail's own integrity and access controls.

## Phase 1 non-goals

No production credentials; no calls to Vultr, Wasabi, Velocity, or live Minecraft; no real provisioning, backup, restore, routing, or destroy; no automatic update promotion; no major-version migration; no telemetry implementation; no selected language/database/job queue.

Phase 1 is complete when contracts, schemas, state model, fake adapters, and contract tests are reviewed and agreed.
