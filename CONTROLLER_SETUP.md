# Controller Setup Procedure

This document describes the complete setup of the Game Server Controller on an existing Debian machine running systemd. The setup wizard provides provider credential validation, audit logging, and rollback procedures to ensure safe installation.

## Prerequisites

- A Debian 12+ machine with systemd
- sudo/administrator privileges
- Go 1.22+ for building the controller binary
- Systemd-creds for encrypted credential storage
- Vultr CLI or API access (for cloud operations)

## Quick Start

```bash
# Build and test the setup wizard
cd wasabi-backup-manifest-integration
go run ./cmd/controller-setup --help

# Preview installation plan
go run ./cmd/controller-setup plan --config <config.yaml> --json

# Generate encrypted credentials (one-time)
sudo go run ./cmd/controller-setup credentials generate \
  --config <config.yaml> \
  --name vultr-adapter-api-token

# Apply controller configuration
sudo go run ./cmd/controller-setup apply \
  --config <config.yaml> \
  --binary /path/to/controller \
  --confirm INSTALL-GAMESERVER-CONTROLLER
```

## Detailed Steps

### Step 1: Build the Controller Binary

```bash
go build -o controller ./cmd/controller/serve
```

### Step 2: Validate System Prerequisites

```bash
# Check for required tools and systemd support
sudo go run ./cmd/controller-setup plan --config <config.yaml> --json
```

The plan command validates that:
- Debian OS is running
- systemd is available
- All required paths are absolute and valid
- No overlapping directories

### Step 3: Configure Encrypted Credentials

Provider credentials (API tokens, access keys) are stored encrypted using `systemd-creds`. Generate a random idempotency key first:

```bash
sudo go run ./cmd/controller-setup credentials generate \
  --config <config.yaml> \
  --name vultr-adapter-api-token
```

Then securely enter the credential via prompt. The value is encrypted and stored at `/etc/credstore.encrypted/<credential-name>`.

For SSH key management:

```bash
# Generate a new SSH key pair
sudo go run ./cmd/controller-setup ssh-key generate \
  --public-key-out /tmp/gsc-pilot-ssh-key.pub

# Or import an existing private key
sudo go run ./cmd/controller-setup ssh-key import \
  --private-key-file <private.key>
```

### Step 4: Install the Controller

```bash
sudo go run ./cmd/controller-setup apply \
  --config <config.yaml> \
  --binary /path/to/controller \
  --confirm INSTALL-GAMESERVER-CONTROLLER
```

The installation process:
1. Creates the `gameserver-controller` service account
2. Installs the controller binary to `/usr/local/bin/controller`
3. Places configuration at `/etc/gameserver-controller/controller.yaml`
4. Sets up systemd unit at `/etc/systemd/system/gameserver-controller.service`
5. Prepares private state directory at `/var/lib/gameserver-controller`
6. Loads encrypted credentials into the systemd unit

### Step 5: Enable and Start the Service

```bash
sudo systemctl enable gameserver-controller
sudo systemctl start gameserver-controller
```

## Provider Credential Validation

Before installation, all provider credentials are validated:
- Credentials must exist in `/etc/credstore.encrypted/`
- File permissions must be `0600` (owner-only)
- Files must not be empty

If any credential is missing or invalid, the installation fails with a clear error message. This prevents accidental use of stale or compromised credentials.

## Audit Logging

The setup wizard records all operations to JSON log files at `/var/log/gameserver-controller/audit.json`. Each entry includes:
- Timestamp in RFC3339 format
- Operation name (e.g., "apply-install-bundle-start")
- Component identifier
- Log level (info, error)

## Rollback Procedures

If installation fails after partial completion, rollback can be performed:

```bash
sudo systemctl stop gameserver-controller
sudo systemctl disable gameserver-controller
```

Manual rollback steps:
1. Stop and disable the systemd service
2. Remove installed files using `rm -rf`
3. Delete the service account with `userdel --remove-home gameserver-controller`

## API Keys Management

The controller can issue scoped API keys for management operations:

```bash
sudo go run ./cmd/controller-setup keys issue \
  --database /var/lib/gameserver-controller/state.db \
  --actor api-user \
  --audience gameserver-controller \
  --expires-in 24h \
  --scope read:inventory:* \
  --scope write:inventory:*
```

## Security Considerations

- All credentials are encrypted at rest using `systemd-creds`
- State directory permissions are `0750` with service account ownership
- Configuration files are `0640` readable by root and group only
- SSH keys are stored as encrypted systemd credentials
- No credentials appear in shell history or logs

## Troubleshooting

### Credential validation fails
Ensure that:
- `/etc/credstore.encrypted/` exists with mode `0700`
- All referenced credentials exist in the directory
- Files have permissions `0600`

### Service won't start
Check systemd status:
```bash
systemctl status gameserver-controller
journalctl -u gameserver-controller -n 50
```

### Audit logs not recording
Ensure write permissions to `/var/log/gameserver-controller/`:
```bash
sudo mkdir -p /var/log/gameserver-controller
sudo chmod 750 /var/log/gameserver-controller
```

## Related Documentation

- [Protocol Specification](protocol/conformance/requirements.md)
- [Architecture Overview](docs/ARCHITECTURE.md)
- [ROADMAP](docs/ROADMAP.md)
- [Contributing Guidelines](CONTRIBUTING.md)
