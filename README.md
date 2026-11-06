# Game Server Controller

A language-independent, extensible project for safely provisioning, operating, updating, staging, backing up, and rolling back disposable game servers across hosting and storage providers.

> **Project status: protocol design.** The project is currently defining its language-independent protocol and architecture. The controller engine, management API, CLI, and production adapters are planned; they are not yet implemented. See the [development roadmap](docs/ROADMAP.md).

## Vision

Game Server Controller aims to coordinate disposable game-server infrastructure without tying its core to one programming language, hosting provider, game, storage service, or user interface.

The initial reference deployment is **Vultr + Wasabi + Minecraft Java Edition (Paper) + Velocity on Debian**. This is the first target for validating the design, not a limitation on the intended platform.

## Core design principles

- **Protocol first:** Define language-independent contracts before selecting the implementation language or building the controller.
- **Language independence:** The protocol is authoritative. Independent implementations and adapters should be able to interoperate across programming languages.
- **Provider independence:** Provider-specific behavior belongs in hosting adapters.
- **Game independence:** Game-specific installation, lifecycle, health checks, versioning, save semantics, and migration rules belong in game adapters.
- **Storage independence:** Backup and artifact operations use documented storage contracts and explicitly declared capabilities.
- **Fail safely:** Never destroy an instance containing data that has not been safely backed up and verified.
- **Core authority:** The controller core owns lifecycle policy, authorization, environment isolation, approvals, audit, and safety checks. Clients and adapters cannot bypass them.
- **Capability discovery:** Adapters declare what they support. Unsupported operations fail explicitly rather than being silently approximated.
- **Production/staging isolation:** Staging cannot overwrite or delete production world data or production backups.
- **Least privilege:** Credentials and permissions are separated by role, adapter, and environment.
- **Reproducible releases:** Manifests, checksums, compatibility information, and validation results accompany releases.
- **Privacy by default:** Optional telemetry is disabled by default. Identifiable player telemetry requires separate informed consent from the server owner and each affected player.
- **Community extensibility:** Contributors can implement adapters and clients without rewriting the controller core.

## Protocol-first architecture

The protocol specifications—not a particular programming language implementation—are intended to be the authoritative source for interoperability.

The design separates two communication surfaces:

### Adapter protocol

The adapter protocol connects the controller core to hosting, storage, game-runtime, and integration adapters. Adapter implementations should be replaceable and may be written in different programming languages.

Adapters report capabilities, results, and observed state. They do not make core authorization or safety decisions.

### Management API and independent clients

The project intends to define a documented, versioned management API for operators and independently developed tools. This can support future web panels, desktop applications, or mobile clients without requiring changes to the controller core.

**A web panel is not part of the initial project requirement.** The controller must be usable without a browser interface, and the project does not require its maintainers to host a web panel. A local CLI is planned as the initial operator interface.

The management API must keep authentication, authorization, approvals, audit, and lifecycle safeguards in the controller. It must not expose unrestricted provider credentials or allow clients to bypass core policy.

## Current protocol status

The protocol is still a draft. The initial specifications cover protocol principles and common types, plus a first draft of the durable operation model.

The [durable operation protocol](protocol/operations.md) describes how long-running work is accepted, tracked, retried, cancelled, and recovered. It emphasizes durable operation records, idempotency, progress, partial outcomes, and reconciliation when an external provider's result is uncertain.

The following decisions have **not** been finalized:

- Implementation programming language.
- Serialization format and schema standard.
- Adapter communication transport.
- Management API transport and endpoint definitions.
- Protocol version negotiation and compatibility details.

Candidate standards are being evaluated rather than treated as settled decisions. The next protocol work is to review and refine the common types and operation semantics, then define lifecycle states and transitions, adapter contracts, and conformance requirements.

## Planned capabilities

The longer-term project aims to support:

- Hosting-provider, storage-provider, game-runtime, and integration adapters.
- Lifecycle orchestration, idle shutdown, provisioning, and destruction.
- Verified backup and restore workflows.
- Staging deployments based on copies of selected production backups.
- Software release management and rollback.
- Infrastructure image building, validation, promotion, and rollback.
- Proxy and player-routing integrations.
- Durable, auditable operations and recovery.
- Policy controls, least-privilege permissions, and optional consent-based telemetry.
- Independent implementations and third-party management clients.

These are project goals, not a claim that these features already exist.

## AI and development transparency

AI may assist with design exploration, documentation drafts, comparisons, prototyping, and troubleshooting. AI-assisted output is not automatically trusted or accepted; maintainers remain responsible for review, testing, security, licensing, and project decisions.

**AI will never be required to develop, build, install, configure, operate, maintain, or use this project.** The controller's functionality must not depend on an AI model, AI API, subscription, or external AI service. Contributors are not required to use AI, and contributions made without AI assistance are equally welcome.

See [AI Use and Development Transparency](docs/AI_DISCLOSURE.md) for the full disclosure and commitments.

## Documentation

### Project and architecture

- [Development roadmap](docs/ROADMAP.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Core design and lifecycle model](docs/CORE_DESIGN.md)
- [Adapter contracts](docs/ADAPTER_CONTRACTS.md)
- [Configuration and secret reference model](docs/CONFIGURATION.md)
- [Security model](docs/SECURITY_MODEL.md)

### Protocol specifications

- [Protocol overview and design principles](protocol/README.md)
- [Common protocol types](protocol/common-types.md)
- [Durable operation protocol](protocol/operations.md)

### Project policies

- [Contributing](CONTRIBUTING.md)
- [Security and privacy reporting](SECURITY.md)
- [Code of conduct](CODE_OF_CONDUCT.md)
- [AI-use disclosure](docs/AI_DISCLOSURE.md)
- [License](LICENSE) — GNU AGPL-3.0-only

## Safety model

The design is governed by these operational safeguards:

1. A server containing unsaved data must not be destroyed until a backup is complete and verified.
2. Staging receives a copy of a selected production backup and has no permission to write to or delete production backup locations.
3. Credentials must never be stored in source control, baked into images, or printed in logs.
4. Update promotion is gated by validation; process startup alone is insufficient.
5. Major game-version upgrades and world migrations require explicit approval and a recoverable pre-migration backup.
6. Rollback must respect world compatibility; old software must not be launched against an incompatible migrated world.
7. Timeouts and interrupted external operations must be reconciled before retrying or proceeding with dependent destructive actions.
8. Clients and adapters must not bypass core authorization, approvals, environment boundaries, or audit requirements.

## Development approach

Development is proceeding in phases to reduce risk:

1. Define and review the language-independent protocol.
2. Build language-neutral examples, schemas, conformance fixtures, and tests.
3. Select an implementation language and supporting technologies based on the completed protocol.
4. Build a local controller using fake adapters before connecting to real infrastructure.
5. Validate real adapters against isolated test resources before considering production use.

Existing infrastructure remains operational while this project is developed and tested separately. Existing production scripts should not be replaced until equivalent functionality is implemented, tested, and deliberately migrated.

No production credentials, live world data, private configuration, or deployment secrets belong in this repository.

## License

GNU Affero General Public License, version 3.0 only. See [LICENSE](LICENSE).
