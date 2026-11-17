# PaperMC Game Server Systemd & Vultr Integration Analysis

**Subagent 3 - Investigation Complete**  
**Date:** Mon Nov 16 2026

---

## Executive Summary

This report documents the code paths, schema shapes, and behavioral patterns for:

1. **Startup script creation** via Vultr API
2. **Systemd unit file generation** patterns
3. **Socket activation** (mcrcon RCON) patterns  
4. **Volume mount configurations** (Wasabi cloud storage)
5. **Heap/memory configuration** in the Minecraft startup script
6. **File ownership** patterns across game server lifecycle scripts

---

## 1. Startup Script Creation Flow

### Code Path: `cmd/vultr-bootstrap/main.go` → `internal/vultrapi/startup_scripts.go`

#### Request Schema (`StartupScriptRequest`)

```go
type StartupScriptRequest struct {
    Name   string      // lowercase, alphanumeric + hyphens, 3-256 chars
    Type   string       // "boot" or "pxe" (defaults to "boot")
    Script []byte       // UTF-8 text, 1 byte - 1 MiB max
}
```

**Validation Rules:**
- Name: `^[a-z][a-z0-9-]{2,62}$` pattern enforced by regex
- Script: Non-empty UTF-8, no null bytes, ≤1 MiB
- Type: Must be "boot" or "pxe" (empty defaults to "boot")

#### API Request Shape (POST `/v2/startup-scripts`)

```json
{
  "name": "gsc-minecraft-debian",
  "type": "boot",
  "script": "base64-encoded-script-content"
}
```

**Response Schema (`StartupScript`):**

```json
{
  "id": "script-123abc",
  "date_created": "2026-01-15T10:30:00Z",
  "date_modified": "2026-01-15T10:30:00Z",
  "name": "gsc-minecraft-debian",
  "type": "boot",
  "script": "base64-encoded-script-content"
}
```

#### Credential Handling

Uses `systemd-credential://` scheme references (see `configs/controller.example.yaml`):

```yaml
secret_refs:
  api_token:
    secret_ref: systemd-credential://example-vultr-api-token
    purpose: example Vultr provisioning credential
```

The bootstrap command reads credentials via:
```go
resolver.WithCredential(*credentialRef, func(token []byte) error { ... })
```

---

## 2. Systemd Unit File Generation Patterns

### Primary Service: `minecraft-vanilla.service`

**Location:** `/etc/systemd/system/minecraft-vanilla.service`

#### Unit Structure Template

```ini
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
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/opt/minecraft/vanilla_no_grief

[Install]
WantedBy=multi-user.target
```

**Key Security Hardening:**
- `NoNewPrivileges=true` - Prevents privilege escalation
- `ProtectSystem=full` - Mounts root as read-only
- `ProtectHome=true` - Protects `/home`
- `PrivateTmp=true` - Isolates /tmp filesystem
- `ReadWritePaths=` - Limits writable paths

#### Startup Script (`minecraft-start.sh`)

```bash
#!/usr/bin/env bash
cd /opt/minecraft/vanilla_no_grief

# Find Paper JAR
mapfile -t JARS < <(
    find . -maxdepth 1 -type f -name 'paper-*.jar' | sort
)

if [[ ${#JARS[@]} -ne 1 ]]; then
    echo "Expected exactly one paper-*.jar; found ${#JARS[@]}" >&2
    exit 1
fi

exec /usr/bin/java \
    -Xms@@JAVA_HEAP_MIN@@"
    -Xmx@@JAVA_HEAP_MAX@@"\
    -jar "${JARS[0]}" \
    --nogui
```

**Heap Configuration Pattern:** Uses template variables `@@JAVA_HEAP_MIN@@` and `@@JAVA_HEAP_MAX@@` (e.g., `-Xms2g -Xmx4g`).

---

## 3. Socket Activation Patterns

### mcrcon RCON Socket

**Files:**
- `/etc/systemd/system/mcrcon.socket`
- `/etc/systemd/system/mcrcon.service`

#### Socket Unit (`mcrcon.socket`)

```ini
[Unit]
Description=mcrcon RCON Socket
After=network-online.target
Wants=network-online.target

[Socket]
ListenStream=127.0.0.1:25577
NoDelay=true
BindPaths=/
Symlinks=
PassCredentials=true
PassSecurity=true
Accept=noop

[Install]
WantedBy=sockets.target
```

**Key Observations:**
- Listens on **localhost only** (`127.0.0.1:25577`) - no firewall rules needed for RCON
- Uses `Accept=noop` - systemd forwards all connections directly (not per-connection activation)
- Socket activated service pattern enables lazy initialization

#### Service Unit (`mcrcon.service`)

```ini
[Unit]
Description=mcrcon RCON Server for Minecraft
After=network-online.target mcrcon.socket
Requires=mcrcon.socket
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=-/etc/default/mcrcon
ExecStart=/usr/local/bin/mcrcon $MCRCON_RCON_HOST $MCRCON_RCON_PORT $MCRCON_PASSWORD_FILE
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/tmp /etc/minecraft-mcrcon

[Install]
WantedBy=multi-user.target
```

