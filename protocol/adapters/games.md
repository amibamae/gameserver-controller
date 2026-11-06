# Game Adapter Contract

**Status:** Draft, version 0.7.0-draft.

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

A release description MUST identify game adapter ID, game name, opaque game version, platform/architecture constraints, immutable artifact references and checksums, runtime requirements, configuration schema URI and checksum, data-format metadata, and required execution capabilities.

Game versions are opaque to the core unless the game adapter declares comparison and compatibility rules. The core MUST NOT assume semantic versioning for game releases or worlds.

## 4. Installation and configuration

Installation MUST verify every declared artifact checksum before execution; a mismatch MUST prevent installation from starting. Repeating install for the same exact release MUST report an already-installed state or converge idempotently.

Configuration validation MUST use the schema URI and checksum pinned by the release and MUST finish before application begins. The adapter MUST verify these values against the release and MUST NOT dereference an arbitrary schema URI supplied by the caller. Configuration objects MUST NOT contain secret values; secret bindings MUST use authorized secret references.

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

Test artifact checksum mismatch before installation starts, repeated install of the same release, invalid configuration before application, unknown installed-artifact validation blocking start, game process running but not ready, unknown readiness, save failure, backup validation failure, incompatible world, and migration without approval.
