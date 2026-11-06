# Adapter Contracts

**Status:** Proposed language-neutral contracts. No adapter API is implemented yet.

The detailed drafts live under [protocol/adapters](../protocol/adapters/README.md). They define five categories: hosting, storage, execution, game, and integration.

## Shared rules

- The core defines each contract; adapters implement it.
- An adapter declares its identity, version, configuration schema, capabilities, constraints, permissions, and compatibility.
- The controller discovers optional capabilities before it calls them.
- If an operation is unsupported, return a structured unsupported-capability result. Do not imitate it silently.
- Treat provider identifiers as opaque and scope them to both adapter and environment.
- Mutating calls carry operation and correlation IDs, plus an idempotency key where supported.
- Redact secrets and unnecessary player data from diagnostics.
- Adapters cannot bypass core authorization, environment isolation, safety policy, or consent enforcement.

Each adapter descriptor includes a stable ID such as hosting.vultr, a display name, adapter and contract versions, a configuration schema version, capabilities, required permission scopes, read-only diagnostics, and compatibility notes. The core records the descriptor and capability snapshot used for each operation. A descriptor describes behavior; it does not grant permission.

## Hosting providers

The hosting adapter translates provider-neutral requests into provider API calls. It handles provider IDs, asynchronous polling, and error normalization. Depending on declared capabilities, it can discover regions, plans, and OS offerings; inspect, create, start, stop, reboot, or destroy an instance; inspect network or firewall state; and deliver or report bootstrap status.

A create request can include an opaque provider OS ID, readable distribution and version, region, plan, hostname, labels, SSH key references, firewall policy references, a protected bootstrap reference and revision, environment, logical server ID, architecture, and minimum resources.

Never include passwords, private keys, API tokens, or storage credentials. Do not assume providers share the same OS catalog, bootstrap mechanism, networking, or lifecycle behavior.

## Storage providers

The storage adapter uploads, downloads, inspects, lists, verifies, copies, and, when explicitly authorized, deletes objects. It declares optional capabilities such as versioning and server-side copy.

A backup record should capture adapter and bucket identity, object key, length, checksum algorithm and digest, timestamps, artifact category, server and environment, source operation, and available encryption or version metadata.

An upload acknowledgment is not a verified backup. The core must confirm integrity before it destroys compute. Staging may read selected production backups but cannot write to or delete them. Release-artifact permissions stay separate from world-backup permissions.

## Execution environments

The execution adapter manages a process or workload on a provisioned host. The first reference is systemd on a Linux VM. Containers, Kubernetes, and Agones are optional.

The execution adapter reports the service state. The game adapter defines installation, save behavior, readiness, data paths, and version compatibility. See the [execution protocol](../protocol/adapters/execution.md).

## Game servers

The game adapter defines installation, configuration, runtime needs, readiness and health checks, persistent paths, backup consistency, compatibility, and migration.

Its proposed operations cover release description, install, validation, start and graceful stop, save/flush, health and readiness, data-path description, backup validation, compatibility checks, and explicitly supported migrations.

A running process is not necessarily ready. For Paper, startup logs may be one signal; the adapter must also check required health signals and plugins. It must distinguish states such as starting, running but not ready, ready, degraded, stopped, and failed.

The game adapter says how to quiesce or save data consistently. The core coordinates transfer and verification. Copying files alone does not prove a backup is valid.

## Integrations

Integrations connect the core to Velocity, monitoring, alerts, and notifications. They declare supported events and actions, permissions, whether calls mutate state, retry and idempotency behavior, timeout rules, transmitted fields, and privacy implications.

A proxy adapter may inspect, register, drain, and unregister a backend, and report player counts. It routes players only after the core confirms readiness. It must not route to a provisioning, starting, validating, degraded, or failed backend. Any broad transfer must name and validate every affected player and destination.

## Request and result shape

Requests carry an operation ID, correlation ID, logical server and environment where applicable, deadline, idempotency key when supported, trusted authorization context from the core, and secret references only.

Results report status, observed state, opaque resource or provider-operation references, a structured error category, safe diagnostics, retryability, and recovery guidance.

Normalize errors using the categories in [core design](CORE_DESIGN.md). Keep raw provider responses only after redaction and under an explicit diagnostic policy.

## Contract tests

Test each adapter for:

- A valid descriptor and declared capabilities.
- Explicit errors for unsupported capabilities.
- Side-effect-free read-only inspection.
- Operation IDs on mutating calls.
- Timeout and transient-error classification.
- Secret and sensitive-data redaction.
- Environment and resource-reference isolation.
- Core-issued authorization for destructive calls.
- Safe idempotent retries where the provider supports them.
- Accurate reporting of partial provider failures.

Use fake adapters for core tests. Test real adapters only against isolated resources, never production.

## First adapter targets

| Contract | Reference target | Possible later target |
|---|---|---|
| Hosting | Vultr | OVH or another provider |
| Storage | Wasabi through its S3-compatible API | Other object stores |
| Execution | systemd on a Debian VM | Containers, Kubernetes, Agones |
| Game | Minecraft Java with Paper | Terraria, Valheim, ARK |
| Integration | Velocity | Other proxies, monitoring, notifications |

This is the first design target. It does not promise that providers or games expose identical capabilities.