# Game Adapter Contract

**Status:** Draft, version 0.4.0-draft.

## 1. Responsibility

A game adapter defines game-specific installation, configuration, runtime requirements, health/readiness, persistent data, backup consistency, version compatibility, and migration semantics. It does not own provider APIs, general operation persistence, authorization, or storage credentials.

## 2. Capability names

| Capability | Meaning |
|---|---|
| `game.release.describe` | Describe a game release and requirements |
| `game.install` | Install a pinned release |
| `game.configure` | Apply validated configuration |
| `game.validate_installation` | Verify installation and artifacts |
| `game.readiness.check` | Perform game-specific readiness checks |
| `game.health.check` | Check ongoing game health |
| `game.save.request` | Request game-native save/flush |
| `game.data.describe` | Describe persistent paths/data sets |
| `game.backup.prepare` | Quiesce or prepare data for consistent capture |
| `game.backup.validate` | Validate backup content/manifest |
| `game.compatibility.check` | Check source/target version compatibility |
| `game.migrate` | Perform an explicitly supported migration |

## 3. Release description

A release description MUST identify game adapter ID, game name, opaque game version, platform/architecture constraints, artifact references and checksums, runtime requirements, configuration schema, data-format metadata, and required execution capabilities.

Game versions are opaque to the core unless the game adapter declares comparison and compatibility rules. The core MUST NOT assume semantic versioning for game releases or worlds.

## 4. Installation and configuration

Install/configure operations MUST be repeatable or must explicitly declare non-idempotent behavior. They MUST use immutable release/artifact references and verify checksums before execution. Configuration validation MUST be side-effect free. Secret values MUST be supplied only through authorized secret references.

## 5. Readiness and health

The game adapter defines mandatory readiness checks and optional health checks. A process running or a port listening is insufficient when game-specific readiness can be checked.

A readiness result MUST identify:
- Check ID and check version.
- Pass/fail/unknown result.
- Observation timestamp.
- Safe evidence and structured reason.
- Game version and release reference inspected.

Unknown readiness MUST NOT be treated as pass. Failed required checks prevent transition to `ready`.

## 6. Persistent data and backup consistency

The game adapter declares persistent data sets and their paths, exclusions, consistency mode, save/flush sequence, and validation method.

Supported consistency modes MUST be explicit, for example:
- `offline`: game is stopped before capture.
- `game_native`: game provides a documented consistent snapshot/save mechanism while running.
- A future named mode with a documented consistency guarantee.

The controller coordinates save/quiesce, storage transfer, verification, manifest creation, and retention policy. The game adapter validates game-specific structure and version metadata. Copying files alone is not proof of a valid backup.

## 7. Compatibility and migration

The adapter defines whether a backup can be opened by a target release, whether migration is needed, and whether migration is reversible. Major-version migration MUST require an explicit approval gate and a verified pre-migration backup. The core MUST NOT infer compatibility from version-string ordering.

## 8. Conformance cases

Test artifact checksum mismatch, installation repeat, invalid configuration, game process running but not ready, unknown readiness, save failure, backup validation failure, incompatible world, and migration without approval.
