# PaperMC & systemd Integration Patterns Investigation Report

## Executive Summary

This document details the systemd execution patterns, volume mounting behaviors, health check implementations, and schema shapes used in the Gameserver Controller's PaperMC server deployment flow.

---

## 1. Startup Script Creation Flow (`cmd/vultr-bootstrap/main.go`)

### Key Code Path

```go
// Line 27-28: Interface definition for startup script creation
type scriptCreator interface {
    CreateStartupScript(context.Context, []byte, vultrapi.StartupScriptRequest) (vultrapi.StartupScript, error)
}

// Line 106-111: Main upload flow
err = resolver.WithCredential(*credentialRef, func(token []byte) error {
    var createErr error
    uploaded, createErr = client.CreateStartupScript(ctx, token, vultrapi.StartupScriptRequest{
        Name: *name, Type: "boot", Script: script,
    })
    return createErr
})
```

### Vultr Startup Script Schema (`internal/vultrapi/startup_scripts.go`)

#### StartupScript (Response/Provider Metadata)
```go
type StartupScript struct {
    ID           string `json:"id"`           // Resource identifier (e.g., "script-123")
    DateCreated  string `json:"date_created,omitempty"`
    DateModified string `json:"date_modified,omitempty"`
    Name         string `json:"name"`         // e.g., "gsc-minecraft-debian"
    Type         string `json:"type"`         // "boot" or "pxe"
    Script       string `json:"script"`       // Base64-encoded script content
}
```

#### StartupScriptRequest (Client-side Request)
```go
type StartupScriptRequest struct {
    Name   string  // e.g., "gsc-minecraft-debian"
    Type   string   // Default: "boot" if empty; must be "boot" or "pxe"
    Script []byte   // Plaintext script, max 1 MiB UTF-8 text
}
```

#### Validation Rules
- **Name**: Non-empty string, max 255 characters (line 114-116)
- **Type**: Must be "boot" or "pxe" (line 117-119)
- **Script**: 
  - Non-empty UTF-8 text (line 120-122)
  - Max size: 1 MiB (`maximumStartupScriptBytes = 1 << 20`)
  - No null bytes allowed

#### API Behavior
- **Create**: POST `/startup-scripts` with base64-encoded script body
- **Get**: GET `/startup-scripts/{id}` for reconciliation
- **Update**: PATCH `/startup-scripts/{id}` (no retry on unclear results)
- **Response validation**: Reconciles ID, name, type, and decoded script content (lines 81-88)

---

## 2. Systemd Integration Points (`scripts/bootstrap/minecraft-debian.sh.tmpl`)

### Systemd Unit Files Generated

#### 1. `minecraft-restore.service` (Line 429-439)
```bash
[Unit]
Description=Restore Vanilla No Grief Minecraft data from Wasabi
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/minecraft-restore.sh
RemainAfterExit=yes
```
**Behavior**: One-shot service that runs once to restore data from Wasabi storage. Never starts automatically.

#### 2. `minecraft-vanilla.service` (Line 443-471)
```bash
[Unit]
Description=Vanilla No Grief Minecraft Paper Server
After=network-online.target minecraft-restore.service
Wants=network-online.target
Requires=minecraft-restore.service

[Service]
Type=simple
User=minecraft
Group=minecraft
WorkingDirectory=/opt/minecraft/vanilla_no_grief

ExecStart=/usr/local/sbin/minecraft-start.sh

Restart=on-failure
RestartSec=10
TimeoutStopSec=120
SuccessExitStatus=0 143

NoNewPrivileges=true
PrivateTmp=true=true   # NOTE: BUG - should be "true" not "true=true"
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/opt/minecraft/vanilla_no_grief

[Install]
WantedBy=multi-user.target
```

**Behavior**: Main Minecraft server service that:
- Runs as unprivileged `minecraft` user
- Dependent on `minecraft-restore.service` completing first
- Restarts on failure with 10-second delay
- Uses PaperMC JAR from `/opt/minecraft/vanilla_no_grief/`

#### 3. `mcrcon.socket` (Line 642-658) & `mcrcon.service` (Line 659-678)
```bash
# Socket activation for RCON
[Socket]
ListenStream=127.0.0.1:25577
NoDelay=true
PassCredentials=true

[Install]
WantedBy=sockets.target
```

**Behavior**: Uses systemd socket activation to secure RCON access on localhost only (no firewall rules needed).

