# Configuration and Secret References

**Status:** Version 1 is parsed and validated by `config-check`. This is a
side-effect-free validation tool, not yet a runtime configuration loader or
provider credential resolver.

## Validate a file

```sh
go run ./cmd/config-check validate configs/controller.example.yaml
```

The command accepts one YAML document, rejects unknown fields, checks
references and safety rules, and prints only a validation summary. It does not
resolve secrets, open the configured state-store path, contact providers, or
start operations. The example uses fictional adapter profiles and a placeholder
environment-variable reference; it contains no credentials. It declares the
production boundary but includes only a staging test-server profile, so a live
production server does not have to be adopted into controller-managed inventory.

## Version 1 shape

The validator requires `schema_version: 1`, local controller mode, a bbolt
state-store path, bounded operation timeout and retry defaults, and enabled
local audit. Adapter profiles declare one of the five protocol categories:
`hosting`, `storage`, `execution`, `game`, or `integration`. Their IDs use the
matching category prefix. Each server references an existing environment and
hosting, storage, execution, and game profiles; integration is optional.

Environments are classified as `production`, `staging`, or `testing`.
Production declares `production_backup_access: read_write`; staging and testing
may use `none` or `read_only`, never `read_write`. These are validated policy
declarations. They do not enforce access against a storage service. A server
must declare verified backup and operator approval as prerequisites for
destroy. An optional idle-shutdown duration is validated but not scheduled by
this tool.

Relative state-store paths are interpreted relative to the configuration file
by `ResolveStateStorePath`; absolute local paths are retained. The validator
does not create the parent directory, canonicalize symlinks, or prevent a
separate runtime from opening a path.

## Secret references

Version 1 accepts secret references only in the explicit `env://NAME` form.
Each reference also records a purpose and may include permission scopes and a
version reference. Validation checks the reference's syntax; it does not read
the environment variable, reveal its value, or verify the permissions of a
credential. Literal credentials are not valid references.

Any future resolver must reject missing or unauthorized credentials, never
print resolved values, resolve credentials only when needed, and fail closed.
Credentials must stay out of source control, bootstrap/user-data, command-line
arguments, release artifacts, and ordinary logs. Production and staging must
use distinct least-privilege credentials, with staging unable to modify or
delete production backups.

## Implemented validation boundaries

The validator checks document shape, duplicate/unknown YAML fields, IDs,
adapter categories and references, secret-reference syntax, environment
classification and backup access, lifecycle safeguards, and timeout/retry
bounds. It does not yet validate provider endpoints, network destinations,
filesystem symlink or traversal policy, runtime authorization, config
migrations, or redacted change plans. Passing validation alone does not
authorize an operation.

The persistent operation store selected for the initial controller is bbolt;
SQLite in the older proposal is superseded. Runtime config loading, environment
isolation enforcement, approval identity, and provider adapters remain later
implementation work. See [the roadmap](ROADMAP.md) for the current Phase 2
gate.
