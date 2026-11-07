# Game Server Controller

Game Server Controller is a protocol-first design for automating disposable Minecraft and other game servers. The goal is to start a backend when players need it, restore its world from object storage, route players only after the server is ready, then verify a backup before removing the idle VM.

I’m shaping the project around a real reference setup: Debian on Vultr, Minecraft Java with Paper, a Velocity proxy, and Wasabi object storage. Those tools are the first test of the design, not a limit on which providers or games the protocol can support.

> **Current status: Phase 1 protocol package complete for the current draft.** This repository contains reviewed draft specifications, JSON Schemas, examples, and conformance fixtures. It does not yet contain a full controller, management API implementation, or production adapters. It includes a Go protocol validator and a limited supervised backup pilot; neither is a general-purpose controller for a live server. The untagged development branch uses protocol version 0.21.0-draft. Go is the initial implementation language, and contributions in other languages are welcome but optional. See the [roadmap](docs/ROADMAP.md).

## Why I’m building it

I want the server to handle its own routine lifecycle: create a VM, prepare Debian with a reviewed bootstrap, restore the selected world, start Paper, and send players over from the lobby once readiness checks pass. When the server goes idle, it should save and verify the world backup before it destroys the VM. Compute can be temporary. The world cannot.

That workflow raises the hard questions this project is trying to make explicit: what happens when a provider times out after creating a VM, how do you know a backup matches the data on disk, and who can approve a destructive action? I’d rather settle those contracts before building an engine that can lose a world.

If you build game-server automation, cloud adapters, storage tooling, or operator clients, I want you to challenge the design. Point out unsafe assumptions, unclear protocol behavior, and work that should be simpler. The specs are drafts and should be treated that way.

## What exists now

- Draft language-neutral contracts for lifecycle operations and adapters.
- JSON Schemas, examples, and a growing behavioral conformance suite validated in Go CI.
- A validation workflow for the schema package and fixture runner.
- A limited supervised backup pilot that wraps the existing `minecraft-backup.service` without installing or replacing its script; see the [pilot handoff guide](docs/SUPERVISED_PILOT.md).
- An initial design for hosting, storage, execution, game, and proxy integrations.
- A roadmap that separates documented goals from implemented software.

The full controller engine, management API, and production adapters are planned. Go is the initial implementation language. The protocol remains documented in a portable form, and other-language contributions are welcome without being required for release. This draft does not authorize use against production infrastructure.

## Design in brief

The controller core should own lifecycle policy, authorization, approvals, audit records, and production/staging boundaries. Adapters should report what they can do and what happened. They must not make safety decisions for the core.

The protocol keeps these concerns separate:

- **Hosting:** create and inspect provider instances.
- **Storage:** upload, restore, and verify world backups and release artifacts.
- **Execution:** run a process or service on an allocated host.
- **Game:** install Paper or another game runtime, check readiness, and define safe save and migration behavior.
- **Integration:** connect a proxy such as Velocity and route players to a confirmed-ready backend.

A normal VM and systemd are the first reference path. Containers, Kubernetes, and Agones may fit through optional adapters, but none is required.

The draft requires a durable operation record before a mutating external call. If a provider response disappears, the controller must inspect the real resource before it retries. An accepted upload is not a verified backup, and a running process is not proof that a game is ready. A failed or uncertain backup blocks destruction.

## The lifecycle I’m designing

1. A player joins the lobby, and the controller accepts a provisioning operation.
2. The hosting adapter creates an instance from a provider-supported OS image.
3. A pinned, reviewed bootstrap prepares the host. Credentials arrive separately through scoped secret references.
4. The game adapter installs the selected release and restores one explicitly identified backup.
5. Paper starts. The controller checks game and network readiness before Velocity routes players to it.
6. When the server is idle, the controller drains traffic, saves the world, uploads and verifies the backup, then destroys the exact VM.
7. For updates, an isolated test or staging deployment checks the candidate before promotion. Major Minecraft upgrades and world migrations require explicit approval.

These are design goals. The repository does not yet implement this lifecycle.

## Protocol and implementation

The current draft uses JSON, JSON Schema 2020-12, JSON-RPC 2.0, and OpenAPI 3.2.1. It keeps protocol meaning independent of a programming language and allows clients to work without a hosted web panel. See [protocol/README.md](protocol/README.md) and the [compatibility rules](protocol/COMPATIBILITY.md).

The first deployment uses ordinary provider-supported Debian offerings and versioned bootstrap scripts. I’m not building a project-owned preinstalled VM image as the default. That keeps the host setup reviewable and leaves world data in separately managed storage. The [architecture decision](docs/decisions/0002-bootstrap-based-provisioning.md) explains the trade-offs.

## Start with these documents

- [Roadmap and current status](docs/ROADMAP.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Lifecycle and core design](docs/CORE_DESIGN.md)
- [Adapter contracts](docs/ADAPTER_CONTRACTS.md)
- [Configuration and secret references](docs/CONFIGURATION.md)
- [Security model](docs/SECURITY_MODEL.md)
- [Protocol overview](protocol/README.md)
- [Lifecycle state machine](protocol/lifecycle.md)
- [Durable operation model](protocol/operations.md)
- [Standard operation catalog](protocol/operation-catalog.md)
- [Conformance requirements](protocol/conformance/requirements.md)
- [Contributing](CONTRIBUTING.md) and [security reporting](SECURITY.md)

## Review the reference stack

The initial target is Minecraft Java with Paper behind Velocity, on a Debian VM. These vendor docs explain the components the first adapters will have to account for: [Paper setup](https://docs.papermc.io/paper/getting-started/), [Velocity player forwarding](https://docs.papermc.io/velocity/player-information-forwarding/), [Vultr Cloud-Init](https://docs.vultr.com/products/compute/instances/cloud-compute/features/cloud-init), and the [Wasabi S3 API](https://docs.wasabi.com/apidocs/getting-started-with-wasabi-apis). The protocol must still work when an operator chooses a different provider or game.

## Safety and privacy

The project treats player worlds and credentials as valuable data. The design requires verified backups before destructive cleanup, isolated staging storage, scoped credentials, explicit approval for risky operations, and reconciliation when an external outcome is unknown.

Optional telemetry is disabled by default. Optional telemetry that identifies a player requires separate informed consent from the server owner and each affected player. Read the full [security and privacy policy](SECURITY.md).

AI tools may assist development, but the controller must never depend on an AI model or service to build, run, or operate. Contributors do not need to use AI. See the [AI-use disclosure](docs/AI_DISCLOSURE.md).

## License

This project is licensed under GNU AGPL-3.0-only. See [LICENSE](LICENSE).