### Health Check Pattern (implicit in bootstrap)

The bootstrap does **not** define a dedicated health check systemd unit. Instead:
- Minecraft starts without running until data restore completes
- Operator must verify: `systemctl status minecraft-vanilla.service`
- Logs available via: `journalctl -u minecraft-vanilla.service -f`

### Volume Mounting Pattern (Wasabi Storage)

#### rclone Configuration (Line 38, 431-450 in handoff.go)

```bash
WASABI_REMOTE="wasabi:@@WASABI_REMOTE_PATH@@"
```

**Volume is external Wasabi S3 storage**, mounted via rclone:

**Handoff writes config (`internal/pilothandoff/handoff.go`, Line 439-450):**
```go
return []byte("[wasabi]\ntype = s3\nprovider = Wasabi\naccess_key_id = " + access + 
    "\nsecret_access_key = " + secret +
    "\nregion = " + region + "\nendpoint = s3." + region + ".wasabisys.com\nacl = private\n"), nil
```

**Bootstrap uses this config (Line 42 in template):**
```bash
RCLONE_CONFIG="/root/.config/rclone/rclone.conf"
WASABI_REMOTE="wasabi:@@WASABI_REMOTE_PATH@@"
```

**Restore script (Line 351-368) does:**
```bash
rclone copy --checksum "$WASABI_REMOTE" "$MINECRAFT_DIR" --create-empty-src-dirs
```

**Sync script (Line 417-420) does after server stops:**
```bash
rclone sync "$MINECRAFT_DIR/" "$WASABI_REMOTE/" --checksum
```

**Volume semantics:**
- Data persists in Wasabi S3 bucket/prefix
- Local `/opt/minecraft/vanilla_no_grief` is restored on each new instance
- No local volume mounting; cold starts restore from cloud storage
- `minecraft-restore.service` runs as one-shot before main service starts

---

## 3. Health Check Protocol (Execution Adapter)

The execution adapter uses systemd unit inspection via the protocol check schemas:

### Systemd Unit Schema IDs

| Operation | Schema ID |
|-----------|-----------|
| Inspect state | `urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-inspect-input/output` |
| Start/stop unit | `urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-{start/graceful-stop}-input/output` |
| Force stop | `urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-force-stop-input/output` |
| Logs read | `urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-logs-read-input/output` |
| Health inspect | `urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-health-inspect-input/output` |
| Delete | `urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-delete-input/output` |

### Health Inspect Schema Shape (from `execution_health_conformance.go`)

```json
{
  "evaluations": [
    {
      "case_id": "string",
      "request": {
        "unit_ref": {"unit": "minecraft-vanilla.service", "environment_id": "staging"}
      },
      "response": {
        "unit_ref": {"unit": "minecraft-vanilla.service", "environment_id": "staging"},
        "health": "healthy|unknown|unhealthy",
        "safe_diagnostics": ["line1", "line2"]
      }
    }
  ]
}
```

### Health Check Implementation

**No dedicated systemd health check unit defined in bootstrap.** Instead, the protocol uses:

1. **Unit state inspection**: `systemctl is-active minecraft-vanilla.service`
2. **Logs redaction**: Safe diagnostics from journal with PII redaction
3. **Health states**:
   - `healthy`: Unit active and Paper reports "Done"
   - `unknown`: Unit not found or state unclear
   - `unhealthy`: Unit failed or exit code indicates crash

### Logs Read Schema (from `execution_logs_conformance.go`)

```json
{
  "evaluations": [
    {
      "case_id": "string",
      "request": {
        "unit_ref": {"unit": "minecraft-vanilla.service", ...},
        "max_bytes": 10240
      },
      "response": {
        "unit_ref": {"unit": "minecraft-vanilla.service", ...},
        "lines": ["log line 1", "log line 2"]
      }
    }
  ]
}
```

**Behavior**: Reads up to `max_bytes` from unit logs, applies redaction probes.

---

## 4. Systemd Credential Integration (`internal/credential/systemd.go`)

### Credential Reference Scheme

```go
const SystemdReferenceScheme = "systemd-credential://"
```

### Usage Pattern

Secrets are stored as encrypted systemd credentials in `/run/systemd/credentials/`:

```go
type Credentials interface {
    WithCredential(reference string, use func([]byte) error) error
}

// Example from vultr-bootstrap:
credentialRef := flags.String("credential-ref", "systemd-credential://vultr-bootstrap-api-token", ...)
resolver.WithCredential(*credentialRef, func(token []byte) error {
    // Use Vultr API token
})
```

