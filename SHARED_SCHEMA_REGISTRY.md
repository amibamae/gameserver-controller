# Shared Schema Registry
## Coordination Document for Protocol-Hardening Milestone

**Baseline:** `origin/main` at `fb606f54de6268ff0fe1e76c67014571364404cb`  
**Coordination agent:** Lead engineer (current session)

---

## 1. Wasabi Storage Schema (Agent 2 - COMPLETE ✅)

### Capability Registration

```yaml
capabilities:
  - name: storage.object.put
    mutating: true
    schema_uri: storage/object-put
    description: Upload new objects to Wasabi bucket
  
  - name: storage.object.get  
    mutating: false
    schema_uri: storage/object-get
    description: Download object contents with verification
    
  - name: storage.object.head
    mutating: false
    schema_uri: storage/object-head
    description: Inspect object metadata without downloading
    
  - name: storage.object.list
    mutating: false  
    schema_uri: storage/object-list
    description: List objects in bucket/prefix
    
  - name: storage.object.copy
    mutating: true
    schema_uri: storage/object-copy
    description: Server-side copy within same bucket
    
  - name: storage.object.delete
    mutating: true
    schema_uri: storage/object-delete
    description: Delete object (exact version required)
    
  - name: storage.object.versioning.inspect
    mutating: false
    schema_uri: storage/object-versioning-inspect  
    description: Check bucket versioning status
    
  - name: storage.object.retention.inspect
    mutating: false
    schema_uri: storage/object-retention-inspect
    description: Check object lock/legal hold status
```

### Core Data Structures (JSON Schema)

```go
// Config - Adapter configuration (non-secret)
type StorageWasabiConfig struct {
    AdapterID          string      // e.g., "storage.wasabi-staging"
    StorageProfileID   string      // e.g., "staging-storage"
    EnvironmentID      string      // e.g., "staging", "production"
    Container          string      // Wasabi bucket name (e.g., "backups")
    KeyPrefix          string      // Object prefix with trailing "/"
    Region             string      // Wasabi region  
    EndpointURL        string      // HTTPS URL: https://s3.X.wasabisys.com
    AccessKeyIDRef     string      // systemd-credential://<ref>
    SecretAccessKeyRef string      // systemd-credential://<ref>
    MaxObjectBytes     int64       // Max object size for this profile
}

// Copy operation input
type StorageCopyInput struct {
    SourceRef         objectReference `json:"source_ref"`
    Destination       objectTarget    `json:"destination"`
    ExpectedSizeBytes int64           `json:"expected_size_bytes"`
    ExpectedChecksum  checksum        `json:"expected_checksum"`
}

// Checksum verification
type Checksum struct {
    Algorithm string `json:"algorithm"` // Always "sha256"
    Digest    string `json:"digest"`    // Hex-encoded SHA-256 digest (64 hex chars)
}
```

### Copy Operation Contract

**Preconditions:**
- Source object exists with exact version
- Destination does not exist
- Checksum is valid SHA-256

**Postconditions:**
- Object copied with same content
- Size and checksum verified after copy
- New version_id assigned

**Error Handling:**
- Missing source: Returns graceful error, no operation
- Failed copy: Returns `InvocationError` with side-effect=true
- Checksum mismatch: Operation fails immediately

---

## 2. PaperMC + Systemd Execution Schema (Agent 3 - COMPLETE ✅)

### Startup Script Creation Contract

**Request Shape:**
```json
{
  "name": "gsc-minecraft-debian",
  "type": "boot",
  "script": "<base64-encoded-utf8-script>"
}
```

**API Endpoint:** `POST /v2/startup-scripts`

### Systemd Service Unit Schema

**Service Pattern (from minecraft-debian.sh.tmpl):**

