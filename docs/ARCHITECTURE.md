# Architecture

**Status:** Proposed architecture. Interfaces and components in this document are design targets, not implemented APIs.

## Goals

The platform coordinates game-server infrastructure while keeping the orchestration core independent of hosting vendors, storage vendors, and game implementations. The first reference deployment is Vultr + Wasabi + Minecraft Paper + Velocity. Other adapters should be addable without embedding their behavior in core lifecycle code.

## High-level components

```text
                    Operator / CLI / API
                             |
                      Core Controller
          +------------------+------------------+
          |                  |                  |
      Lifecycle           Release            Policy /
      Orchestrator        Manager            Security
          |                  |                  |
          +------------------+------------------+
                             |
                       Adapter Contracts
          +------------------+------------------+
          |                  |                  |
     Hosting Adapter    Storage Adapter     Game Adapter
       (Vultr)             (Wasabi)         (Minecraft/Paper)
          |                  |                  |
       Provider           Object store       Game process,
       API / VM            backups and        config, health,
       lifecycle           release files      compatibility
                             |
                       Integrations
                    (Velocity, monitoring,
                     notifications, etc.)
```

## Core responsibilities

Core owns orchestration policy and state transitions, not vendor-specific API calls or game-specific commands.

- **Lifecycle:** desired state, operation coordination, locks, retries, timeouts, and safe shutdown.
- **Provisioning:** request and track instances through hosting adapter capabilities, then apply a selected, versioned bootstrap script and user configuration.
- **Backups:** coordinate consistent backup, upload, verification, restore, and retention workflows.
- **Releases:** immutable release manifests, validation, approval, promotion, and retention.
- **Staging:** isolated candidate environments based on selected production backup snapshots.
- **Rollback:** coordinate software and infrastructure rollback while respecting world compatibility.
- **Security:** credential references, permission boundaries, audit events, and consent enforcement.
- **Configuration:** validate configuration and expose stable, versioned schemas.

## Adapter boundaries

### Hosting-provider adapter

A hosting adapter translates platform operations into provider-specific API calls. Potential capabilities include instance create/inspect/start/stop/reboot/destroy, region and plan discovery, standard operating-system selection, bootstrap delivery/execution where supported, networking/firewall management, SSH key assignment, and asynchronous operation status.

Adapters declare capabilities. Core must not assume every provider exposes the same operating-system catalog, bootstrap-delivery mechanism, private networking, or lifecycle semantics. Initial target: Vultr. OVH is a possible later adapter to validate provider portability.

### Storage-provider adapter

A storage adapter provides upload/download, object metadata and checksums, listing, retention, and safe deletion where permitted. It must declare optional capabilities such as versioning or object locking rather than assume all object stores behave alike. Initial target: Wasabi through its S3-compatible interface.

### Game-server adapter

A game adapter describes installation, runtime requirements, configuration, startup/shutdown, readiness/health, data directories, backup consistency, version compatibility, and migration behavior. Initial target: Minecraft Java Edition with Paper.

### Integrations

Integrations connect the core to proxies, monitoring, alerts, and notifications. They must use documented interfaces and must not bypass lifecycle safety or privacy controls. Velocity is an initial proxy integration, not a hosting provider.

## Environments and data isolation

- **Production** owns the live world and production backup write credentials. It must not accept staging writes.
- **Staging** receives a copy of a selected production backup, records its timestamp, and uses separate credentials with read-only access to production backup objects. It writes only to its own workspace and explicitly authorized release/test destinations. It must never sync its world into production.
- **Bootstrap execution** prepares a newly provisioned instance from a provider-supported standard operating-system offering. Bootstrap code must not contain credentials or live world data; access to storage credentials is granted separately and only when a restore operation requires it.

## Provisioning and software-release lifecycle

Bootstrap scripts and game software releases are independently versioned artifacts with separate validation and promotion.

### Bootstrap lifecycle

Instances begin with a standard operating-system offering supplied by the hosting provider. The controller then applies a selected bootstrap revision and user configuration. Bootstrap scripts are source-controlled, customizable, reviewed, and validated by provisioning fresh isolated instances. Record the exact source revision, integrity checksum, configuration inputs, and outcome for each provisioning operation. The default workflow depends only on provider-supported operating-system offerings and versioned bootstrap scripts.

### Game software releases

A release may include Paper, plugin JARs, server configuration, plugin configuration, a manifest, checksums, and compatibility metadata. It must exclude world/player data, operating-system installation media, Java installations, rclone binaries, logs, and temporary staging files.

Artifacts are immutable. The proposed archive retention is five approved software releases (current plus four previous); candidates remain until accepted or rejected. Never delete an artifact referenced by an active deployment or rollback record.

## Version compatibility and rollback

Software rollback and bootstrap rollback are distinct operations. A replacement instance is created from a provider-supported standard operating-system offering and prepared with an explicitly selected bootstrap revision. Every production backup records the Minecraft version and relevant world-format compatibility. Before a major-version migration, preserve a verified pre-migration backup. Never assume an older Paper version can safely open a world already migrated by a newer version.

Promotion of a bootstrap revision or software release does not itself authorize a world migration. Major Minecraft upgrades and world migrations require explicit operator approval.

## Lifecycle safety invariants

1. Never destroy an instance with required unsaved data until backup upload and verification succeed.
2. Failed backup or verification blocks destructive cleanup.
3. Staging cannot write to production backup storage.
4. Credentials are references to protected secret stores/files, never literal config values.
5. Operations are auditable and have explicit states, timeouts, and recovery behavior.
6. A failed candidate cannot silently replace a healthy approved production release.
7. Destructive operations require validated target identity and safeguards against production/staging confusion.
8. Adapters cannot bypass core authorization, telemetry consent, or environment isolation.

## Telemetry and privacy

Optional telemetry is disabled by default. Identifiable player telemetry requires explicit server-owner opt-in and separate informed opt-in by each affected player. Consent must be centrally enforced across all adapters and integrations. See [SECURITY.md](../SECURITY.md).

## Open design work

Before implementation, define the language/runtime, durable state store, concurrency and recovery model, adapter contracts and versioning, configuration/secret references, audit schema and retention, release manifest, per-game health checks, and approval policy for destructive operations.
