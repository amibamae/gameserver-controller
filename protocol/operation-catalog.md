# Standard Operation Catalog

**Status:** Draft, protocol version 0.20.0-draft.

This catalog defines the initial standard management operations. An implementation MUST use the operation names and semantics here when it advertises them. Vendor/game-specific operations may be namespaced extensions and MUST NOT redefine a standard operation.

## 1. Common rules

Every mutating request is accepted as a durable operation according to [operations.md](operations.md). The operation request is immutable after acceptance. The controller MUST persist the operation and audit record before any external side effect.

Every operation is scoped to an authenticated actor, logical server (except initial definition), and environment. The controller checks:
- Actor authentication and authorization.
- Current server state and conflicting operation locks.
- Adapter descriptors, required capabilities, and permissions.
- Environment and target-resource ownership.
- Required approvals and policy.
- Exact operation-specific parameters and schema version.

A request's `parameters` object MUST validate against the schema named in the table below. For `POST /v1/servers/{server_id}/operations`, the path `server_id` MUST equal any `server_id` in the parameters object. The target environment MUST be resolved and checked before acceptance. An implementation MUST NOT accept arbitrary unvalidated fields. Unknown fields are rejected except where the selected schema explicitly defines `extensions`.

## 2. Standard operations

| Operation type | Parameters schema | Purpose |
|---|---|---|
| `server.define` | `server-definition.schema.json` | Create a logical server record in `unprovisioned` state; no provider resource is created. |
| `server.provision` | `provision-request.schema.json` | Create a provider instance, prepare it, optionally restore one explicit backup, start, and validate. |
| `server.start` | `start-parameters.schema.json` | Start the recorded instance/runtime and validate readiness. |
| `server.stop` | `stop-parameters.schema.json` | Drain when configured, save/stop the game, and leave the logical server stopped. |
| `server.backup` | `backup-parameters.schema.json` | Capture, upload, and verify a consistent backup. |
| `server.restore` | `restore-parameters.schema.json` | Restore one explicit verified backup to a prepared target. |
| `server.destroy` | `destroy-parameters.schema.json` | Ensure a verified backup, then destroy the exact recorded instance. |

`server.replace` is intentionally not a standard 0.20.0-draft operation. Safe replacement may require simultaneous old/new instance references and cutover semantics beyond the single-current-instance model. It will be specified only after a multi-instance transition model is reviewed.

## 3. Operation semantics

### 3.1 `server.define`

Creates a logical server definition and initial lifecycle record `unprovisioned`. It MUST NOT create infrastructure, install software, or access game data. The typed result identifies the new record by `server_id` and `environment_id`, confirms its initial `unprovisioned` state, and contains no external affected resources. The management API exposes the record at `GET /v1/servers/{server_id}`. The server ID MUST be unique within the controller installation.

### 3.2 `server.provision`

Creates a new instance from a provider-supported standard OS offering, runs a pinned bootstrap revision, installs/configures the selected game release and execution unit, optionally restores the specific `restore_backup_id`, then starts and validates the runtime.

The request MUST identify the hosting adapter, region, plan, OS offering, bootstrap source/revision/digest, game release, and execution profile. A backup ID is never inferred. On partial failure, the created instance reference is retained and the operation records known side effects; automatic cleanup is not assumed safe.

### 3.3 `server.start`

Starts the current recorded execution unit on the current recorded instance. The operation MUST verify the exact instance reference before dispatch. Success requires runtime start postconditions and game readiness validation. Repeated start requests may return success without starting a duplicate process only when inspection confirms the runtime is already ready.

### 3.4 `server.stop`

Default behavior is `drain=true` and `force=false` when omitted. It drains new player routing when the configured proxy integration supports it, requests game-native save/flush, gracefully stops the execution unit, and confirms termination. Force stop is not a default behavior. If force stop is supported, it requires a separate capability, permission, explicit `force=true` parameter, and a valid `approval_id`; the parameter schema rejects `force=true` without an approval reference. Environment policy may impose additional approval requirements.

A successful stop leaves the logical server `stopped`; it does not destroy the VM or delete data.