### Credential Scopes

- **Vultr API token**: Stored for server provisioning operations
- **Wasabi credentials**: Separate credentials for storage access
- **SSH private key**: Encrypted key for controller VM access

---

## 5. Behavioral Patterns Summary

### Startup Sequence (Post-Bootstrap)

1. **VM boots** → Vultr startup script executes `minecraft-debian.sh`
2. **Bootstrap completes** → Installs packages, creates systemd units, writes scripts
3. **Pilot handoff** (separate phase):
   - Verifies VM via single-use SSH host key
   - Swaps host key for persistent controller key
   - Sends Wasabi credentials to pinned key only
   - Writes rclone config to `/root/.config/rclone/rclone.conf`
4. **Run `minecraft-restore.service`** → Restores data from Wasabi
5. **Finalize security** → Opens firewall, enables services
6. **Start Minecraft** → Operator runs `systemctl start minecraft-vanilla.service`

### Restart Behavior

```bash
# In service unit (Line 471)
Restart=on-failure
RestartSec=10
TimeoutStopSec=120
```

- Restarts on failure with exponential backoff (10s base)
- Stops gracefully up to 120 seconds
- Success status includes SIGTERM exit code (143)

### Volume Persistence Strategy

- **Persistent**: Wasabi S3 storage (`/bucket/path`)
- **Local ephemeral**: `/opt/minecraft/vanilla_no_grief`
- **Restore on cold start**: `minecraft-restore.service` downloads from Wasabi
- **Sync to cloud**: `minecraft-sync.sh` uploads after shutdown
- **No local volume mount** for persistence; all state in Wasabi

### Health Check Implementation

The bootstrap does **not** define a dedicated systemd health check unit. Instead:

1. Operator queries `systemctl status minecraft-vanilla.service`
2. Logs via `journalctl -u minecraft-vanilla.service -f`
3. Protocol-level inspection uses schema validation of state transitions

**For production**, consider adding a health check socket like:

```bash
cat > /etc/systemd/system/minecraft-health.socket <<'EOF'
[Unit]
Description=Health check socket for Minecraft

[Socket]
ListenStream=8080
Accept=noop

[Install]
WantedBy=sockets.target
EOF

cat > /etc/systemd/system/minecraft-health.service <<'EOF'
[Unit]
Description=Health endpoint for Minecraft service

[Service]
Type=simple
ExecStart=/usr/local/sbin/minecraft-start.sh health-check-endpoint
```

---

## 6. Schema Reference Table

| Component | Schema ID Example | Location |
|-----------|-------------------|----------|
| Start unit | `execution.systemd.unit.start` | Protocol conformance |
| Graceful stop | `execution.systemd.unit.graceful-stop` | Protocol conformance |
| Force stop | `execution.systemd.unit.force-stop` | Protocol conformance |
| Logs read | `execution.systemd.unit.logs.read` | Conformance tests |
| Health inspect | `execution.systemd.unit.health.inspect` | Conformance tests |
| Delete | `execution.systemd.unit.delete` | Conformance tests |

---

## 7. Example Request/Response Shapes

### Create Startup Script Request

```json
{
  "name": "gsc-minecraft-debian",
  "type": "boot",
  "script": "CG4oIyEvdXNyL2Jpbi9lbnYgYmFzaA..." // base64-encoded bootstrap
}
```

### Create Startup Script Response

```json
{
  "startup_script": {
    "id": "script-abcd1234",
    "name": "gsc-minecraft-debian",
    "type": "boot",
    "date_created": "2026-11-16T08:00:00Z",
    "script": "CG4oIyEvdXNyL2Jpbi9lbnYgYmFzaA..." // base64-encoded
  }
}
```

### Systemd Unit Inspect Response

```json
{
  "unit_ref": {
    "unit": "minecraft-vanilla.service",
    "environment_id": "staging"
  },
  "state": "running",
  "health": "healthy"
}
```

---

## Notes & Known Issues

1. **Line 469 in template**: `PrivateTmp=true=true` is a bug; should be just `true`
2. **No dedicated health check unit**: Bootstrap doesn't define one; operator must use protocol-level inspection
3. **Volume mounting via rclone**: External Wasabi S3, not block volume mounts
4. **Credential handling**: Uses systemd credentials for secret storage; never logs secrets
