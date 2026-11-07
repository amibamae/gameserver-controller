# Configuration and Secret References

**Status:** The v1 configuration validator is implemented in `config-check`. It checks files without loading runtime settings or resolving provider credentials.

## Validate a file

```sh
go run ./cmd/config-check validate configs/controller.example.yaml
```

The command accepts one YAML document, rejects unknown fields, checks references and safety rules, and prints a validation summary. It does not read secrets, open the configured state-store path, contact providers, or start operations.

The example uses fictional adapter profiles and a placeholder environment-variable reference. It contains no credentials. It defines a production boundary but includes only a staging test-server profile, so you do not have to adopt a live production server into controller-managed inventory.

## Version 1 shape

The validator requires `schema_version: 1`, local controller mode, a bbolt state-store path, bounded operation timeouts and retry defaults, and enabled local audit.

Adapter profiles use one of five protocol categories: `hosting`, `storage`, `execution`, `game`, or `integration`. Their IDs must use the matching category prefix. Each server points to an existing environment and hosting, storage, execution, and game profile. Integration is optional.

Environments are `production`, `staging`, or `testing`. Production declares `production_backup_access: read_write`; staging and testing may declare `none` or `read_only`, never `read_write`. The validator checks these policy declarations but does not enforce access at the storage service. A server must list verified backup and operator approval as destroy prerequisites. The validator checks an optional idle-shutdown duration but does not schedule it.

Relative state-store paths are resolved against the configuration file by `ResolveStateStorePath`; absolute local paths remain absolute. The validator does not create parent directories, canonicalize symlinks, or stop a separate runtime from opening a path.

## Secret references

Version 1 accepts only the explicit `env://NAME` reference form. Each reference records a purpose and may include permission scopes and a version reference. Validation checks syntax. It does not read the environment variable, reveal its value, or verify the credential's permissions. A literal credential is not a valid reference.

A future resolver must reject missing or unauthorized credentials, never print resolved values, resolve credentials only when needed, and fail closed. Keep credentials out of source control, bootstrap/user-data, command-line arguments, release artifacts, and ordinary logs. Production and staging need distinct least-privilege credentials; staging must not change or delete production backups.

## What validation covers

The validator checks document shape, duplicate and unknown YAML fields, IDs, adapter categories and references, secret-reference syntax, environment and backup-access rules, lifecycle safeguards, and timeout/retry bounds.

It does not yet check provider endpoints, network destinations, filesystem symlink or traversal policy, runtime authorization, config migrations, or redacted change plans. Passing validation does not authorize an operation.

The initial operation store uses bbolt; SQLite from the earlier proposal is superseded. Runtime config loading, environment-isolation enforcement, approval identity, and provider adapters remain future work. See the [roadmap](ROADMAP.md) for the current Phase 2 gate.