# Core Design and Lifecycle Model

**Status:** Phase 1 specification. This describes design targets, not implemented APIs.

## Purpose

The controller coordinates disposable game-server infrastructure through provider-neutral contracts. The core owns lifecycle policy, authorization, state transitions, recovery, and audit. Adapters make vendor- or game-specific calls and report observed facts. They cannot override core policy.

## Layers

1. **Operator interface:** CLI first; future API or UI may use the same application services.
2. **Application services:** provision, inspect, start, backup, restore, stage, release, roll back, and destroy.
3. **Domain core:** lifecycle state machine, policy, capability checks, compatibility, and operation records.
4. **Ports:** hosting, storage, game-runtime, proxy, and integration contracts.
5. **Adapters:** Vultr, Wasabi, Minecraft/Paper, Velocity, and future implementations.
6. **Infrastructure:** configuration, secret resolution, durable state, audit, and process supervision.

The core must not import provider SDKs or game-specific libraries directly.

## Shared concepts

- **Environment:** an isolation boundary such as production, staging, or a provider-backed test.
- **Logical server:** the durable identity of a deployment, independent of its VM.
- **Instance:** the concrete provider resource that hosts a server.
- **Backup:** a timestamped data snapshot with integrity metadata.
- **Release:** immutable software and configuration artifacts plus a manifest.
- **Bootstrap revision:** a versioned source and configuration reference used to prepare a standard provider OS.
- **Operation:** a durable work request, as defined in the [operation protocol](../protocol/operations.md).
- **Capability:** an operation an adapter declares it supports.
- **Approval:** explicit authorization for a gated action.
- **Secret reference:** a pointer to protected storage, never a secret value.

External resource IDs stay opaque and are scoped to both adapter and environment.

## Lifecycle states

The [lifecycle protocol](../protocol/lifecycle.md) defines the draft state machine:

unprovisioned, provisioning, preparing, restoring, starting, validating, ready, degraded, draining, stopping, backing_up, stopped, destroying, destroyed, and failed.

Keep four kinds of state separate:

- A logical server keeps its identity when its VM is replaced.
- An infrastructure instance is a provider resource that may not exist or may be replaced.
- An execution unit is the process, service, container, or workload running the game.
- An operation tracks one request and its own status and recovery record.

A live process is not proof of readiness. The controller may route players only when the logical server is ready, its readiness evidence is current, and the proxy confirms registration. It MUST NOT route to transitional, degraded, or failed backends.

## Execution model

The first reference is a provider VM prepared by a versioned bootstrap script, with the game managed by systemd. Containers and Kubernetes are not required. The [adapter specifications](../protocol/adapters/README.md) keep hosting, storage, execution, game, and integration behavior separate.

## Durable operations

The [operation protocol](../protocol/operations.md) defines acceptance, status, steps, idempotency, deadlines, retries, cancellation, progress, partial outcomes, reconciliation, and recovery.

At minimum, persist the operation ID and type; protocol version; trusted actor; target server and environment; validated request and secret references; timestamps; status and current step; idempotency key; adapter and provider references; attempts, deadline, outcome, structured error, recovery guidance, and audit links.

Commit an operation record before any external side effect. A lost response does not prove an action failed or succeeded. Reconcile uncertain outcomes before retrying. The protocol does not promise exactly-once execution across provider boundaries.

## Main workflows

### Provision and start

1. Validate the request, environment, policy, and adapter capabilities.
2. Lock the server against conflicting operations.
3. Persist the operation and audit record.
4. Create an instance through the hosting adapter and record its exact reference.
5. Prepare the instance and restore only the explicitly selected verified backup, if required.
6. Start the game.
7. Run game-specific readiness and health checks.
8. Mark the server ready only after every required check passes.

Provisioning does not grant permission to overwrite or migrate an existing world. Record the intent and result of each external step. Reconcile an ambiguous result before proceeding.

### Back up and destroy

1. Verify the exact target and environment.
2. Stop new routing and drain players if a proxy integration is configured.
3. Save and stop the game cleanly.
4. Capture a consistent backup, upload it, and verify its objects and metadata.
5. Record the verified backup.
6. Re-check target identity, authorization, and the backup association.
7. Destroy the provider instance and confirm it is absent.
8. Mark the instance destroyed while retaining backup and release references.

Any save, upload, verification, authorization, or target-check failure stops this workflow. Never use destroy as best-effort cleanup. If the destroy response is ambiguous, inspect the exact provider instance before doing anything else.

### Billing-aware scaling

Hosting providers such as Vultr bill each instance in whole increments (one hour) from creation until it is destroyed. The core uses each instance for the time already charged:

- `idle_shutdown` is the minimum idle time, not the shutdown time. An idle server is backed up and destroyed only in the decision window near the end of its current paid increment (by default minutes 58 to 59 of each hour since creation). Until then it stays up, so returning players reuse it instead of starting a new, separately billed instance.
- With a server running, adding capacity also waits for that server's decision window. Starting a server when none is running is never delayed.
- A server whose configured hourly cost is below the owner's threshold skips the window and scales as soon as it is idle.
- The window only decides *when* the core may start a scaling action. Back up and destroy still runs every check above, and a failed check stops it.

The policy lives in `internal/scalingpolicy` and is configured per server under `lifecycle.scaling` (see [configuration](CONFIGURATION.md#scaling-and-billing)). Hosting adapters may declare their billing increment with the `billing_increment_seconds` descriptor constraint. The runtime scheduler that applies it is future work.

### Restore and stage

Restore one identified backup. Check its integrity and version compatibility; require approval when replacing data or crossing a major-version boundary. Reject archive traversal and unsafe symlinks.

For staging, record the selected production backup and timestamp. Give staging read-only production access, restore into staging-owned storage, and block any path that can write the staged world back to production. Keep the deployment until an operator accepts, rejects, or retires it.

## Concurrency and recovery

Use an exclusive per-server lock for mutations to runtime or data. Backup, restore, upgrade, stop, and destroy must not race. Record the owning operation and deadline. Inspect and reconcile a stale lock before releasing it.

After a restart, inspect every non-terminal operation. Resume only when safe; otherwise reconcile it or suspend it for an operator. An unresolved operation blocks conflicting work on that resource.

Use the structured errors in [common protocol types](../protocol/common-types.md): validation, authorization, unsupported capability, not found, conflict, transient, provider failure, integrity failure, compatibility failure, safety blocked, timeout, cancelled, and internal. Redact secrets and unnecessary player data.

## State and audit

Persist environment and server definitions, resource references, lifecycle state, operation and recovery records, backup and release manifests, approvals, adapter versions and capabilities, and audit events.

Audit records identify the actor, action, target, time, decision, and outcome. Link operation history to the audit trail, but keep separate integrity and access controls. Define retention and integrity requirements before production.

## Phase 1 excludes

Phase 1 has no production credentials or calls to Vultr, Wasabi, Velocity, or live Minecraft. It does not provision, back up, restore, route, destroy, promote updates, migrate worlds, or implement telemetry. The language, database, and job queue are not yet selected.

Phase 1 is complete when maintainers review and agree on the contracts, schemas, state model, fake adapters, and contract tests.