### 3.5 `server.backup`

Captures a consistent backup using the game adapter's declared consistency mode, transfers objects, writes a manifest, and verifies every required object. If the server was `ready`, omitted `resume_after_backup` defaults to `true`; it requests restart and readiness validation after verification. Setting it to `false` leaves the server stopped. If the server was already `stopped`, it remains stopped. A failed or unverified backup MUST NOT be recorded as verified.

### 3.6 `server.restore`

Restores the immutable backup identified by `backup_id`. Integrity, environment ownership, data format, and game compatibility MUST pass before data is made available to the game runtime. Replacing existing data requires `replace_existing_data=true` plus explicit approval where policy requires it. Default final state is `stopped`; `start_after_restore` defaults to `false` and when set to `true` triggers startup and validation. `replace_existing_data` defaults to `false`.

### 3.7 `server.destroy`

Destroys only the exact `expected_instance_ref` attached to the logical server. It MUST verify or create a backup for the current known `data_generation_id` using `backup_profile` before transition to `destroying`. A `verified_backup_id` MAY be supplied as a candidate, but the controller MUST independently verify ownership, exact generation match, integrity status, and policy. A missing/unknown current generation blocks destruction until a new consistent backup is created.

Production policy SHOULD require a separate approval bound to the exact destroy plan. The controller rechecks target identity, backup verification, environment, locks, and approval immediately before provider dispatch. Success means provider absence is confirmed; the logical server remains in `destroyed` state and retains backup references.

## 4. Required postconditions

An operation MUST NOT become `succeeded` until its listed postconditions are confirmed.

| Operation | Required postcondition |
|---|---|
| `server.define` | Logical server exists in `unprovisioned`; no infrastructure side effect occurred. |
| `server.provision` | Instance reference recorded; host preparation, optional restore, runtime start, and readiness all passed; state `ready`. |
| `server.start` | Current runtime is ready and required checks passed; state `ready`. |
| `server.stop` | Runtime is confirmed stopped and save/flush result is recorded; state `stopped`. |
| `server.backup` | Manifest and all required objects pass integrity/ownership checks; final state matches resume policy. |
| `server.restore` | Selected backup is restored and game validation passes; final state is `stopped` or `ready` according to request. |
| `server.destroy` | Exact provider instance absence confirmed; verified backup remains associated with the logical server; state `destroyed`. |

## Result schemas

Successful operation results use the canonical envelope from [operation-result.schema.json](schemas/operations/operation-result.schema.json). The current typed payload schemas are:

| Operation | Result schema |
|---|---|
| `server.define` | [server-define-result.schema.json](schemas/operations/server-define-result.schema.json) |
| `server.provision` | [server-provision-result.schema.json](schemas/operations/server-provision-result.schema.json) |
| `server.start` | [server-start-result.schema.json](schemas/operations/server-start-result.schema.json) |
| `server.stop` | [server-stop-result.schema.json](schemas/operations/server-stop-result.schema.json) |
| `server.backup` | [server-backup-result.schema.json](schemas/operations/server-backup-result.schema.json) |
| `server.restore` | [server-restore-result.schema.json](schemas/operations/server-restore-result.schema.json) |
| `server.destroy` | [server-destroy-result.schema.json](schemas/operations/server-destroy-result.schema.json) |

Every standard operation now has a registered typed result schema; the conformance suite checks the operation type and exact target binding.

## 5. Input schemas

Schemas are published under [schemas/operations](schemas/operations/). Provisioning uses [hosting/provision-request.schema.json](schemas/hosting/provision-request.schema.json). The schema registry is [schemas/index.json](schemas/index.json).

## 6. Extending the operation catalog

A new standard operation requires:
1. A stable namespaced operation type.
2. Exact input and output schemas.
3. Authorization and approval requirements.
4. Preconditions, state transitions, and postconditions.
5. Idempotency, cancellation, timeout, and reconciliation rules.
6. Positive and negative conformance vectors.
7. Documentation of compatibility and versioning.

A private extension MAY use a reverse-domain namespaced operation type, but MUST NOT use the `server.*` namespace reserved for standard operations.
