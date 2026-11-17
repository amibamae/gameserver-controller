# Quick Start Guide

Get Game Server Controller up and running in minutes with these steps.

---

## Prerequisites

Before you begin, ensure you have:

- **Go 1.23+** ([download](https://go.dev/dl/))
- **Git** for version control
- **Debian-based Linux system** (tested on Debian 12)
- A Vultr hosting account (or compatible cloud provider)
- Wasabi or S3-compatible storage access

---

## Installation

### Step 1: Clone the Repository

```bash
git clone https://github.com/amibamae/gameserver-controller.git
cd gameserver-controller
```

### Step 2: Install CLI Tools

Install the binaries you'll need:

```bash
# Controller binary
go install ./cmd/controller

# Setup wizard  
go install ./cmd/controller-setup

# Config validator
go install ./cmd/config-check

# Protocol conformance checker
go install ./cmd/protocol-check
```

Add Go's binary directory to your PATH if needed:

```bash
export PATH=$PATH:$GOPATH/bin
# or for Go 1.22+ with modules
export PATH=$PATH:$(go env GOPATH)/bin
```

### Step 3: Validate Configuration

Before running anything, validate the example configuration file:

```bash
go run ./cmd/config-check validate configs/controller.example.yaml
```

---

## First-Time Setup

### Option A: Manual Installation (Recommended)

Follow these steps to set up GSC on an existing Debian/systemd machine:

#### Step 1: Validate System Readiness

```bash
controller-setup validate --config configs/controller.example.yaml
```

This checks:
- Configuration without making host changes
- Required systemd user account exists
- State directories are accessible

#### Step 2: Install Controller Components

```bash
controller-setup install --config configs/controller.example.yaml
```

This installs:
- Controller binary in your system path
- Hardened systemd unit file  
- Service account with private state directories
- Adapter configuration files

**Note:** This doesn't enable or disable any services. It just prepares the system for controller operation.

#### Step 3: Generate Credentials

```bash
# Generate API key for management API
controller-setup credentials generate --config configs/controller.example.yaml --name management-api-key

# Store the generated key securely - you won't see it again!
```

#### Step 4: Configure Adapters

Edit your configuration file to add adapter profiles. See [Configuration Guide](configuration) for details.

Example adapter configuration:

```yaml
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
      storage_profile_id: wasabi-staging
      environment_id: staging
```

#### Step 5: Start the Controller

```bash
controller serve --config configs/controller.example.yaml
```

The controller will start with:
- bbolt state store initialized
- Adapter registry loaded  
- No production adapters (as configured)

### Option B: Using Setup Wizard Script

Want an automated alternative? Run:

```bash
controller-setup wizard --config configs/controller.example.yaml
```

This interactive wizard walks you through all setup steps with prompts and sensible defaults.

---

## Running Locally for Development

For local development without systemd:

```bash
# Start in foreground for debugging
controller serve --config configs/controller.example.yaml

# Or run with specific options
controller serve \
  --config configs/controller.example.yaml \
  --log-level debug \
  --state-store ./local-state.db
```

---

## Configuration Reference

### Core Configuration File Structure

```yaml
schema_version: 1
controller:
  state_store: "state.db"           # bbolt database path
  log_level: "info"                 # debug, info, warn, error
  management_api:                   # Optional HTTP API
    enabled: false
    bind_address: "0.0.0.0:8443"

adapters:                           # Adapter profiles
  - id: storage.wasabi-staging
    category: storage
    implementation: wasabi
    # ... configuration

servers:                            # Server definitions
  - id: example-server
    environment: production
    hosting: hosting.vultr-prod
```

### Network Profile Example

```yaml
network:
  join_mode: proxy                  # proxy, direct_dns, or none
  proxy: integration.velocity       # Velocity proxy adapter
  dns: integration.cloudflare-dns   # Cloudflare DNS adapter (optional)
  backends:
    - id: paper-main
      address_mode: hostname        # instance_ip or hostname
      hostname: paper.main.example.com
      port: 25565
```

### Scaling Configuration

```yaml
lifecycle:
  idle_shutdown: 15m                # Minimum idle time before shutdown
  scaling:
    billing_increment: 1h           # Billable hour increment (default)
    decision_window_start: 58m      # When to consider scaling (min 58)
    decision_window_end: 59m        # Decision window end (min 59)
```

### Secret References

All credentials use the `systemd-credential://` format:

```yaml
secret_refs:
  access_key_id:
    secret_ref: systemd-credential://wasabi-access-key-id
    purpose: Wasabi access key ID
  secret_access_key:
    secret_ref: systemd-credential://wasabi-secret-key
    purpose: Wasabi secret access key
```

**Never store credentials in configuration files or source control!**

---

## Testing Locally

### Step 1: Run Tests

```bash
go test -count=1 ./...
go test -race -count=1 ./...
```

### Step 2: Validate Protocol Conformance

```bash
go run ./cmd/protocol-check
```

### Step 3: Check Code Formatting

```bash
gofmt -l .  # Should print nothing
```

### Step 4: Run Complexity Check (for changed functions)

```bash
go run ./cmd/complexity-check -base origin/main
```

---

## Common Operations

### Start a Server

```bash
controller api POST /v1/servers \
  --data '{
    "server": {
      "id": "my-server",
      "definition": {...},
      "operations": [
        {
          "operation": "server.start",
          "parameters": {...}
        }
      ]
    }
  }'
```

### Check Server Status

```bash
controller api GET /v1/servers/{server-id}
```

### Backup a Running Server

```bash
controller api POST /v1/servers/{server-id}/operations \
  --data '{"operation": "server.backup"}'
```

### Destroy a Server (After Verification)

```bash
controller api POST /v1/servers/{server-id}/operations \
  --data '{"operation": "server.destroy", "parameters": {...}}'
```

**Note:** The controller will fail destroy if a backup hasn't been verified.

---

## Troubleshooting

### Controller Won't Start

Check the state store directory exists and is writable:

```bash
ls -la $(grep state_store configs/controller.example.yaml | awk '{print $NF}')
```

### Credential Errors

Ensure systemd credentials exist:

```bash
systemctl --user show-status
systemctl --user list-credentials
```

Generate missing credentials:

```bash
controller-setup credentials generate --config <file> --name <credential-name>
```

### Adapter Not Found

Verify adapter profiles are in the configuration file. Missing adapters will cause startup failure.

---

## Next Steps

After getting started:

1. Read the **[Configuration Guide](Configuration)** for detailed schema reference
2. Explore the **[Protocol Specification](Protocol-Spec)** to understand operations
3. Review the **[Security Model](Security-Model)** before production use
4. Check out the **[Contributing Guide](Contributing)** to help improve the project

---

## Getting Help

- **Configuration issues:** See [Configuration documentation in docs/](../docs/wiki-configuration.md)
- **Protocol questions:** Read [protocol/README.md](../../protocol/README.md)
- **Architecture doubts:** Check [ARCHITECTURE.md](../../docs/ARCHITECTURE.md)
- **Security concerns:** Review [SECURITY_MODEL.md](../../docs/SECURITY_MODEL.md) and [SECURITY.md](../../SECURITY.md)

---

*Last updated: November 2026*