```ini
[Unit]
Description=Vanilla No Grief Minecraft Paper Server
After=network-online.target minecraft-restore.service
Requires=minecraft-restore.service

[Service]
Type=simple
User=minecraft              # Game runs as non-root
ExecStart=/usr/local/sbin/minecraft-start.sh
Restart=on-failure
ProtectSystem=full          # Read-only root filesystem
PrivateTmp=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

### Socket Activation Schema (mcrcon)

**Socket Configuration:**
```ini
[Socket]
ListenStream=127.0.0.1:25577
Accept=noop                 # Forward connections directly
PassCredentials=true

[Install]
WantedBy=sockets.target
```

**Security Model:**
- Socket bound to localhost only (no firewall rules needed)
- Password file mode `0600` root-owned
- sudoers allow-list for `minecraft-admin` user

### Volume Mount Schema

**Idempotency Markers:**
- `/var/lib/minecraft-bootstrap/bootstrap-complete`
- `/var/lib/minecraft-bootstrap/restore-complete`  
- `/var/lib/minecraft-bootstrap/host-key-rotated`

**Restore Flow:**
1. Wait for rclone config → validates remote
2. Copy data from Wasabi bucket
3. Verify Paper JAR checksum
4. Mark restore complete

### Heap Configuration Schema

```bash
# Embedded in minecraft-start.sh
-Xms@@JAVA_HEAP_MIN@@"    # e.g., -Xms2g (50% of plan RAM)
-Xmx@@JAVA_HEAP_MAX@@"    # e.g., -Xmx6g (full plan RAM)
```

### File Ownership Matrix

| User | Purpose | Shell | Permissions |
|------|---------|-------|-------------|
| `minecraft` | Game server runtime | `/usr/sbin/nologin` | Non-root, restricted |
| `minecraft-admin` | Security operations | `/bin/bash` | sudoers allow-listed |
| `gsc-hostkey` | Host key rotation (ephemeral) | `/bin/sh` | Single-use, auto-delete |

### Heap Calculation Rules

```go
// From template.go
func CalculateHeap(plan string) (min, max int64) {
    ram := vultrPlanToRAM[plan]  // e.g., vc2 → 2GiB
    return ram/2, ram            // min=50%, max=100%
}

// Vultr plan constants
const (
    vultrVC2   = 2 * 1024 * 1024 * 1024
    vultrVC4   = 4 * 1024 * 1024 * 1024
    vultrVC6   = 6 * 1024 * 1024 * 1024
    vultrVC8   = 8 * 1024 * 1024 * 1024
    vultrVC10  = 10*1024*1024*1024
    vultrVC11  = 11*1024*1024*1024
    vultrVC12  = 12*1024*1024*1024
    vultrVC16  = 16*1024*1024*1024
    vultrVC32  = 32*1024*1024*1024
)
```

---

## 3. Vultr Hosting Schema (Agent 1 - IN PROGRESS 🔄)

### Findings Summary

**Current State:**
- No existing Vultr hosting adapter implemented
- Controller can create VMs via pilot CLI but lacks durable workflow integration
- `internal/fakeadapter` provides testing interface pattern

### Required Implementation

**Minimal Vultr Adapter Capabilities:**
- `vultr.server.create` - Create new server instance
- `vultr.server.get` - Get server details (including plan, IP)
- `vultr.server.start` - Boot server from Pxe boot
- `vultr.server.stop` - Shut down gracefully  
- `vultr.server.delete` - Destroy server and volume

**Request/Response Patterns:**
```go
type VultrServer struct {
    Label       string  // Server name
    Memory      int     // RAM in GB
    Disk        int     // Volume size in GB
    Region      string  // e.g., "lon-01", "nyc-01"
    Plan        string  // e.g., "vc2-1-0150", "vc4-1-0150"
    IPv4        string  // Public IP address
    Status      string  // active, started, suspended, etc.
}

