# Game Adapter Contract

**Status:** Draft, version 0.21.0-draft.

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
| `game.compatibility.check` | Check one backup manifest against one exact target release |
| `game.migrate` | Perform an explicitly supported migration |

An adapter advertises `game.migrate` only when it satisfies the requirements in §7.

## 3. Release description

A release description MUST identify game adapter ID, game name, opaque game version, platform/architecture constraints, immutable artifact references and checksums, runtime requirements, configuration schema URI and checksum, data-format metadata, and required execution capabilities.

Game versions are opaque to the core unless the game adapter declares comparison and compatibility rules. The core MUST NOT assume semantic versioning for game releases or worlds.

## 4. Installation and configuration

Installation MUST verify every declared artifact checksum before execution; a mismatch MUST prevent installation from starting. Repeating install for the same exact release MUST report an already-installed state or converge idempotently.

Successful install results MUST identify exactly the pinned release artifact IDs and confirm each checksum. Configuration validation MUST use the schema URI and checksum pinned by the release and MUST finish before application begins. The adapter MUST verify these values against the release and MUST NOT dereference an arbitrary schema URI supplied by the caller. Configuration objects MUST NOT contain secret values; secret bindings MUST use authorized secret references.

## 5. Readiness and health

The game adapter defines mandatory readiness checks and optional health checks. A process running or a port listening is insufficient when game-specific readiness can be checked.

`game.validate_installation` reports `valid` only when its output binds the exact target and release and contains exactly one `verified` result with matching expected/observed checksums for every pinned artifact; missing, duplicate, unavailable, mismatched, or unknown results cannot permit start.

A readiness result MUST identify:
- Check ID and check version.
- Pass/fail/unknown result.
- Observation timestamp.
- Safe evidence and structured reason.
- Game version and release reference inspected.

Unknown readiness MUST NOT be treated as pass. Failed required checks prevent transition to `ready`. The `game.health.check` capability uses the same exact-target binding, returns one result per requested check, and reports `unknown` when evidence is incomplete; its diagnostics MUST be redacted. See [health check input](../schemas/game/health-check-input.schema.json) and [health check output](../schemas/game/health-check-output.schema.json).

## 6. Persistent data and backup consistency

The `game.data.describe` capability declares release-bound persistent data sets, normalized relative paths, required status, and supported consistency modes. It MUST NOT return absolute host paths, path traversal, or secret values. `game.save.request` returns `confirmed` only after a game-native save/flush has completed; an `outcome_unknown` result MUST carry a reconciliation reference and MUST NOT be retried blindly. `game.backup.prepare` MUST echo the exact target, release, data generation, selected data-set IDs, and consistency mode. It reports `prepared` only with a preparation ID and evidence for the declared consistency guarantee. Schemas: [data describe input](../schemas/game/data-describe-input.schema.json), [data describe output](../schemas/game/data-describe-output.schema.json), [save input](../schemas/game/save-request-input.schema.json), [save output](../schemas/game/save-request-output.schema.json), [backup prepare input](../schemas/game/backup-prepare-input.schema.json), and [backup prepare output](../schemas/game/backup-prepare-output.schema.json).

Supported consistency modes MUST be explicit, for example:
- `offline`: game is stopped before capture.
- `game_native`: game provides a documented consistent snapshot/save mechanism while running.
- A future named mode with a documented consistency guarantee.

The controller coordinates save/quiesce, storage transfer, verification, manifest creation, and retention policy. The game adapter validates game-specific structure and version metadata. Copying files alone is not proof of a valid backup. The `game.backup.validate` capability receives the manifest and one bounded, operation-scoped stream for each exact object reference. It reports `valid` only when all requested game-specific validation checks pass; `unknown` is not valid. See [backup validation input](../schemas/game/backup-validate-input.schema.json) and [backup validation output](../schemas/game/backup-validate-output.schema.json).

## 7. Compatibility and migration

`game.compatibility.check` is read-only. It compares one exact backup manifest with one immutable target release description. Its output MUST bind the backup, data generation, source game version, and target release. Every requested check MUST have exactly one result. The adapter MUST NOT infer compatibility from version-string ordering.

Aggregate status is `incompatible` when any required check is incompatible; otherwise `unknown` when evidence is unknown or incomplete; otherwise `migration_required` when any check requires migration; otherwise `compatible`. Unknown or incomplete results do not establish compatibility.

A compatibility result does not verify backup integrity or authorize restore. The core MUST separately require a verified backup and authorization. A `migration_required` result does not permit restore; a separate migration workflow and a verified pre-migration backup are required. Migration is a separate mutating capability. The controller MUST require a successful durable backup result whose objects and data generation are verified, a compatibility result of `migration_required` bound to that backup and exact release, and an approval bound to the backup, target instance, release, and required migration checks before dispatch. The adapter receives bounded streams for the exact manifest objects. The target MUST be a distinct, stopped instance; the adapter MUST preserve the source instance and backup and write the migrated data as a new generation. A `migrated` result requires one passing result for every requested check. An `outcome_unknown` result requires reconciliation before retry; the controller MUST retain partial target state and MUST NOT perform automatic rollback. Any recovery or cleanup is a separately authorized operation.

## 8. Conformance cases

Test artifact checksum mismatch before installation starts, completed install with the exact artifact set, incomplete install results requiring reconciliation, repeated install of the same release, complete and incomplete installed-artifact validation, contradictory `valid` claims, invalid configuration before application, unknown installed-artifact validation blocking start, game process running but not ready, unknown readiness, save failure, backup validation failure, exact-target compatibility, migration required, incompatible data, unknown or incomplete compatibility, a false compatible claim, a mismatched target release, migration approval and backup gates, source preservation, exact migration target binding, complete migration checks, and unknown-result reconciliation.
