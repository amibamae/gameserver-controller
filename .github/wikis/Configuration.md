# Configuration Guide

This guide documents the configuration schema for Game Server Controller. The controller uses YAML files to define lifecycle rules, adapter connections, and server definitions.

---

## Core Configuration Structure

The main configuration file (`controller.yaml`) has this structure:

```yaml
schema_version: 1
controller:                    # Controller settings
  state_store: "state.db"      # bbolt database path
  log_level: "info"           # debug, info, warn, error
  management_api:             # Optional HTTP API
    enabled: false
    bind_address: "0.0.0.0:8443"

adapters:                     # Adapter profiles
  - id: storage.wasabi-staging
    category: storage
    implementation: wasabi
    secret_refs: ...          # Credential references
    wasabi: ...               # Wasabi-specific settings

servers:                      # Server definitions
  - id: example-server
    environment: production
    hosting: hosting.vultr-prod
    lifecycle_rules: ...      # Lifecycle configuration
    adapters:                 # Which adapters to use
      storage: storage.wasabi-staging
      hosting: hosting.vultr-prod
```

---

## Schema Version

- `schema_version: 1` - Current schema version. Don't change unless upgrading.

---

## Controller Settings

### State Store

The controller uses bbolt (a pure Go key-value store) to persist operation state:

```yaml
controller:
  state_store: "state.db"
```

- **state_store**: Path to the bbolt database file. Can be relative or absolute path.
- Default is `state.db` in the current directory.

### Logging

```yaml
controller:
  log_level: "info"
```

Options:
- `debug` - Verbose logging, includes internal events
- `info` - Normal operational logging (recommended)
- `warn` - Only warnings and errors
- `error` - Critical errors only

### Management API (Optional)

Enable an HTTP management interface for advanced use cases:

```yaml
controller:
  management_api:
    enabled: true
    bind_address: "0.0.0.0:8443"
```

When enabled, the API exposes:
- Operation queries
- Server status checks
- Durable operation tracking

**Note:** Production deployments typically leave this disabled, relying on CLI/API calls instead.

---

## Adapter Configuration

Adapters handle provider-specific operations. Each adapter has:

- `id`: Unique identifier (referenced from server definitions)
- `category`: One of hosting, storage, execution, game, integration
- `implementation`: Provider name (e.g., `wasabi`, `vultr`, `systemd`)

### Secret References

Credentials use the `systemd-credential://` format to avoid storing secrets in config files:

```yaml
secret_refs:
  access_key_id:
    secret_ref: systemd-credential://wasabi-access-key-id
    purpose: Wasabi access key ID
  secret_access_key:
    secret_ref: systemd-credential://wasabi-secret-key
    purpose: Wasabi secret access key
```

The controller reads secrets from systemd credentials rather than the config file itself. Never store actual credentials in configuration files.

---

## Server Definitions

Each server entry specifies:

### Environment

```yaml
servers:
  - id: my-server
    environment: production    # or staging
```

Environments enforce boundaries:
- **production**: Real deployments, verified backups required for destruction
- **staging**: Test environments, separate storage credentials

### Hosting Adapter

```yaml
hosting: hosting.vultr-prod   # adapter id from adapters list
```

The hosting adapter provisions and tears down VMs. Referenced adapter must be in the `adapters` section.

### Lifecycle Rules

Define when and how servers scale:

```yaml
lifecycle_rules:
  idle_shutdown: "15m"          # Minimum idle time before considering shutdown
  scaling:
    billing_increment: "1h"     # Billable hour increment (cloud provider default)
    decision_window_start: "58m"  # Check if shutdown should happen (min 58)
    decision_window_end: "59m"   # Confirm idle state (min 59)
```

The `decision_window_start` and `decision_window_end` ensure a server is truly idle before shutting down. The window defaults to 1 minute (58-59 minutes past the hour) for billing purposes.

---

## Adapter Categories

### Hosting Adapters

Create, destroy, and inspect VMs; manage firewalls. Example Vultr adapter:

```yaml
hosting:
  provider: vultr
  image_id_template: "debian-12-x64-{version}"
  ssh_key_path: "~/.ssh/id_ed25519"
```

