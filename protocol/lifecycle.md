# Lifecycle Protocol

**Status:** Draft specification, version 0.1.0-draft. This document defines proposed lifecycle semantics; it is not yet a stable protocol release or implemented state machine.

## 1. Purpose

This specification defines the lifecycle of a logical game server independently of its hosting provider, execution technology, game, programming language, and user interface.

It MUST be possible to implement this model using ordinary virtual machines and system services without Kubernetes, containers, or Agones. Those technologies MAY be used through optional adapters.

The lifecycle protocol is authoritative for legal server-state transitions and safety preconditions. An implementation MUST NOT invent additional successful transitions or silently weaken a precondition.

## 2. Separate resource and process models

Implementations MUST keep these concepts distinct:

- **Logical server:** durable identity and configuration for a game server. Its `server_id` survives replacement of the underlying machine.
- **Infrastructure instance:** a concrete provider resource, such as a VM. Its external reference is scoped to a hosting adapter and environment. A replacement receives a new instance reference.
- **Execution unit:** the managed service, process, container, pod, or allocated game-server workload that runs the game software.
- **Game runtime:** game-specific software and its observed condition, such as Paper installed, starting, healthy, or stopped.
- **Operation:** a durable request to perform work. Operation status is independent of server lifecycle state.

A logical server MAY exist without an infrastructure instance. A VM MAY exist while its game runtime is absent or stopped. A process being alive MUST NOT by itself mean that the logical server is ready.

Implementations SHOULD expose separate observed state for the instance and execution unit. At minimum, records MUST identify the logical lifecycle state, current instance reference (or absence), runtime condition, and last confirmed observation time.

## 3. Logical server lifecycle states

The following states are normative for this draft.

| State | Meaning |
|---|---|
| `unprovisioned` | Logical server exists, but no instance is assigned for the current deployment. |
| `provisioning` | A hosting instance is being created or adopted under an accepted operation. |
| `preparing` | The instance exists and is being bootstrapped, secured, and prepared for the selected execution and game release. |
| `restoring` | A specifically identified backup is being validated and restored to the target data location. |
| `starting` | The game execution unit is being started. |
| `validating` | Startup completed sufficiently to run required game, runtime, network, and dependency checks. |
| `ready` | Required readiness checks passed. The controller may permit routing according to policy; this state alone does not assert that a proxy has registered the backend. |
| `degraded` | The server exists but one or more required health conditions are failing or uncertain. It MUST NOT receive new player routing. |
| `draining` | New player routing is being prevented and existing players/sessions are being given the configured opportunity to leave. |
| `stopping` | The game execution unit is being stopped gracefully or, under an explicitly authorized policy, forcibly. |
| `backing_up` | A consistent backup is being captured, uploaded, and verified. |
| `stopped` | The logical server is retained, and its game execution unit is confirmed stopped. Its VM MAY remain allocated. |
| `destroying` | The assigned infrastructure instance is being destroyed after all required safety checks. |
| `destroyed` | The assigned instance is confirmed absent. The logical server record and backup/release references remain. |
| `failed` | Automation cannot safely continue. Known partial effects and resource references MUST be retained for recovery. |

The protocol does not use `deleted` as a synonym for `destroyed`. Permanent deletion of logical-server metadata and backup data is a separate administrative/data-retention concern and is outside this lifecycle state machine.

## 4. Legal transitions

Only the following transitions are permitted in this draft. A transition MUST be associated with a durable operation and recorded with its actor, cause, time, previous state, next state, and relevant evidence.