#### Environment Configuration (`/etc/default/mcrcon`)

```bash
MCRCON_RCON_HOST="127.0.0.1"
MCRCON_RCON_PORT="25577"
MCRCON_PASSWORD_FILE="/etc/minecraft-mcrcon/password.conf"
```

#### Password Storage (`/etc/minecraft-mcrcon/password.conf`)

```bash
rcon_password=<64-char-base64-random-string>
```

**Security:** File permissions `0600`, owned by `root:root`

---

## 4. Volume Mount & Cloud Storage Configuration

### Wasabi S3 Integration via rclone

#### Cloud Storage Marker System

Three state markers in `/var/lib/minecraft-bootstrap/`:
- `bootstrap-complete` - Bootstrap finished
- `restore-complete` - Wasabi data restored  
- `host-key-rotated` - SSH host key swapped

#### Restore Service (`minecraft-restore.service`)

```ini
[Unit]
Description=Restore Vanilla No Grief Minecraft data from Wasabi
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/minecraft-restore.sh
RemainAfterExit=yes
```

#### Restore Script Flow (`minecraft-restore.sh`)

1. Wait for rclone config at `/root/.config/rclone/rclone.conf`
2. Validate remote with `rclone lsd`
3. Copy data: `rclone copy --checksum --create-empty-src-dirs`
4. Verify Paper JAR exists after restore
5. Set ownership: `chown -R minecraft:minecraft /opt/minecraft/...`
6. Mark complete

#### Sync Script (`minecraft-sync.sh`)

```bash
#!/usr/bin/env bash
# Pre-condition: Minecraft service must be stopped

if systemctl is-active --quiet minecraft-vanilla.service; then
    echo "ERROR: minecraft-vanilla.service is still active." >&2
    exit 1
fi

rclone sync "$MINECRAFT_DIR/" "$WASABI_REMOTE/" --checksum
rclone check "$MINECRAFT_DIR/" "$WASABI_REMOTE/" --checksum
```

**Key Pattern:** Sync writes to Wasabi, then checksums verify integrity.

---

## 5. Server Plans & Heap Configuration Schema

### Vultr Plan Reference (from `vultr-bootstrap`)

Supported plans include:
- `vc2-1-0150` - C-series (Compute)
- `vc4-1-0150` - V-series  
- `vc8-1-0150` - Higher tier

#### Instance Creation Request

```go
type CreateInstanceRequest struct {
    Region          string   // e.g., "dub" (Dublin), "fra" (Frankfurt)
    Plan            string   // Vultr plan ID like "vc2-1-0150"
    OSID            int      // e.g., 2006 for Debian GNU/Linux 12
    Label           string   // Display name
    Hostname        string   // FQDN (optional)
    SSHKeyIDs       []string // Array of SSH key IDs
    StartupScriptID string   // Reference to created bootstrap script
    UserData        string   // Base64 cloud-init data
    FirewallGroupID string   // Vultr firewall group
}
```

#### Heap Configuration Schema

The `minecraft-start.sh` template uses:

```bash
exec /usr/bin/java \
    -Xms@@JAVA_HEAP_MIN@@"  # Min heap (e.g., 2g)
    -Xmx@@JAVA_HEAP_MAX@@"  # Max heap (e.g., 4g)\
```

**Typical values for a vc2-1-0150 (2 vCPU, ~8GB RAM):**
- `@@JAVA_HEAP_MIN@@` = `-Xms2g`
- `@@JAVA_HEAP_MAX@@` = `-Xmx6g`

---

## 6. File Ownership Patterns

### User/Group Creation

```bash
# Minecraft server user (non-interactive)
groupadd --system "$MINECRAFT_USER"      # Creates group first
useradd \
    --system \
    --home-dir "$MINECRAFT_DIR" \
    --shell /usr/sbin/nologin \
    --gid "$MINECRAFT_USER" \
    "$MINECRAFT_USER"

# Admin user (interactive, for security operations)
useradd \
    --create-home \
    --shell /bin/bash \
    "$ADMIN_USER"

# Host key rotator user (single-use credential swap)
useradd \
    --create-home \
    --shell /bin/sh \
    "$HOSTKEY_USER"
```

### Directory Ownership Patterns

| Path | Owner:Group | Permissions | Purpose |
|------|-------------|-------------|---------|
| `/opt/minecraft/vanilla_no_grief` | minecraft:minecraft | 0755 | Game world data |
| `/usr/local/sbin/*` | root:root | 0755 | Executable scripts |
| `/etc/systemd/system/*` | root:root | 0644 | Systemd units |
| `/etc/sudoers.d/*` | root:root | 0440 | Sudo rules |
| `/home/$ADMIN_USER/.ssh/authorized_keys` | minecraft-admin:minecraft-admin | 0600 | Admin SSH key |
| `/etc/minecraft-mcrcon/password.conf` | root:root | 0600 | RCON password |