### Storage Adapters

Upload, download, list, and verify backups. Wasabi example:

```yaml
storage:
  endpoint: "s3.wasabisys.com"
  region: "US-EAST-1"
  storage_class: "STANDARD"
  verify_checksum: true
```

### Execution Adapters

Manage game processes via systemd or similar. Collects readiness signals (e.g., Paper reporting ready on port 25565).

### Game Adapters

Game-specific operations like saves, installs, backups. For Minecraft/Paper:

```yaml
game:
  engine: paper
  save_command: "stop"         # Signal to Paper server
  backup_path: "/world/{timestamp}.dat"
```

### Integration Adapters

Route player traffic and manage DNS. Velocity proxy example:

```yaml
integration:
  velocity:
    bind_address: "0.0.0.0:8192"
    max_players: 100
```

---

## Network Configuration

Define how players connect to the server:

```yaml
network:
  join_mode: proxy              # proxy, direct_dns, or none
  proxy: integration.velocity   # Velocity proxy adapter id
  dns: integration.cloudflare-dns  # Optional DNS handoff
  backends:
    - id: paper-main
      address_mode: hostname     # instance_ip or hostname
      hostname: paper.main.example.com
      port: 25565
```

- `join_mode`: How players join
  - `proxy`: Route through Velocity proxy
  - `direct_dns`: Direct to game server IP (no proxy)
  - `none`: No networking configured

- `backends`: List of available game servers. Each backend has:
  - `address_mode`: Use hostname or instance IP
  - `hostname`: Hostname for player connections
  - `port`: Game port (default 25565)

---

## Scaling Configuration

Lifecycle scaling settings:

```yaml
lifecycle:
  idle_shutdown: "15m"
  scaling:
    billing_increment: "1h"
    decision_window_start: "58m"
    decision_window_end: "59m"
```

- `idle_shutdown`: Minimum time a server can be idle before shutdown consideration
- `scaling.decision_window_*`: Timing for scaling decisions; must be within 1-minute windows

---

## Example: Complete Configuration File

Here's a minimal working configuration:

```yaml
schema_version: 1
controller:
  state_store: "state.db"
  log_level: "info"

adapters:
  - id: storage.wasabi-staging
    category: storage
    implementation: wasabi
    secret_refs:
      access_key_id:
        secret_ref: systemd-credential://wasabi-access-key-id
        purpose: staging Wasabi access key ID
      secret_access_key:
        secret_ref: systemd-credential://wasabi-secret-key
        purpose: staging Wasabi secret access key
    wasabi:
      endpoint: "s3.wasabisys.com"
      region: "US-EAST-1"
  
  - id: hosting.vultr-prod
    category: hosting
    implementation: vultr
    vultr:
      api_token_secret_ref: systemd-credential://vultr-api-token

servers:
  - id: my-server
    environment: production
    lifecycle_rules:
      idle_shutdown: "15m"
      scaling:
        billing_increment: "1h"
        decision_window_start: "58m"
        decision_window_end: "59m"
    hosting: hosting.vultr-prod
    storage: storage.wasabi-staging
    game:
      engine: paper
    network:
      join_mode: proxy

```

---

## Validating Configuration

Before starting the controller, validate your configuration:

```bash
go run ./cmd/config-check validate configs/controller.example.yaml
```

This checks:
- Schema version compatibility
- Required fields present
- Secret reference format
- Adapter IDs referenced exist

---

## Common Issues

### "Secret not found" Error

Ensure systemd credentials exist before starting the controller:

```bash
systemctl --user list-credentials
```

Generate missing credentials with:

```bash
controller-setup credentials generate --config <file> --name <credential-name>
```

### Adapter Not Found

Verify adapter IDs in server definitions match entries in the `adapters` section. Missing adapters cause startup failure.

---

## Production Considerations

For production deployments:

1. Use absolute paths for `state_store`
2. Set appropriate `log_level` (`warn` or `error` for prod)
3. Enable network binding only on needed interfaces
4. Store secrets via systemd credentials, never in config files
5. Configure DNS handoff if using `direct_dns` mode

---

*Last updated: November 2026*