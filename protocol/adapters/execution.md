# Execution Adapter Contract

**Status:** Draft, version 0.19.0-draft.

## 1. Purpose

The execution adapter manages the unit that runs game software on an already provisioned execution environment. It separates process/workload management from hosting-provider VM lifecycle and game-specific installation/save semantics.

An execution environment may be a conventional VM, bare-metal host, container runtime, or an orchestration platform. No container engine, Kubernetes cluster, or Agones installation is mandatory.

## 2. Reference and optional implementations

The initial reference implementation is `execution.systemd`, managing a dedicated service on a standard Linux VM.

Optional future implementations may include:
- `execution.container`: a container runtime on an existing host.
- `execution.kubernetes`: workloads scheduled by Kubernetes.
- `execution.agones`: Agones GameServer lifecycle/allocation integration.

These are examples, not required dependencies or predefined guarantees. Each must satisfy the same contract and declare its additional constraints.

## 3. Capability names

| Capability | Meaning |
|---|---|
| `execution.unit.create` | Define/register an execution unit |
| `execution.unit.inspect` | Inspect exact unit and observed state |
| `execution.unit.start` | Start the execution unit |
| `execution.unit.graceful_stop` | Request a graceful stop |
| `execution.unit.force_stop` | Force stop after policy approval |
| `execution.unit.restart` | Restart with documented semantics |
| `execution.unit.logs.read` | Read bounded, redacted diagnostics |
| `execution.unit.health.inspect` | Inspect process/unit health |
| `execution.unit.delete` | Remove unit definition without deleting persistent game data |

An adapter MUST declare which capabilities require an existing host, which create separate infrastructure, and what happens to persistent data when a unit is deleted.

## 4. Execution unit specification

A unit specification SHOULD include:
- Logical server and environment IDs.
- Execution adapter identity and target environment reference.
- Game adapter/release reference.
- Immutable executable or artifact references and integrity digests.
- Non-secret launch arguments and environment configuration.
- Working directory and declared data mounts/paths.
- Resource limits/requirements where supported.
- Network ports/protocols where supported.
- Secret references with explicit purpose and scope.
- Health and shutdown policy references.

Secrets MUST be resolved by an authorized controller/runner channel at point of use. They MUST NOT be embedded in process arguments, container images, user-data, or ordinary environment dumps/logs where avoidable.

## 5. Lifecycle semantics

Systemd start and graceful-stop capabilities use operation-specific result schemas. A completed start confirms the unit is running but does not establish game readiness. Accepted and unknown outcomes carry an external operation reference; an unknown outcome requires inspection before retry.

- Create defines the unit and does not imply it is running.
- Start returns accepted/observed state separately; readiness is established by the game adapter and controller validation workflow.
- Graceful stop asks the game runtime to save and exit using the game adapter's save semantics, then waits for process/unit termination.
- Force stop is an exceptional capability. The controller MUST validate an approval bound to the exact unit and reason before dispatch; the input schema requires an approval reference. It must report that graceful save could not be confirmed. A completed result confirms only that the unit stopped, not that game data was saved. Accepted and unknown outcomes carry a reference; unknown requires inspection before retry.
- Inspect reports current observed state, identity, version, timestamps, and safe diagnostics.
- Delete removes only the execution-unit definition. It MUST NOT delete world data, backups, or the logical server record unless a separate contract explicitly authorizes that effect. Its result MUST explicitly confirm persistent data was preserved.
- Logs are read with a caller-supplied byte bound, counting UTF-8 line bytes and one newline separator between adjacent lines, and MUST be redacted before leaving the adapter. Health inspection reports `unknown` when it cannot confirm health; it MUST NOT infer healthy from an active process.

A timeout while starting/stopping MUST be reconciled by inspection. It MUST NOT be interpreted as a known stopped/running result.

Any unit result whose unit reference does not exactly match the request is untrusted. The controller MUST inspect the requested exact unit before retrying or doing dependent work.

## 6. Relationship to the game adapter

The execution adapter knows how to run and stop a unit. The game adapter knows what software to install, how to configure it, how to save/quiesce it consistently, what data is persistent, how to check game-level readiness, and what versions are compatible.

For example, a systemd execution adapter can start a Paper service, while the Paper game adapter checks the expected Minecraft version, startup completion, required plugins, and world-save semantics.

For Agones, the execution/orchestration adapter manages GameServer allocation and lifecycle, while a game-specific bridge reports readiness/health to the Agones SDK. The controller still owns backup verification and safe-destroy policy.

## 7. Conformance cases

Test create without start, start timeout, inspect reconciliation, graceful stop, force-stop authorization, unit deletion preserving data, secret redaction, and advertised capability behavior.
