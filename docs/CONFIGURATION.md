# Configuration and Secret References

**Status:** The v1 configuration validator is implemented in `config-check`. It checks files without loading runtime settings or resolving provider credentials.

## Validate a file

```sh
go run ./cmd/config-check validate configs/controller.example.yaml
```

The command accepts one YAML document, rejects unknown fields, checks references and safety rules, and prints a validation summary. It does not read secrets, open the configured state-store path, contact providers, or start operations.

The example uses fictional adapter profiles and named systemd credential
references; it contains no credential values. It defines a production boundary
but includes only a staging test-server profile, so you do not have to adopt a
live production server into controller-managed inventory.

## Version 1 shape

The validator requires `schema_version: 1`, local controller mode, a bbolt state-store path, bounded operation timeouts and retry defaults, and enabled local audit.

Adapter profiles use one of five protocol categories: `hosting`, `storage`, `execution`, `game`, or `integration`. Their IDs must use the matching category prefix. Each server points to an existing environment and hosting, storage, execution, and game profile. Integration is optional.

### Network profile

A server may add a `network` block saying how players reach it. It follows the [network profile requirements](../protocol/adapters/integrations.md#6-network-profile-requirements) in the protocol. Without the block the join mode is `none`.

| `join_mode` | Required settings |
|---|---|
| `proxy` | `proxy`: an integration adapter such as `integration.velocity`; `backends`: at least one |
| `direct_dns` | `dns`: an integration adapter such as `integration.cloudflare-dns`; `join_hostname` |
| `none` | Nothing else may be set |

Each proxy backend has an `id` (the proxy server name), a `port`, and an `address_mode`:

- `instance_ip`: the proxy connects to the instance's current public IP from the hosting provider.
- `hostname`: the proxy connects to `hostname`, a managed DNS name kept pointed at the instance, so an IP change only updates DNS. This also requires `dns`.

```yaml
network:
  join_mode: proxy
  proxy: integration.velocity
  dns: integration.cloudflare-dns   # only needed for hostname backends
  backends:
    - id: paper-main
      address_mode: hostname
      hostname: paper-main.backends.example.com
      port: 25565
```

To give every backend its own domain from one naming scheme, set `backend_hostname_template` instead of per-backend hostnames. It must contain `{backend}` and may also use `{server}` and `{environment}`. Backends then default to `address_mode: hostname`, and setting `hostname` by hand is rejected so every name follows the scheme:

```yaml
network:
  join_mode: proxy
  proxy: integration.velocity
  dns: integration.cloudflare-dns
  backend_hostname_template: "{backend}.{environment}.mc.example.com"
  backends:
    - id: paper-main        # paper-main.production.mc.example.com
      port: 25565
    - id: paper-creative    # paper-creative.production.mc.example.com
      port: 25566
```

Every backend hostname, from a template or set by hand, must be unique across all servers in the file. The DNS adapter profile must manage each generated name.

The older `adapters.integration` field is still accepted. If it is set together with `network.proxy`, they must name the same adapter. The validator checks these settings; the controller runtime does not use them yet.

Environments are `production`, `staging`, or `testing`. Production declares `production_backup_access: read_write`; staging and testing may declare `none` or `read_only`, never `read_write`. The validator checks these policy declarations but does not enforce access at the storage service. A server must list verified backup and operator approval as destroy prerequisites. The validator checks an optional idle-shutdown duration but does not schedule it.

Each server may define `lifecycle.backups.schedule` and `lifecycle.backups.retention`. The defaults are every 9 hours and keep the newest five verified backups. Schedule units are `minutes`, `hours`, `days`, `weeks`, `months`, or `years`; month and year intervals use calendar units, anchored to the original schedule date. If a month lacks that day, the occurrence uses that month's last day and later occurrences return to the original day. Set `keep_last: 0` to retain backups indefinitely. The configuration validator checks this policy, but scheduled execution and retention cleanup are not wired into a controller runtime yet. Cleanup must run only after a new backup is verified and must use the normal exact-object authorization and retention checks.

Relative state-store paths are resolved against the configuration file by `ResolveStateStorePath`; absolute local paths remain absolute. The validator does not create parent directories, canonicalize symlinks, or stop a separate runtime from opening a path.

## Secret references

Version 1 accepts references only in the explicit
`systemd-credential://NAME` form. Each reference records a purpose and may
include permission scopes and a version reference. Validation checks syntax;
it does not read the credential, reveal its value, or verify its permissions.
Literal credentials and environment-variable references are rejected. A
reference names a credential loaded for the controller service; it does not
contain or encrypt the credential value.

The `internal/credential` package provides a bounded accessor for named
systemd credentials, but the controller runtime and provider adapters do not
call it yet. Runtime integration must reject missing or unauthorized
credentials, never print resolved values, load them only when needed, and fail
closed. For the initial Debian/systemd deployment, store values as encrypted
systemd credentials and load only the named values needed by the controller
service. Keep credentials out of source control,
bootstrap/user-data, command-line arguments, process environment variables,
release artifacts, operation records, and ordinary logs. Production and
staging storage must use separate least-privilege credentials, with staging
unable to modify or delete production backups. See [secret management](SECRET_MANAGEMENT.md)
for the deployment rules. Validation rejects a shared storage credential
reference across production and staging profiles.

## What validation covers

The validator checks document shape, duplicate and unknown YAML fields, IDs,
adapter categories and references, protected credential-reference syntax,
environment and backup-access rules, lifecycle safeguards, and timeout/retry
bounds.

It does not yet check provider endpoints, network destinations, filesystem symlink or traversal policy, runtime authorization, config migrations, or redacted change plans. Passing validation does not authorize an operation.

The initial operation store uses bbolt; SQLite from the earlier proposal is superseded. Runtime config loading, environment-isolation enforcement, approval identity, and provider adapters remain future work. See the [roadmap](ROADMAP.md) for the current Phase 2 gate.
