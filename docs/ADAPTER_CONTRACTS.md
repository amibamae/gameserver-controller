# Adapter Contracts

**Status:** Proposed language-neutral contracts, not implemented APIs.

## Contract principles

The detailed, language-neutral contracts are published under [protocol/adapters](../protocol/adapters/README.md). The five categories are hosting, storage, execution, game, and integration.

- Core defines contracts; adapters implement them.
- Adapters declare identity, version, configuration schema, capabilities, constraints, and required permissions.
- Optional capabilities are discoverable before execution.
- Unsupported operations return a structured `unsupported_capability` result; never silently emulate them.
- External IDs are opaque and scoped to adapter and environment.
- Mutating calls receive operation/correlation IDs and idempotency keys where supported.
- Adapters redact secrets and sensitive player data.
- Adapters cannot bypass core authorization, environment isolation, safety policy, or consent enforcement.

## Common adapter descriptor

Each adapter exposes a stable ID (for example `hosting.vultr`), display name, adapter version, contract version, configuration schema version, capabilities, required permission scopes, read-only diagnostics, and compatibility documentation. Core records the descriptor/capability snapshot used for each operation.

## Hosting-provider contract

Responsibilities: translate logical infrastructure requests into provider-specific operations; own provider API details, resource IDs, asynchronous polling, and error normalization.

Proposed operations:
- Discover capabilities, regions, plans, and provider-supported operating-system offerings/versions where supported.
- Inspect an instance.
- Create an instance from a provider-neutral specification.
- Wait for provider operation completion.
- Start, stop, reboot, and destroy instances where supported.
- Inspect networking/firewall state where supported.
- Apply or report the status of a referenced bootstrap mechanism where supported.

An instance specification may include region, plan, provider OS selection (using the provider's opaque OS identifier plus a human-readable distribution/version), hostname/labels, SSH key references, firewall policy references, protected bootstrap reference and revision, environment, logical server identity, architecture, and minimum resource requirements. It must never contain raw passwords, private keys, API tokens, or storage credentials.

Example namespaced capabilities: `hosting.instance.create`, `hosting.instance.inspect`, `hosting.instance.start`, `hosting.instance.stop`, `hosting.instance.destroy`, `hosting.bootstrap.deliver`, and `hosting.firewall.manage`. Core must not assume every provider exposes the same OS catalog, bootstrap-delivery mechanism, private networking, or lifecycle semantics.

## Storage-provider contract

Responsibilities: upload/download objects, inspect metadata, list objects where supported, verify integrity, and perform deletion only when explicitly authorized.

Proposed operations:
- Discover capabilities.
- Put/get object.
- Inspect object metadata (HEAD).
- List objects where supported.
- Delete object only with explicit authorization.
- Calculate/verify checksums where supported.
- Copy objects and inspect versioning where supported.

Record adapter/bucket identity, object key, length, checksum algorithm/digest, timestamps, artifact category, logical server/environment, source operation ID, and encryption/version metadata when available.

An accepted upload is not automatically a verified backup. Core must require verification before destructive cleanup. Production backup credentials are not available to staging for writes/deletes. Release artifact permissions are separate from world backup permissions.

## Execution contract

Execution adapters manage a process or workload unit on an already provisioned environment. The initial reference is `execution.systemd` on a conventional Linux VM. Container, Kubernetes, and Agones implementations are optional. The execution adapter reports process/unit state; the game adapter defines installation, save semantics, game readiness, data paths, and version compatibility. See the [execution adapter protocol](../protocol/adapters/execution.md).

## Execution contract\n\nExecution adapters manage the process or workload on an already provisioned environment. The initial reference is systemd on a conventional Linux VM. Container, Kubernetes, and Agones implementations are optional. See the [execution adapter protocol](../protocol/adapters/execution.md).\n\n## Game-server contract

Responsibilities: describe and control a game runtime, including installation, configuration, save semantics, readiness, data paths, version compatibility, and migration.

Proposed operations:
- Describe runtime and supported versions.
- Validate installation.
- Install a specific release.
- Inspect runtime/process state.
- Start and gracefully stop.
- Request save/flush.
- Run readiness and health checks.
- Describe data paths and backup consistency method.
- Validate a backup.
- Inspect source/target version compatibility.
- Perform migration only when explicitly supported and separately approved.

A running process is not necessarily ready. The Paper adapter may use startup logs as one signal, but must also verify expected health signals and required plugin checks. It distinguishes absent, starting, running-not-ready, ready, degraded, stopped, and failed states.

The game adapter describes how to save/quiesce data consistently. Core coordinates storage transfer and verification; copying files alone does not prove a valid backup.

## Integration contract

Integrations connect core to Velocity, monitoring, alerts, or notifications. They declare supported events/actions, permissions, mutating/read-only behavior, retry/idempotency semantics, timeout behavior, transmitted fields, and privacy implications.

A proxy integration may inspect backends, register/unregister them, route selected players only after core confirms readiness, drain a backend, and report player counts. It must never route to a provisioning, starting, validating, degraded, or failed backend. Broad "send all" operations require explicit core selection and validation of every affected player/destination.

## Shared request/result conventions

Requests include operation ID, correlation ID, logical server/environment, deadline, idempotency key when applicable, actor/authorization context from core, and secret references only.

Results include status, observed state, opaque resource/provider operation references, structured error category, safe diagnostics, retryability, and recovery guidance.

Normalize errors into the taxonomy in [CORE_DESIGN.md](CORE_DESIGN.md). Raw provider responses may be retained only after redaction and under explicit diagnostic policy.

## Contract tests

Every adapter must test:
- Valid descriptor and capability declaration.
- Explicit failure for unsupported capabilities.
- Read-only inspection has no side effects.
- Mutating calls correlate to operation IDs.
- Timeout and transient-error classification.
- Secret and sensitive-data redaction.
- Environment/resource reference isolation.
- Destructive calls require core-issued authorization context.
- Idempotent retries do not duplicate resources where provider semantics allow.
- Partial provider failures are reported accurately.

Core tests use fake adapters. Real adapter tests run only against isolated test resources, never production.

## Initial adapter mapping

| Contract | Initial target | Possible later targets |
|---|---|---|
| Hosting | Vultr | OVH, other providers |
| Storage | Wasabi (S3-compatible) | Other object stores |
| Execution | systemd on Debian VM | Container runtime, Kubernetes, Agones |
| Execution | systemd on Debian VM | Container runtime, Kubernetes, Agones |
| Game | Minecraft Java / Paper | Terraria, Valheim, ARK |
| Execution | systemd on Debian VM | Container runtime, Kubernetes, Agones |\n| Integration | Velocity | Other proxies, monitoring, notifications |

This mapping is a starting point, not a claim of identical provider/game capabilities.
