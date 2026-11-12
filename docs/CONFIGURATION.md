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

Every backend hostname, from a template or set by hand, must be unique across all servers in the file, and must lie inside the DNS adapter's zone. Backend IDs must also be unique among servers that share one proxy, because the proxy knows backends only by ID.

With `join_mode: direct_dns`, `fallback_records` is required: the A and/or AAAA values (normally the lobby's address) the join name points at while the server is not ready.

```yaml
network:
  join_mode: direct_dns
  dns: integration.cloudflare-dns
  join_hostname: play.example.com
  fallback_records:
    A: [203.0.113.10]
```

The proxy and DNS adapters a server uses must belong to the server's environment.

The older `adapters.integration` field is still accepted. If it is set together with `network.proxy`, they must name the same adapter.

At startup the controller checks every server's network profile against the adapters it registered (see [integrations section 6](../protocol/adapters/integrations.md#6-network-profile-requirements)). A server whose profile needs a capability that no adapter in its environment advertises is reported and cannot be planned; the controller does not skip or emulate the missing step. Only adapters with `implementation: velocity` or `cloudflare` are bound to an environment, so a declared-only or process adapter cannot satisfy a network requirement yet. The workflows that register backends, move players, and update DNS are not wired yet.

Environments are `production`, `staging`, or `testing`. Production declares `production_backup_access: read_write`; staging and testing may declare `none` or `read_only`, never `read_write`. The validator checks these policy declarations but does not enforce access at the storage service. A server must list verified backup and operator approval as destroy prerequisites. The validator checks an optional idle-shutdown duration but does not schedule it. `idle_shutdown` is the minimum time a server must be idle; with the default scaling policy below it is then kept until its next billing decision window.

### Scaling and billing

Providers such as Vultr bill each server in whole hours from creation until it is destroyed, so shutting a server down early in an hour saves nothing. Each server may define `lifecycle.scaling`:

```yaml
lifecycle:
  idle_shutdown: 15m                   # minimum idle time before shutdown
  scaling:
    billing_increment: 1h              # default
    decision_window_start: 58m         # default
    decision_window_end: 59m           # default
    hourly_cost: "0.036"               # optional, provider account currency
    ignore_billing_below_hourly_cost: "0.005"   # optional
```

While a server is running, the controller adds or removes servers only inside the decision window, measured from the start of each hour the server has run (by default minutes 58 to 59). An idle server is shut down only when it has been idle for at least `idle_shutdown` and the window is open; otherwise it stays up for the rest of the paid hour, and returning players cancel the shutdown. Starting a server when none is running is never delayed, because players are waiting. If `hourly_cost` is below `ignore_billing_below_hourly_cost`, the window is ignored and the server scales as soon as it is idle.

#### Planned: vertical auto-scaling and side-by-side migration

**These options are planned and not yet accepted by the validator.** Adding them to a configuration today makes it fail validation, because unknown fields are rejected. They are documented here so operators can plan for them.

```yaml
controller:
  capacity:
    side_by_side_migration:
      enabled: true        # default true; false never allows a third server
      max_servers: 1       # default 1 extra server; 0 means unlimited

servers:
  - id: example
    lifecycle:
      side_by_side_migration:   # optional per-server override of the controller setting
        enabled: true
        max_servers: 1
      scaling:
        plans:                  # plan ladder, cheapest first; scale one rung at a time
          - vc2-1c-2gb
          - vc2-2c-4gb
          - vc2-4c-8gb
```

The controller will move a server one rung up or down the plan ladder at a time, through a side-by-side migration (see [core design](CORE_DESIGN.md#vertical-auto-scaling-planned)). The old live server is destroyed only after its final backup is verified. A migration is the only case where more than two managed servers may run, within `max_servers`. The thresholds that trigger a move are not designed yet.

Shutting down includes stopping the game, backing up, verifying, and destroying the instance. If that takes longer than the time left in the hour after the window, the next hour is charged; move the window earlier once real shutdown times are known. The validator checks these settings; the controller runtime does not schedule them yet.

Each server may define `lifecycle.backups.schedule` and `lifecycle.backups.retention`. The defaults are every 9 hours and keep the newest five verified backups. Schedule units are `minutes`, `hours`, `days`, `weeks`, `months`, or `years`; month and year intervals use calendar units, anchored to the original schedule date. If a month lacks that day, the occurrence uses that month's last day and later occurrences return to the original day. Set `keep_last: 0` to retain backups indefinitely. The configuration validator checks this policy, but scheduled execution and retention cleanup are not wired into a controller runtime yet. Cleanup must run only after a new backup is verified and must use the normal exact-object authorization and retention checks.

### Adapter implementations

An adapter profile may set `implementation` to tell the controller runtime to start it. Without `implementation`, the profile is declared for cross-reference checks only and the runtime does not start it.

- `implementation: wasabi` builds the built-in Wasabi storage adapter (category `storage` only). The `wasabi:` block holds non-secret settings, and `secret_refs` must define exactly `access_key_id` and `secret_access_key`. A server that uses this adapter must be in the same environment as `wasabi.environment_id`. See [the Wasabi adapter](WASABI_STORAGE_ADAPTER.md) for the meaning of each field.
- `implementation: velocity` builds the built-in Velocity proxy adapter (category `integration`). The `velocity:` block names the `environment_id`, the `bridge_url` of the [proxy bridge](PROXY_BRIDGE_API.md) (`https`, or plain `http` only to a loopback address), the `proxy_instance_id` the bridge must report, and an optional request `timeout` (at most 2m). `secret_refs` must define exactly `bridge_token`. At startup the controller checks the bridge's identity: a different instance or API version stops startup; an unreachable bridge does not, but every proxy call is refused until the check passes. Backends come from the network profiles of the servers routed through this proxy. An `instance_ip` backend looks its IP up from Vultr using the `api_token` secret ref of the server's hosting profile; without one, the lookup fails and that backend cannot be registered.
- `implementation: cloudflare` builds the built-in Cloudflare DNS adapter (category `integration`) for one zone. The `cloudflare:` block names the `environment_id`, the 32-character `zone_id`, and the `zone_name`. `secret_refs` must define exactly `api_token` (a token scoped to DNS edit for that zone). The records it may change are derived from network profiles: each hostname backend's name (`backend_address`), and each `direct_dns` join name (`direct_join` and `fallback`, with `fallback_records`). A Cloudflare profile that no network profile uses is rejected.
- `implementation: process` starts an adapter executable over [`jsonrpc-stdio-v1`](../protocol/rpc/transport.md). The executable must report the configured adapter ID and category, or startup fails. `env` is the complete environment for the child; nothing is inherited from the controller, so put only non-secret settings there. Process adapters cannot use the storage byte-stream profile yet.

```yaml
adapters:
  - id: storage.wasabi-staging
    category: storage
    implementation: wasabi
    secret_refs:
      access_key_id:
        secret_ref: systemd-credential://wasabi-staging-access-key-id
        purpose: staging Wasabi access key ID
      secret_access_key:
        secret_ref: systemd-credential://wasabi-staging-secret-access-key
        purpose: staging Wasabi secret access key
    wasabi:
      storage_profile_id: wasabi-staging
      environment_id: staging
      container: example-staging-backups
      key_prefix: server/
      region: us-west-1
      endpoint_url: https://s3.us-west-1.wasabisys.com
      max_object_bytes: 1073741824
      require_version_ids: true
  - id: execution.local
    category: execution
    implementation: process
    process:
      path: /usr/lib/gameserver-controller/execution-adapter
      args: [--profile, local]
      env: [LANG=C.UTF-8]
      start_timeout: 10s   # optional, default 10s, at most 10m
      stop_timeout: 10s    # optional, default 10s, at most 10m
```

`config-check validate` also checks the Wasabi, Velocity, and Cloudflare settings with each adapter's own rules and confirms that each process path is an executable file. It never contacts a provider or reads credentials.

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
systemd credentials. The controller runtime uses it for Wasabi adapter
profiles, reading `CREDENTIALS_DIRECTORY` at startup and the values only at
invocation time. Runtime integration must reject missing or unauthorized
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

The initial operation store uses bbolt; SQLite from the earlier proposal is superseded. `controller serve` now builds its runtime from this configuration (see [architecture](ARCHITECTURE.md)); environment-isolation enforcement at the storage service, approval identity, and most provider adapters remain future work. See the [roadmap](ROADMAP.md) for the current Phase 2 gate.
