# Game Server Controller

Game Server Controller (GSC) is an open-source, self-hosted orchestration project designed to run game servers on demand across cloud and local infrastructure. The goal is to reduce hosting costs, unnecessary electricity consumption and administrative work while protecting valuable player worlds.

The first priority is safe, routine operation of the owner's production Minecraft network. The initial reference stack is Debian, Vultr, Paper, Velocity and Wasabi, with a lightweight Go controller and embedded bbolt state store.

## Intended deployment options

| Deployment | Goal |
|---|---|
| Cloud-only | Provision disposable Vultr VMs when needed; no local server hardware or Proxmox required. |
| Self-hosted | Start and stop game workloads on Proxmox VE; optionally power down dedicated idle hosts when a safe independent wake path exists. |
| Hybrid | Use local and cloud capacity together, subject to resource limits, cost policies and verified recovery. |

Vultr remains a first-class hosting option. Proxmox and hybrid orchestration are planned, not currently supported end to end. The owner intends to migrate compute locally only if capacity, networking and recovery tests justify it. Wasabi remains the reference off-site object store for backups and versioned artifacts, independent of the compute provider.

## Intended workloads

- **Persistent worlds:** Bring survival or other durable game worlds online when needed; save, verify and restore authoritative data safely.
- **On-demand minigames:** Start sessions from reviewed, versioned map and release templates, route waiting players after readiness, and return them to the lobby when matches finish. Dispose of arena state only under an approved policy after required persistent results are saved and verified.
- **Energy- and cost-aware operation:** Reclaim idle cloud resources and stop unused local guests. Whole-host shutdown is a separate opt-in feature for dedicated hardware, not a requirement for running GSC.

The design aims to preserve Java and Bedrock connectivity and configured voice chat through the existing Minecraft network. These are deployment goals, not claims of complete controller-managed support.

> **Development status:** Not yet a production-ready end-to-end controller. The Phase 1 protocol is reviewed at draft version `0.21.0-draft`. The runtime currently executes `server.define`; other standard lifecycle definitions exist and have fake-adapter tests, but are not fully connected to real production workflows. A supervised Vultr pilot and initial real storage/integration components exist. Proxmox, on-demand minigame orchestration and the complete reference lifecycle remain planned or incomplete. See the [roadmap](docs/ROADMAP.md) for priorities and verification gates before using the controller with irreplaceable worlds.

## Why I’m building it

I want routine server work to happen without me logging in to build a VM by hand: create the instance, prepare Debian with a reviewed bootstrap, restore the selected world, start Paper, then route players from the lobby when readiness checks pass. When the server goes idle, it should save and verify the backup before the controller destroys that exact VM.

The awkward cases deserve more attention than the happy path. What if Vultr times out after creating an instance? What proves a stored backup matches the world on disk? Who can approve a destructive action, and what does the controller do when the provider never returns a final answer? I want those rules clear before a controller can make a mistake that loses a world.

If you work on game-server automation, cloud adapters, storage tooling, or operator clients, please challenge the design. Point out unsafe assumptions, vague protocol behavior, and parts we can make simpler. These are drafts, so I expect corrections.

## What exists now

| Component | Implementation status |
|---|---|
| Draft protocol and Go conformance checker | Implemented validation, schemas, examples and behavioral fixtures; stable protocol release pending. |
| Durable operation store and workflow engine | Implemented, with actor-scoped idempotency, resource locks and reconciliation/restart recovery primitives. Continuous scheduling and real lifecycle recovery still need work. |
| Logical-server catalog and controller runtime | `server.define` is registered and executable; it creates metadata without external infrastructure effects. |
| Other standard lifecycle workflows | Provision, start, stop, backup, restore and destroy definitions exist and are fake-tested; normal runtime integration is incomplete. |
| Management API | Authenticated operation inspection, capability and logical-server routes, and operation submission exist. Runnable operations remain limited; cancellation/events are not implemented. |
| Vultr supervised pilot | Guarded, explicitly confirmed provisioning under the two-slot capacity policy (one production, one shared staging/testing); an unmanaged live production server is excluded. Backup-service inspection and supervised backup tools exist. No pilot destroy command or complete production hosting adapter. |
| Wasabi, Velocity and Cloudflare | Initial adapters and configuration/runtime registration exist; end-to-end real lifecycle integration and live verification remain incomplete. |
| Configuration and credentials | Validator, bounded credential accessor and runtime configuration loading exist; installation and provider validation are still being completed. |
| Optional observability | VictoriaLogs and Homelable tools exist and are disabled by default; no mandatory observability service or production deployment is implied. |
| Proxmox, minigames and hybrid placement | Planned; require protocol review, implementation and independent verification. |

