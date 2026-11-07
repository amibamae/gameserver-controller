# Architecture

**Status:** Proposed. The components and interfaces below are design targets, not implemented APIs.

## Goal

The Go controller is designed to coordinate game-server infrastructure without tying its core to one hosting company, storage vendor, or game. The first reference setup is Vultr, Wasabi, Paper, and Velocity. New providers and games should fit through adapters rather than provider-specific branches in lifecycle code. Contributions in other languages are welcome for adapters and clients.

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

The core owns workflow policy and state transitions. It does not make vendor API calls or issue game-specific commands directly.

- **Lifecycle:** desired state, operation coordination, locks, retries, deadlines, and safe shutdown.
- **Provisioning:** request instances through declared hosting capabilities, then apply a selected bootstrap revision and operator configuration.
- **Backup:** coordinate a consistent save, upload, verification, restore, and retention.
- **Release management:** track immutable software manifests, validation, approval, promotion, and retention.
- **Staging:** create isolated candidates from an explicitly selected production backup.
- **Rollback:** return to an earlier software or host baseline without violating world compatibility.
- **Security:** manage secret references, permissions, audit, and consent decisions.
- **Configuration:** validate settings and publish stable, versioned schemas.

## Adapter boundaries

The protocol works with ordinary virtual machines and service managers. Docker, Kubernetes, and Agones are optional integrations, not prerequisites. See [ADR 0003](decisions/0003-execution-agnostic-runtime.md).

### Hosting

A hosting adapter translates instance create, inspect, start, stop, reboot, and destroy requests into provider calls. It may also report regions, plans, standard OS offerings, bootstrap delivery, SSH keys, network state, or firewall capabilities. Vultr is the first target; OVH is a possible later portability check.

Capabilities vary by provider. The core must not assume that every provider offers the same OS catalog, bootstrap mechanism, private networking, or instance lifecycle.

### Storage

A storage adapter uploads and downloads objects, reports metadata and checksums, lists within an allowed scope, and deletes only when authorized. It declares features such as versioning or object lock. Wasabi’s S3-compatible API is the first target.

### Game

A game adapter defines installation, configuration, runtime needs, startup and shutdown, readiness, health, data paths, backup consistency, compatibility, and migration. Minecraft Java with Paper is the first target.

### Execution

An execution adapter runs and inspects a unit on an already provisioned environment. The first reference is systemd on a Linux VM. Optional adapters may target containers, Kubernetes, or Agones.

Keep game and execution behavior separate. The execution adapter starts a unit. The game adapter defines what to install, how to save it safely, when it is ready, and which versions can open its data.

### Integrations

Integrations connect the core to proxies, monitoring, alerts, and notifications. They use documented interfaces and cannot bypass lifecycle or privacy policy. Velocity is a proxy integration, not a hosting provider.

## Production and staging boundaries

- **Production** owns the live world and production backup write credentials. Staging cannot write there.
- **Staging** restores a timestamped copy of one selected production backup. It uses separate credentials with read-only production access, writes only to its own storage, and cannot sync the test world back into production.
- **Bootstrap execution** prepares a new instance from a provider-supported OS image. Bootstrap code contains no credentials or live world. Grant storage credentials separately and only to steps that restore data.

## Bootstrap and release lifecycles

Bootstrap scripts and game releases are separate, versioned artifacts. Validate and promote them independently.

### Bootstrap

Start from the provider’s standard OS offering. Apply one selected, versioned bootstrap revision and operator configuration. Validate it on fresh, isolated instances. Record the source revision, checksum, inputs, and outcome for each provision operation.

### Game releases

A release may contain Paper, plugin JARs, server configuration, plugin configuration, a manifest, checksums, and compatibility metadata. Keep world/player data, OS media, Java installations, rclone binaries, logs, and temporary staging files out of the release.

Treat artifacts as immutable. The proposed archive keeps five approved software releases, current plus four previous. Keep candidates until they are accepted or rejected. Do not delete anything an active deployment or rollback still references.

## Compatibility and rollback

Software rollback differs from bootstrap rollback. To restore an earlier host baseline, create an instance from a provider-standard OS and apply a previously approved bootstrap revision.

Every production backup records the Minecraft version and world-format compatibility. Before a major-version migration, keep a verified pre-migration backup. Never assume an older Paper release can open a world already migrated by a newer one.

Promoting a bootstrap or software release does not approve a world migration. Major Minecraft upgrades and migrations require explicit operator approval.

## Safety invariants

The lifecycle protocol defines public state transitions. These rules must hold across adapters:

1. Do not destroy an instance with unsaved data until its backup is uploaded and verified.
2. A failed or uncertain backup blocks destructive cleanup.
3. Staging cannot write to production backup storage.
4. Configuration stores secret references, not literal credentials.
5. Every operation is auditable and has explicit status, deadlines, and recovery behavior.
6. A failed candidate cannot silently replace an approved production release.
7. Destructive operations verify the exact target and environment.
8. Adapters cannot bypass core authorization, consent, or environment isolation.

Optional telemetry starts disabled. Identifiable player telemetry requires informed opt-in from both the server owner and each affected player. See [SECURITY.md](../SECURITY.md).

## Open design work

Go is selected for the initial implementation. The first durable operation store and in-process adapter boundary now exist. Remaining implementation work includes configuration and secret resolution, durable step dispatch and reconciliation, workflow orchestration, audit and event storage/retention, production adapter implementations, per-game health checks, and approval rules for destructive actions.