| From | To | Required reason / precondition |
|---|---|---|
| `unprovisioned` | `provisioning` | Authorized provision operation accepted. |
| `provisioning` | `preparing` | Exact instance creation/adoption is confirmed and recorded. |
| `provisioning` | `failed` | Provisioning definitively failed or requires operator intervention. |
| `preparing` | `restoring` | A specific backup was requested and restore prerequisites passed. |
| `preparing` | `starting` | No restore is required and preparation postconditions passed. |
| `preparing` | `failed` | Preparation cannot safely continue. |
| `restoring` | `starting` | Restore verification and postconditions passed and the workflow will start the game. |
| `restoring` | `stopped` | Restore verification passed and the requested final state is stopped. |
| `restoring` | `failed` | Restore or integrity validation failed. |
| `starting` | `validating` | Execution adapter confirms the start request reached its defined start postcondition. |
| `starting` | `failed` | Start definitively failed or outcome requires operator intervention. |
| `validating` | `ready` | All required readiness checks passed. |
| `validating` | `degraded` | Runtime exists but a required health/readiness condition failed or became uncertain. |
| `validating` | `failed` | Validation cannot safely continue. |
| `ready` | `degraded` | Required health is lost or cannot be established. |
| `ready` | `draining` | Shutdown, backup, replacement, or destroy workflow accepted. |
| `ready` | `stopping` | Only when the configured integration has no drainable sessions or an explicit policy authorizes skipping drain. |
| `degraded` | `validating` | Authorized recovery/revalidation operation accepted. |
| `degraded` | `draining` | Safe shutdown or backup workflow accepted. |
| `degraded` | `stopping` | Explicit recovery/shutdown policy permits stopping without a successful drain. |
| `degraded` | `failed` | Recovery is unsafe or cannot proceed automatically. |
| `draining` | `ready` | Drain was safely aborted and routing eligibility is revalidated. |
| `draining` | `stopping` | Drain completed or policy-defined drain deadline expired and its outcome was recorded. |
| `draining` | `failed` | Drain cannot be resolved safely. |
| `stopping` | `backing_up` | Runtime stop/save postconditions passed and backup is required. |
| `stopping` | `stopped` | Runtime is confirmed stopped and no backup step is required by this operation. |
| `stopping` | `failed` | Runtime stop outcome is failed or uncertain after reconciliation. |
| `backing_up` | `starting` | Backup is verified and the operation requested the server to return to service. |
| `backing_up` | `stopped` | Backup is verified and the requested final state is stopped. |
| `backing_up` | `destroying` | Backup is verified and all destroy checks are repeated successfully. |
| `backing_up` | `failed` | Backup capture, upload, verification, or reconciliation failed. |
| `stopped` | `starting` | Authorized start/restart operation accepted. |
| `stopped` | `restoring` | Authorized restore to the stopped server accepted. |
| `stopped` | `backing_up` | A stopped/offline backup operation is accepted. |
| `stopped` | `destroying` | Required backup and destruction checks passed. |
| `destroying` | `destroyed` | Exact provider instance absence is confirmed. |
| `destroying` | `failed` | Destruction failed or its outcome remains uncertain. |
| `destroyed` | `provisioning` | Authorized replacement/reprovision operation accepted for the same logical server. |
| `failed` | `provisioning` | Operator-authorized retry or replacement after reconciliation. |
| `failed` | `preparing` | Operator-authorized preparation recovery against a confirmed existing instance. |
| `failed` | `restoring` | Operator-authorized restore recovery against a confirmed target. |
| `failed` | `stopping` | Operator-authorized recovery shutdown of a confirmed execution unit. |
| `failed` | `destroying` | Only after reconciliation, verified backup, exact-target checks, and explicit destructive approval. |

Any transition not listed MUST be rejected with a structured conflict or invalid-transition error. An implementation MAY add internal substates, but MUST map them to the public states without changing the meaning of the public state.

A `failed` state is not an automatic retry state. Recovery requires a new, authorized operation that states the intended recovery action.

## 5. Required workflow semantics

### 5.1 Provision and start

1. Validate logical server, environment, policy, adapter capabilities, selected OS, bootstrap revision, and release.
2. Persist the operation and audit record before any external side effect.
3. Transition to `provisioning`; create the instance through the hosting adapter.
4. Reconcile and record the exact external instance reference.
5. Transition to `preparing`; apply the pinned bootstrap revision and validate host readiness.
6. Install/configure the selected game release through the game and execution adapters.
7. If a backup was explicitly selected, transition to `restoring`, validate its manifest/integrity/compatibility, then restore it.
8. Transition to `starting`; start the execution unit.
9. Transition to `validating`; run all required readiness checks.
10. Transition to `ready` only after those checks pass.

Provisioning MUST NOT silently choose the newest backup or overwrite existing data. A restore request MUST identify a specific backup ID or immutable manifest reference.

### 5.2 Start and restart

A start from `stopped` follows `starting` -> `validating` -> `ready`. A restart from `ready` follows `draining` -> `stopping` -> `starting` -> `validating` -> `ready`. The controller MUST NOT represent a restart as a successful process start while the prior execution unit may still be running.

### 5.3 Stop

A normal stop from `ready` follows `draining` -> `stopping` -> `stopped`. The game adapter MUST define the save/flush behavior and the execution adapter MUST report whether graceful stop completed. A forced stop is an explicit, separately authorized fallback and MUST be recorded as such.

### 5.4 Backup

A backup while running follows `ready` -> `draining` -> `stopping` -> `backing_up`. A stopped server follows `stopped` -> `backing_up`. Online/low-downtime backup is an optional capability and MUST declare its consistency guarantees; it MUST NOT be assumed by the core.

The backup operation MUST capture a manifest that identifies the logical server, environment, source instance/runtime, game and world versions, selected data paths, creation time, checksum algorithm and values, and source operation. A storage upload acknowledgment is not proof of a verified backup.