The pilot is not a complete production deployment tool. It cannot establish the full install, restore, readiness, routing, verified-backup and cleanup sequence by itself. See the [pilot handoff guide](docs/SUPERVISED_PILOT.md) and [roadmap](docs/ROADMAP.md). Production destruction remains disabled pending separate authorization.

## How I’m splitting the work

The core should own lifecycle rules, permissions, approvals, audit records, and production/staging boundaries. Adapters report what they support and what happened; they do not make safety decisions for the core.

- **Hosting** creates, inspects, starts, stops, reboots, and destroys provider instances.
- **Storage** uploads, restores, and verifies world backups and release artifacts.
- **Execution** runs services on an allocated host.
- **Game** installs Paper or another runtime, checks readiness, and defines safe save and migration behavior.
- **Integration** connects a proxy such as Velocity and routes players to a backend the core has confirmed ready.

The first reference path uses a regular VM and systemd. Proxmox, container and Kubernetes adapters are optional. The controller must work without a hosted management panel, dedicated remote file manager or SFTP service; pinned SSH remains useful for secure host handoff and execution.

The draft requires the controller to save an operation record before it makes a mutating provider call. If the provider's answer disappears, the controller must inspect the actual resource before it retries. An upload acknowledgment does not prove a backup. A running Java process does not prove Paper is ready. A failed or uncertain backup blocks destruction.

## The lifecycle I’m designing

1. A player joins the lobby, and the controller accepts a provisioning operation.
2. A hosting adapter creates an instance from a provider-supported OS image.
3. A reviewed, pinned bootstrap prepares the host. Credentials arrive separately through scoped secret references.
4. The game adapter installs the selected release and restores one identified backup.
5. Paper starts. The controller checks game and network readiness before Velocity routes players to it.
6. When the server is idle, the controller drains traffic, saves the world, uploads and verifies its backup, then may destroy the exact authorized disposable VM. A local guest may instead be stopped and retained.
7. For updates, an isolated test or staging deployment checks the candidate before promotion. Major Minecraft upgrades and world migrations need explicit approval.

That is the target persistent-world workflow. The repository does not implement it end to end yet. Minigame cleanup policies and local host power management are separate planned extensions that must preserve the same applicable safety guarantees.

## Protocol and implementation

Draft version `0.21.0-draft` uses JSON, JSON Schema 2020-12, JSON-RPC 2.0, and OpenAPI 3.2.1. The contract defines the behavior; Go is the initial implementation language, not part of the wire protocol. Start with the [protocol overview](protocol/README.md) and [compatibility rules](protocol/COMPATIBILITY.md).

The first deployment uses provider-supported Debian images and reviewed, versioned bootstrap scripts. I’m not making a project-owned preinstalled VM image the default. That keeps host setup inspectable and world data separate from the disposable VM disk. See [ADR 0002](docs/decisions/0002-bootstrap-based-provisioning.md).

## Start here

- [Project overview (newcomer guide)](docs/PROJECT_OVERVIEW.md)
- [Roadmap and current status](docs/ROADMAP.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Core design](docs/CORE_DESIGN.md)
- [Adapter contracts](docs/ADAPTER_CONTRACTS.md)
- [Operator quick reference (start / stop / save / drain)](docs/OPERATOR_OPERATIONS.md)
- [Configuration and secret references](docs/CONFIGURATION.md)
- [Durable workflow engine](docs/WORKFLOW_ENGINE.md)
- [Secret management](docs/SECRET_MANAGEMENT.md)
- [Optional VictoriaLogs and Homelable observability](docs/OBSERVABILITY.md)
- [Optional no-proxy DNS handoff](docs/NO_PROXY_DNS_HANDOFF.md)
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

AI coding agents (OpenAI Codex and Anthropic Claude Code) write most of this repository under the owner's direction: 165 of 166 merged pull requests so far came from AI agent branches. The controller itself must never depend on an AI service to build, run, or operate. You do not need to use AI to contribute. See the [AI-use disclosure](docs/AI_DISCLOSURE.md).

## License

This project uses GNU AGPL-3.0-only. See [LICENSE](LICENSE). Third-party components retain their own licenses; see [third-party notices](THIRD_PARTY_NOTICES.md).

The [copyright notice](COPYRIGHT.md) identifies amibamae for material covered by applicable copyright law. To cite the project, use [CITATION.cff](CITATION.cff).
