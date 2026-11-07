# Game Server Controller

Game Server Controller is a Minecraft server lifecycle controller in development. I want it to bring a backend online when players need it, restore the world, send players across only after the game is ready, and remove the VM only after a fresh backup has been verified.

I’m using a real setup to test the design: Debian on Vultr, Paper for Minecraft Java, Velocity as the proxy, and Wasabi for object storage. Those services give the first adapters something concrete to account for. The protocol is meant to support other providers and games too.

> **Current status:** The Phase 1 protocol package is reviewed for draft version `0.21.0-draft`. The repository includes a Go protocol validator, durable operation acceptance and storage, a local schema registry, an in-process adapter boundary, and a limited supervised backup pilot. It does not yet include the full lifecycle controller, management API runtime, durable workflow dispatch, or production adapters. Go is the first implementation language; contributors can build clients or adapters in other languages, but the project does not require a second implementation for release. See the [roadmap](docs/ROADMAP.md).

## Why I’m building it

I want routine server work to happen without me logging in to build a VM by hand: create the instance, prepare Debian with a reviewed bootstrap, restore the selected world, start Paper, then route players from the lobby when readiness checks pass. When the server goes idle, it should save and verify the backup before the controller destroys that exact VM.

The awkward cases deserve more attention than the happy path. What if Vultr times out after creating an instance? What proves a stored backup matches the world on disk? Who can approve a destructive action, and what does the controller do when the provider never returns a final answer? I want those rules clear before a controller can make a mistake that loses a world.

If you work on game-server automation, cloud adapters, storage tooling, or operator clients, please challenge the design. Point out unsafe assumptions, vague protocol behavior, and parts we can make simpler. These are drafts, so I expect corrections.

## What exists now

- Draft language-neutral contracts for lifecycle operations and adapters.
- JSON Schemas, examples, and behavioral conformance fixtures, checked by Go CI.
- A Go operation store and acceptance service, local schema registry, and in-process adapter registry with scripted fakes.
- A configuration validator for the initial local v1 shape. It checks input; it does not load runtime settings or resolve provider credentials.
- A supervised backup CLI that wraps the server's existing `minecraft-backup.service`. It does not install or replace the service script. Read the [pilot handoff guide](docs/SUPERVISED_PILOT.md).
- A roadmap that separates working code from planned lifecycle features.

The full controller, management API runtime, durable step dispatch, workflow reconciliation, and production adapters still need implementation. The backup pilot is narrow by design. It does not create VMs or prove that backup files are valid.

## How I’m splitting the work

The core should own lifecycle rules, permissions, approvals, audit records, and production/staging boundaries. Adapters report what they support and what happened; they do not make safety decisions for the core.

- **Hosting** creates and inspects provider instances.
- **Storage** uploads, restores, and verifies world backups and release artifacts.
- **Execution** runs services on an allocated host.
- **Game** installs Paper or another runtime, checks readiness, and defines safe save and migration behavior.
- **Integration** connects a proxy such as Velocity and routes players to a backend the core has confirmed ready.

The first reference path uses a regular VM and systemd. Containers, Kubernetes, and Agones can fit as optional adapters; none is a prerequisite.

The draft requires the controller to save an operation record before it makes a mutating provider call. If the provider's answer disappears, the controller must inspect the actual resource before it retries. An upload acknowledgment does not prove a backup. A running Java process does not prove Paper is ready. A failed or uncertain backup blocks destruction.

## The lifecycle I’m designing

1. A player joins the lobby, and the controller accepts a provisioning operation.
2. A hosting adapter creates an instance from a provider-supported OS image.
3. A reviewed, pinned bootstrap prepares the host. Credentials arrive separately through scoped secret references.
4. The game adapter installs the selected release and restores one identified backup.
5. Paper starts. The controller checks game and network readiness before Velocity routes players to it.
6. When the server is idle, the controller drains traffic, saves the world, uploads and verifies its backup, then destroys the exact VM.
7. For updates, an isolated test or staging deployment checks the candidate before promotion. Major Minecraft upgrades and world migrations need explicit approval.

That is the target workflow. The repository does not implement it end to end yet.

## Protocol and implementation

Draft version `0.21.0-draft` uses JSON, JSON Schema 2020-12, JSON-RPC 2.0, and OpenAPI 3.2.1. The contract defines the behavior; Go is the initial implementation language, not part of the wire protocol. Start with the [protocol overview](protocol/README.md) and [compatibility rules](protocol/COMPATIBILITY.md).

The first deployment uses provider-supported Debian images and reviewed, versioned bootstrap scripts. I’m not making a project-owned preinstalled VM image the default. That keeps host setup inspectable and world data separate from the disposable VM disk. See [ADR 0002](docs/decisions/0002-bootstrap-based-provisioning.md).

## Start here

- [Roadmap and current status](docs/ROADMAP.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Core design](docs/CORE_DESIGN.md)
- [Adapter contracts](docs/ADAPTER_CONTRACTS.md)
- [Configuration and secret references](docs/CONFIGURATION.md)
- [Security model](docs/SECURITY_MODEL.md)
- [Protocol overview](protocol/README.md)
- [Lifecycle state machine](protocol/lifecycle.md)
- [Durable operation model](protocol/operations.md)
- [Standard operation catalog](protocol/operation-catalog.md)
- [Conformance requirements](protocol/conformance/requirements.md)
- [Contribution guide](CONTRIBUTING.md)
- [Security reporting](SECURITY.md)

## Reference stack

The first target is Minecraft Java with Paper behind Velocity on a Debian VM. These upstream guides describe the systems the first adapters must handle: [Paper setup](https://docs.papermc.io/paper/getting-started/), [Velocity player forwarding](https://docs.papermc.io/velocity/player-information-forwarding/), [Vultr Cloud-Init](https://docs.vultr.com/products/compute/instances/cloud-compute/features/cloud-init), and the [Wasabi S3 API](https://docs.wasabi.com/apidocs/getting-started-with-wasabi-apis). The protocol still needs to work when you choose a different provider or game.

## Safety, privacy, and AI use

Player worlds and credentials are valuable data. The design requires verified backups before destructive cleanup, isolated staging storage, scoped credentials, explicit approval for risky operations, and reconciliation when a provider outcome is unclear.

Optional telemetry is disabled by default. If it identifies a player, the design requires separate informed consent from the server owner and each affected player. See the full [security and privacy policy](SECURITY.md).

AI tools may assist development, but the controller must never depend on an AI service to build, run, or operate. You do not need to use AI to contribute. See the [AI-use disclosure](docs/AI_DISCLOSURE.md).

## License

This project uses GNU AGPL-3.0-only. See [LICENSE](LICENSE).