The operation MUST verify object presence, size, manifest, and configured integrity evidence before returning to `starting`, `stopped`, or `destroying`.

### 5.5 Backup and destroy

The safe default is `ready` -> `draining` -> `stopping` -> `backing_up` -> `destroying` -> `destroyed`.

Before `destroying`, the core MUST revalidate:
- Exact logical server, environment, hosting adapter, and instance reference.
- A verified backup associated with the target server and data generation.
- No conflicting restore, migration, or unresolved operation.
- Destructive authorization and approval bound to the exact target and plan.
- Provider capability and current observed instance identity.

A failed or uncertain backup MUST block destruction. An unknown destroy result MUST be reconciled before retry or final state assignment. The logical server and verified backup records remain after instance destruction.

### 5.6 Restore

Restore MUST use an explicit backup reference and validate integrity, ownership, environment, game compatibility, and archive safety before writing data. It MUST NOT extract paths outside the configured data root or follow unsafe archive links.

Restoring over existing data requires a separate safety plan and approval according to environment policy. A restore to a new replacement instance is preferred where practical. A successful restore can end in `stopped` or proceed to `starting`, according to the accepted operation.

### 5.7 Replacement

Replacement preserves `server_id` but creates a new instance reference. The old instance MUST remain recorded until its state and data disposition are reconciled. A replacement MUST restore from a specifically selected verified backup or use an explicitly documented data migration capability. The controller MUST NOT route to the replacement until it is `ready` and proxy integration confirms registration.

## 6. Readiness and routing

`ready` means all server-specific mandatory checks passed at a recorded time. A process merely existing, a TCP port merely listening, or a provider merely reporting a running VM is insufficient.

A readiness policy may include:
- Host/bootstrap completion.
- Runtime process state.
- Game-specific startup/readiness signal.
- Required game version and configuration.
- Required plugin/module checks.
- Local network reachability.
- Required storage/data checks.

Proxy registration is a separate integration action. A backend MUST NOT receive new players unless it is `ready`, the registration is confirmed, and current routing policy permits it. `degraded`, `failed`, and transitional states are not eligible for new routing. Draining removes eligibility before shutdown.

## 7. Instance and runtime observations

The controller SHOULD retain observed provider instance state separately from logical lifecycle state. Suggested normalized instance observations are `unknown`, `creating`, `active`, `stopping`, `stopped`, `destroying`, `absent`, and `error`.

The execution adapter SHOULD report `unknown`, `absent`, `installed`, `stopped`, `starting`, `running`, `healthy`, `unhealthy`, `stopping`, and `failed`. These observations do not replace logical lifecycle state and MUST include observation timestamps and source identity.

An observation may be stale. Implementations MUST distinguish last-known state from currently confirmed state and MUST NOT use stale observations to authorize destructive actions.

## 8. Operation relationship

Every mutating transition MUST be caused by one durable operation. The operation status model in [operations.md](operations.md) remains separate: an operation may be `running` while the server is `provisioning`, `backing_up`, or `destroying`.

A server state change and its operation/audit evidence MUST be committed consistently, or the implementation MUST document and test a recovery mechanism that prevents untracked transitions.

## 9. Concurrency and locks

Operations that can affect the same server, runtime, instance, or data MUST use the lock scopes defined by the operation protocol. At minimum, provision, start, stop, backup, restore, update, replacement, and destroy MUST NOT race against one another for the same logical server.

A controller restart or lost process MUST NOT release a lock while the associated operation has unknown external effects. Recovery MUST reconcile before allowing a conflicting operation.

## 10. Conformance requirements

A lifecycle-conforming implementation MUST:
- Implement every public state and legal transition in this document.
- Reject every unlisted transition.
- Preserve the logical server identity across instance replacement.
- Keep instance, execution-unit, and operation state distinct.
- Never route a non-ready or draining backend.
- Never destroy persistent data/compute under the safe-destroy workflow without a verified backup and exact-target authorization.
- Preserve partial effects on failure.
- Reconcile ambiguous provider/runtime outcomes before retry.
- Pass the lifecycle test vectors in [conformance](conformance/requirements.md).

## 11. Open review items

This draft intentionally leaves the following for maintainer review before stabilization:
- Whether forced stop requires a distinct public state or remains an operation detail.
- Exact policy for backups that use a game-native online snapshot capability.
- Exact definition of data-generation identity and backup freshness.
- Whether logical-server permanent retirement should be a separate administrative resource lifecycle.
- The minimum mandatory readiness checks for each game adapter.

## Related specifications

- [Common types](common-types.md)
- [Durable operations](operations.md)
- [Adapter contracts](adapters/README.md)
- [Conformance requirements](conformance/requirements.md)
