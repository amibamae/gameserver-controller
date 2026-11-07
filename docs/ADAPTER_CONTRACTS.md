# Adapter Contracts

**Status:** The contracts remain language-neutral. The repository has an initial Go in-process adapter API; production adapters and the optional process transport are not implemented.

The detailed drafts live under [protocol/adapters](../protocol/adapters/README.md): hosting, storage, execution, game, and integration.

## What exists in Go

The Go registry validates adapter descriptors and capability inputs and outputs against the bundled protocol schemas. Scripted in-process fakes cover all five categories for local tests. This gives the core a boundary to test against; it does not implement lifecycle policy, durable dispatch, authorization, or provider behavior.

Before an adapter step runs, the operation records its validated input and exact adapter/capability version. A confirmed result records the schema-validated output. Durable step snapshots may contain secret references, never resolved credentials.

## Rules shared by every adapter

- The core defines the contract; the adapter implements it.
- Each adapter declares its identity, version, configuration schema, capabilities, constraints, permissions, and compatibility.
- The controller checks optional capabilities before calling them.
- An unsupported capability must return a structured error. Do not silently imitate it.
- Keep provider identifiers opaque and scoped to the adapter and environment.
- Mutating calls carry operation and correlation IDs, plus an idempotency key when the capability supports one.
- Redact credentials and unnecessary player data from diagnostics.
- Adapters cannot bypass core authorization, environment isolation, safety policy, or consent rules.

A descriptor records a stable ID such as `hosting.vultr`, display name, adapter and contract versions, configuration schema version, capabilities, permission scopes, read-only diagnostics, and compatibility notes. It describes behavior; it does not grant permission. The core records which descriptor and capability snapshot an operation used.

## Hosting providers

The hosting adapter translates provider-neutral requests into provider API calls. Depending on its declared capabilities, it can list regions, plans, and OS offerings; inspect, create, start, stop, reboot, or destroy an instance; inspect network or firewall state; and deliver a bootstrap reference or report its status.

A create request may name an opaque OS offering ID, readable distribution and version, region, plan, hostname, labels, SSH-key and firewall-policy references, a protected bootstrap revision, environment, logical server ID, architecture, and minimum resources.

Never send passwords, private keys, API tokens, or storage credentials in the request. Providers do not share one OS catalog, bootstrap mechanism, network model, or lifecycle contract.

## Storage providers

The storage adapter uploads, downloads, lists, inspects, verifies, copies, and deletes objects when the action is explicitly authorized. It declares optional features such as versioning and server-side copy.

A backup record should identify the adapter and bucket, object key, length, checksum algorithm and digest, timestamps, artifact type, server and environment, source operation, and available encryption or version metadata.

An upload acknowledgment does not verify a backup. The core must confirm integrity before destroying compute. Staging may read selected production backups, but cannot write to or delete them. Keep release-artifact permissions separate from world-backup permissions.

## Execution environments

The execution adapter manages a process or workload on a provisioned host. The first reference is systemd on a Linux VM. Containers and Kubernetes remain optional.

The execution adapter reports service state. The game adapter defines installation, save behavior, readiness, data paths, and version compatibility. See the [execution contract](../protocol/adapters/execution.md).

## Game servers

The game adapter defines installation and configuration, runtime needs, health and readiness, persistent paths, backup consistency, compatibility, and migration.

Its proposed operations include release description, install and validation, start and graceful stop, save or flush, health and readiness checks, data-path description, backup validation, compatibility checks, and supported migrations.

A running process may still be unready. Paper startup logs are one signal; the adapter also needs to check required health signals and plugins. It must distinguish starting, running but not ready, ready, degraded, stopped, and failed.

The game adapter explains how to save data consistently. The core coordinates transfer and verification. Copying files by itself does not prove a backup is valid.

## Integrations

Integrations connect the core to Velocity, monitoring, alerts, and notifications. Each declares events and actions, permissions, side effects, retry and idempotency behavior, timeouts, transmitted fields, and privacy implications.

A proxy adapter may inspect, register, drain, and unregister a backend and report player counts. It routes players only after the core confirms readiness. It must not route to a backend that is provisioning, starting, validating, degraded, or failed. A broad transfer must identify and validate every affected player and destination.

## Request and result shape

Requests carry an operation ID, correlation ID, logical server and environment where relevant, deadline, idempotency key when supported, authorization context supplied by the core, and secret references only.

Results report status, observed state, opaque resource or provider-operation references, a structured error category, safe diagnostics, retryability, and recovery guidance.

Normalize errors using the categories in [core design](CORE_DESIGN.md). Keep raw provider responses only after redaction and under an explicit diagnostic policy.

## Contract tests

Test every adapter for:

- A valid descriptor and declared capabilities.
- A clear error for unsupported capabilities.
- Side-effect-free read-only inspection.
- Operation IDs on mutating calls.
- Correct classification of timeouts and transient errors.
- Redaction of credentials and sensitive data.
- Environment and resource-reference isolation.
- Core-issued authorization for destructive calls.
- Safe idempotent retries where the provider supports them.
- Accurate reporting of partial provider failures.

Use fake adapters for core tests. Test production adapters only against isolated resources, never production.

## First adapter targets

| Contract | First target | Possible later target |
|---|---|---|
| Hosting | Vultr | OVH or another provider |
| Storage | Wasabi through its S3-compatible API | Other object stores |
| Execution | systemd on Debian VM | Containers, Kubernetes |
| Game | Minecraft Java with Paper | Terraria, Valheim, ARK |
| Integration | Velocity | Other proxies, monitoring, notifications |

These are the first design targets, not a promise that providers or games expose identical capabilities.