# Game Server Lifecycle

An extensible, open-source platform for provisioning, operating, updating, staging, backing up, and rolling back disposable game servers across hosting and storage providers.

> **Status: design and foundation.** The repository currently contains project policies and architecture direction. The orchestration engine and adapters described below are planned, not yet implemented.

## Vision

The platform coordinates disposable game-server infrastructure without tying its core to one cloud provider, one game, or one storage service.

The first reference deployment is Vultr + Wasabi + Minecraft Java Edition (Paper) + Velocity on Debian. The architecture is intended to support additional hosting providers, storage backends, games, server implementations, and integrations through documented adapters.

## Design principles

- **Disposable infrastructure:** create game-server instances when needed and destroy them only after verified backups.
- **Provider independence:** provider-specific behavior belongs in hosting adapters.
- **Game independence:** game-specific lifecycle, health checks, versioning, and migration rules belong in game adapters.
- **Storage independence:** backup and artifact operations use storage interfaces and declared capabilities.
- **Fail safely:** never destroy a server containing data that has not been safely backed up and verified.
- **Production/staging isolation:** staging cannot overwrite production world data.
- **Least privilege:** credentials and permissions are separated by role and environment.
- **Reproducible releases:** manifests, checksums, compatibility information, and validation results accompany releases.
- **Privacy by default:** optional telemetry is disabled unless explicitly enabled; identifiable player telemetry requires separate server-owner and player consent.
- **Community extensibility:** contributors can add adapters without rewriting core functionality.

## AI-assisted development

This project currently uses AI-assisted tools during its early architecture and proof-of-concept work. AI helps with design exploration, documentation drafts, comparing approaches, prototyping, and troubleshooting. AI-assisted output is not automatically trusted or accepted; maintainers remain responsible for review, testing, security, licensing, and project decisions.

AI is not intended to be a runtime dependency. The controller's normal operation should not require an AI model, AI API, subscription, or external AI service. We are open to reworking, simplifying, or replacing AI-assisted designs and code with conventional, independently maintained implementations as the project matures. Contributors are not required to use AI, and non-AI contributions are equally welcome.

See [AI Use and Development Transparency](docs/AI_DISCLOSURE.md) for the full disclosure and review commitments.

## Planned capabilities

- Hosting-provider, storage-provider, game, and integration adapters.
- Lifecycle orchestration, idle shutdown, provisioning, and destruction.
- Verified backup and restore workflows.
- Staging deployments based on copies of selected production backups.
- Software release management and rollback.
- Infrastructure image building, validation, promotion, and rollback.
- Proxy/player-routing integrations.
- Auditable operations, policy controls, and optional consent-based telemetry.

These are project goals, not a claim that the features already exist.

## Documentation

- [Architecture](docs/ARCHITECTURE.md)
- [Development roadmap](docs/ROADMAP.md)
- [Contributing](CONTRIBUTING.md)
- [Security and privacy](SECURITY.md)
- [Code of conduct](CODE_OF_CONDUCT.md)
- [License](LICENSE) — GNU AGPL-3.0-only

## Safety model

1. Backups must complete and be verified before an instance containing unsaved data can be destroyed.
2. Staging receives a copy of a production backup and has no permission to write to production backup locations.
3. Credentials are never stored in source control, baked into images, or printed in logs.
4. Update promotion is gated by validation; process startup alone is insufficient.
5. Major game-version upgrades and world migrations require explicit approval and a recoverable pre-migration backup.
6. Rollback must respect world compatibility; old software must not be launched against an incompatible migrated world.

## Current status

This repository is being established incrementally. Early work focuses on architecture, configuration schemas, security boundaries, adapter contracts, and tests before production lifecycle automation is introduced.

No production credentials, live world data, private configuration, or deployment secrets belong in this repository.

## License

GNU Affero General Public License, version 3.0 only. See [LICENSE](LICENSE).
