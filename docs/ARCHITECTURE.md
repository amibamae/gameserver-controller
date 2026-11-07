# Architecture

**Status:** Proposed. Some protocol tooling and local controller components now exist; the full lifecycle controller and production adapters remain design targets.

## Goal

I want the Go controller to coordinate game-server infrastructure without tying its core to one cloud provider, storage vendor, or game. The first reference stack is Vultr, Wasabi, Paper, and Velocity. A new provider or game should fit through an adapter, not a maze of provider-specific branches in lifecycle code. Contributors can build adapters and clients in other languages.

## Components

~~~text
Operator / CLI / API
        |
Core Controller
   | Lifecycle Orchestrator
   | Release Manager
   | Policy and Security
        |
Adapter Contracts
   | Hosting     | Storage      | Execution      | Game
   | Vultr       | Wasabi       | systemd        | Minecraft/Paper
        |
Integrations: Velocity, monitoring, notifications
~~~

## What the core owns

The core owns workflow policy and state transitions. It does not call vendor APIs or issue game-specific commands directly.

- **Lifecycle:** desired state, operation coordination, locks, retries, deadlines, and safe shutdown.
- **Provisioning:** request instances through declared hosting capabilities, then apply a selected bootstrap revision and operator configuration.
- **Backup:** coordinate a consistent save, upload, verification, restore, and retention.
- **Release management:** track immutable software manifests, validation, approval, promotion, and retention.
- **Staging:** create isolated candidates from an explicitly selected production backup.
- **Rollback:** return to an earlier software or host baseline without violating world compatibility.
- **Security:** manage secret references, permissions, audit, and consent decisions.
- **Configuration:** validate settings and publish stable, versioned schemas.

## Adapter boundaries

The protocol works with ordinary VMs and service managers. Docker and Kubernetes are optional integrations, not prerequisites. See [ADR 0003](decisions/0003-execution-agnostic-runtime.md).

### Hosting

A hosting adapter handles instance creation, inspection, start, stop, reboot, and destroy calls. It may also report regions, plans, standard OS offerings, bootstrap delivery, SSH keys, network state, or firewall capabilities. Vultr is the first target; OVH may help test portability later.

Provider capabilities differ. The core must not assume the same OS catalog, bootstrap mechanism, private networking, or instance lifecycle everywhere.

### Storage

A storage adapter uploads and downloads objects, reports metadata and checksums, lists within an allowed scope, and deletes only when authorized. It declares features such as versioning or object lock. Wasabi's S3-compatible API is the first target.

### Game

A game adapter defines installation, configuration, runtime needs, startup and shutdown, readiness, health, data paths, backup consistency, compatibility, and migration. Minecraft Java with Paper is first.

### Execution

An execution adapter runs and inspects a unit on an already provisioned environment. The first reference is systemd on a Linux VM. Optional adapters may target containers or Kubernetes.

Keep game behavior separate from process management. The execution adapter starts a unit. The game adapter says what to install, how to save it safely, when it is ready, and which versions can open its data.

### Integrations

Integrations connect the core to proxies, monitoring, alerts, and notifications. They follow documented interfaces and cannot bypass lifecycle or privacy policy. Velocity is a proxy integration, not a hosting provider.

## Production and staging boundaries

- **Production** owns the live world and production backup write credentials. Staging cannot write there.
- **Staging** restores a timestamped copy of one selected production backup. It uses separate credentials with read-only access to production, writes only to staging storage, and cannot sync the test world back into production.
- **Bootstrap** prepares a new instance from a provider-supported OS image. Bootstrap code contains no credentials or live world. Grant storage credentials separately, only to the steps that restore data.

## Bootstrap and release lifecycles

Bootstrap scripts and game releases are separate, versioned artifacts. Validate and promote them independently.

### Bootstrap

Start from the provider's standard OS offering. Apply one selected, versioned bootstrap and operator configuration. Validate it on fresh, isolated instances. Record the source revision, checksum, inputs, and result for each provision operation.

### Game releases

A release may include Paper, plugin JARs, server and plugin configuration, a manifest, checksums, and compatibility metadata. Keep world/player data, OS media, Java installations, rclone binaries, logs, and temporary staging files out of the release.

Treat releases as immutable. The proposed archive keeps five approved software releases, the current release plus four previous ones. Keep candidates until accepted or rejected. Never delete an artifact that an active deployment or rollback still references.

## Compatibility and rollback

Software rollback and bootstrap rollback are different operations. To restore an earlier host baseline, create an instance from a provider-standard OS and apply a previously approved bootstrap revision.

Every production backup records its Minecraft version and world-format compatibility. Keep a verified pre-migration backup before a major-version change. Do not assume an older Paper release can open a world already migrated by a newer one.

Promoting software or bootstrap does not approve a world migration. Major Minecraft upgrades and migrations require explicit operator approval.

## Safety invariants

The lifecycle protocol defines public state transitions. These rules must hold across adapters:

1. Do not destroy an instance with unsaved data until its backup is uploaded and verified.
2. A failed or uncertain backup blocks destructive cleanup.
3. Staging cannot write to production backup storage.
4. Configuration stores secret references, not credential values.
5. Every operation is auditable and has explicit status, deadlines, and recovery behavior.
6. A failed candidate cannot silently replace an approved production release.
7. Before destructive work, verify the exact target and environment.
8. Adapters cannot bypass core authorization, consent, or environment isolation.

Optional telemetry starts disabled. Identifiable player telemetry requires informed opt-in from the server owner and each affected player. See [SECURITY.md](../SECURITY.md).

## Remaining design work

Go is selected for the initial implementation. The operation store and in-process adapter boundary now exist. Still to build: runtime config and secret resolution, durable step dispatch and reconciliation, workflow orchestration, audit/event retention, production adapters, per-game health checks, and approval rules for destructive actions.