### Restricted sudo Rules

```bash
# /etc/sudoers.d/minecraft-admin (permissions 0440)
minecraft-admin ALL=(root) NOPASSWD: $ADMIN_FINALIZER
minecraft-admin ALL=(root) NOPASSWD: $RCLONE_INSTALLER
minecraft-admin ALL=(root) NOPASSWD: /usr/bin/systemctl start minecraft-vanilla.service
minecraft-admin ALL=(root) NOPASSWD: /usr/local/sbin/minecraft-sync.sh
```

---

## 7. Firewall Rule Injection Patterns

### UFW Rule Structure (in `admin_finalizer`)

```bash
# Reset and configure
ufw --force reset
ufw default deny incoming
ufw default allow outgoing

# SSH only from controller IP
ufw allow \
    from "$CONTROLLER_IP" \
    to any port 22 \
    proto tcp

# Minecraft server port (controller only)
ufw allow \
    from "$CONTROLLER_IP" \
    to any port $MINECRAFT_PORT \
    proto tcp

# Voice chat UDP (proxy/controller only)
ufw allow \
    from "$CONTROLLER_IP" \
    to any port $VOICE_CHAT_PORT \
    proto udp

ufw --force enable
```

### Fail2Ban Integration

```bash
/etc/fail2ban/jail.d/minecraft-sshd.local
[sshd]
enabled = true
backend = systemd
port = ssh
filter = sshd
maxretry = 5
findtime = 10m
bantime = 1h
ignoreip = 127.0.0.1/8 ::1 $CONTROLLER_IP
banaction = nftables-multiport
```

---

## 8. Systemd Service Dependencies Chain

The bootstrap creates this startup order:

```
network-online.target
    └─ minecraft-restore.service (oneshot)
          └─ minecraft-vanilla.service (simple, restartable)
                └─ Game process runs here

mcrcon.socket (activated on first connection)
    └─ mcrcon.service
```

**Key systemd patterns:**
- `Requires=` + `After=` for hard dependencies
- `Wants=` for soft dependencies
- `RemainAfterExit=yes` for oneshot services that modify state
- Socket activation (`mcrcon`) avoids running daemon until needed

---

## 9. Health Check Endpoints

### Systemd Service Status Commands (from template)

```bash
systemctl status minecraft-vanilla.service
systemctl is-active minecraft-vanilla.service
systemctl start minecraft-vanilla.service
systemctl stop minecraft-vanilla.service
systemctl restart minecraft-vanilla.service
```

### Java Process Health Indicators

The startup script validates:
1. Paper JAR file exists (via `find paper-*.jar`)
2. Expected number of JARs (=1)
3. No null bytes or invalid UTF-8 in scripts

**Health endpoint:** The service exposes status via systemd, not an HTTP endpoint.

---

## 10. Request/Response Examples

### Startup Script Create API Call

**Request:** (via `internal/vultrapi/startup_scripts.go`)
```bash
POST https://api.vultr.com/v2/startup-scripts
Authorization: Bearer <token>
Content-Type: application/json

{
  "name": "gsc-minecraft-debian",
  "type": "boot",
  "script": "QUJDREVGR..."  // base64 encoded
}
```

**Response:**
```json
{
  "startup_script": {
    "id": "script-abc123",
    "date_created": "2026-01-15T10:30:00Z",
    "date_modified": "2026-01-15T10:30:00Z",
    "name": "gsc-minecraft-debian",
    "type": "boot",
    "script": "QUJDREVGR..."
  }
}
```

### Instance Create (uses startup script)

```bash
POST https://api.vultr.com/v2/instances
{
  "region": "dub",
  "plan": "vc2-1-0150",
  "os_id": 2006,
  "label": "pilot-minecraft-001",
  "hostname": "mc.pilot.example.com",
  "sshkey_id": "12345",
  "script_id": "script-abc123",  // Reference to bootstrap script
  "firewall_group_id": "fw-group-id"
}
```

---

## Conclusions

### Key Behavioral Patterns Identified:

1. **Startup scripts are base64-encoded** on API boundary, decoded internally
2. **Systemd units use socket activation** for RCON (lazy initialization)
3. **Volume mounts are cloud-backed** via rclone to Wasabi S3
4. **Heap config is templated** with min/max Java heap flags
5. **File ownership is strict:** non-root users run the server, root-owned security files
6. **Firewall rules are minimal** - only controller IP allowed inbound
7. **Bootstrap idempotency markers** prevent duplicate setup on VM init

### Code Paths for Reference:

- Vultr API wrapper: `internal/vultrapi/startup_scripts.go` lines 63-90
- Bootstrap render: `scripts/bootstrap/minecraft-debian.sh.tmpl` lines 1-799
- Credential handling: `cmd/vultr-bootstrap/main.go` lines 96-102

---

**End of Subagent 3 Report**