type VultrStartupScript struct {
    Name   string
    Script []byte
    Type   string  // "boot" or "pxe"
}
```

### Implementation Strategy

1. Create `internal/vultrapi/client.go` with HTTP client for Vultr API
2. Register capabilities via `Descriptor()` method
3. Wire into durable workflow engine (same pattern as fakeadapter)
4. Implement minimal operations: create/get/start/stop/delete server
5. Integrate with bootstrap flow from `scripts/bootstrap/`

---

## 4. Conformance Test Schema (Agent 4 - IN PROGRESS 🔄)

### Proposed Test Structure

```go
package protocolcheck

import (
    "testing"
    "github.com/amibamae/gameserver-controller/scripts/bootstrap"
)

func TestSettings_Validate_RejectsEmptyPlan(t *testing.T) {
    s := bootstrap.Settings{ControllerIP: "1.2.3.4"}
    if err := s.Validate(); err == nil {
        t.Fatal("expected validation error for empty Plan")
    }
}

func TestCalculateHeap_MatchesVultrPlan(t *testing.T) {
    tests := []struct {
        plan   string
        expectMin  int64
        expectMax  int64
    }{
        {"vc2-1-0150", 1*1024*1024*1024,      2*1024*1024*1024},
        {"vc4-1-0150", 2*1024*1024*1024,      4*1024*1024*1024},
    }
    
    for _, tt := range tests {
        t.Run(tt.plan, func(t *testing.T) {
            settings := bootstrap.Settings{Plan: tt.plan}
            if min := settings.MinHeap(); min != tt.expectMin {
                t.Errorf("expected MinHeap=%d, got %d", tt.expectMin, min)
            }
        })
    }
}

func TestTemplate_ContainsMcrconSection(t *testing.T) {
    template, _ := bootstrap.ReadFile("minecraft-debian.sh.tmpl")
    if !strings.Contains(template, "Installing and configuring mcrcon") {
        t.Fatal("mcrcon section missing from template")
    }
}

func TestCopyObject_HandlesInvalidPath(t *testing.T) {
    // Test error handling for invalid Wasabi paths
    input := operations.CopyInput{
        SourceRef:  ...objectReference,
        Destination: operations.objectTarget{
            Container: "invalid-bucket-name-without-underscore", // invalid
        },
    }
    
    _, err := operations.CopyObject(context.Background(), config, input)
    if !errors.Is(err, wasabi.ErrInvalidBucket) {
        t.Errorf("expected ErrInvalidBucket, got %v", err)
    }
}
```

---

## Coordination Status

| Agent | Schema Type | Status | Contribution to Registry |
|-------|-------------|--------|--------------------------|
| Agent 1 | Vultr Hosting | 🔄 In Progress | Will add server lifecycle operations schema |
| Agent 2 | Wasabi Storage | ✅ Complete | Storage operations, copy/delete contracts, auth patterns |
| Agent 3 | Systemd + PaperMC | ✅ Complete | Bootstrap flow, heap config, socket activation, systemd units |
| Agent 4 | Conformance Tests | 🔄 In Progress | Behavioral test cases for all adapters |

---

## Integration Points

### How Schemas Interconnect

1. **Bootstrap Flow:**
   - Vultr adapter creates server with specified plan (Agent 1)
   - Calculate heap from plan using PaperMC schema (Agent 3)
   - Render template with heap placeholders (primary session)
   - Upload startup script to Vultr via API

2. **Deployment Sequence:**
   ```
   Vultr.create_server(plan="vc4") 
       → sets JAVA_HEAP_MIN=2GiB, JAVA_HEAP_MAX=4GiB
       → mcrcon installs at /usr/local/bin/mcrcon
       → systemd socket activates on 127.0.0.1:25577
   ```

3. **Test Coverage:**
   - Conformance tests verify all schema contracts
   - Each adapter implements required capabilities
   - Error handling patterns tested with fixtures

---

## Next Steps

1. Await Vultr adapter implementation (Agent 1)
2. Await conformance test designs (Agent 4)  
3. Integrate all schemas into shared registry
4. Run full verification suite before commits to main
5. Update documentation with schema specifications
