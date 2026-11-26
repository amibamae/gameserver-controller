# Observability and Monitoring Integration Guide

This document describes how to integrate optional monitoring services with the controller. The controller operates correctly without external observability infrastructure; these integrations are purely optional enhancements.

## Architecture Principles

1. **Fail-Closed**: Controller must work when monitoring unavailable
2. **Local Logging Primary**: All logs forwarded to systemd journal or file first
3. **Remote Forwarding Secondary**: External monitors (VictoriaLogs, Prometheus, etc.) are optional destinations
4. **No Core Dependencies**: Monitoring adapter failures never prevent server lifecycles
5. **Privacy-First**: Credentials and player data redacted before forwarding

## Available Integrations

### VictoriaLogs (Optional)

**Purpose**: Centralized log aggregation for Kubernetes deployments or on-premise observability stacks.

**Implementation Status**: Optional, not required for pilot. See `internal/controllerruntime/adapter_victorialogs.go` when created.

**Integration Points**:
- Bootstrap service logs: `/var/log/bootstrap/*.log`
- Controller runtime logs: journal + optional remote destination
- Backup verification events
- Scaling policy notifications

**Configuration**:
```yaml
management:
  monitoring:
    enabled: false  # default, keep disabled
    destination: "victorialogs"  # or "none"
    endpoint: "http://logs.victorialogs.example.com:9420/api/v1/logs"
    token_ref: systemd-credential://victorialogs-token
    buffer_size: 256KB
    flush_interval: 30s
    health_check_path: /health
    disable_on_error: true  # fail closed behavior
```

### Bootstrap Log Preservation

After a disposable VM is destroyed, bootstrap logs must be preserved for audit purposes.

**Systemd Unit Hook**: Add to `scripts/bootstrap/minecraft-debian.sh.tmpl`:

```bash
# After destroying server but before removing instance record
if [ -n "$VICTORIALOGS_ENDPOINT" ]; then
    # Copy bootstrap logs to staging location
    mkdir -p /tmp/bootstrap-logs/$(date +%Y%m%d-%H%M%S)
    cp /var/log/bootstrap/*.log /tmp/bootstrap-logs/
    journalctl -u minecraft-restore.service --no-pager > /tmp/bootstrap-logs/restore.log 2>/dev/null || true
    journalctl -u minecraft-vanilla.service --no-pager >> /tmp/bootstrap-logs/vanilla.log 2>/dev/null || true
fi

# Destroy server normally...
```

### Journal Cursor Implementation

To avoid infinite retries on interrupted `journalctl` streams, implement cursor-based tracking:

**Pattern**: Track the last processed journal entry offset per destination.

```go
// Pseudocode for durable cursor
type LogForwarder struct {
    lastOffset map[string]uint64  // destination -> cursor
    mu         sync.Mutex
}

func (f *LogForwarder) advance(offset uint64, dest string) error {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.lastOffset[dest] = offset
    return nil
}

// Reboot recovery: load cursor from persistent storage on init
```

## Privacy Filtering

All logs forwarded remotely must be privacy-filtered before leaving the server.

### Redaction Rules

- Never forward player usernames, UUIDs, or world names without consent
- Strip API keys from stack traces and error messages  
- Mask partial paths in file operations (e.g., `/var/lib/gameserver/` -> `///lib/**/)
- Never log full URLs containing credentials

### Implementation

Create `internal/privacy/filter.go`:

```go
package privacy

import (
    "regexp"
    "strings"
)

func FilterLog(line string) string {
    // Redact secrets
    line = secretRegexp.ReplaceAllString(line, "[REDACTED]")
    
    // Mask player data if telemetry not consented
    if !telemetryConsented {
        line = strings.ReplaceAll(line, "Steve", "[PLAYER]")
        line = strings.ReplaceAll(line, "Alex", "[PLAYER]")
        line = strings.ReplaceAll(line, "<player:.*>", "<player:[REDACTED]>")
    }
    
    return line
}

var secretRegexp = regexp.MustCompile(`(?i)(password|api[_-]?key|token|secret)[=:][^\s"']+`)
```

## Health Check Implementation

Ensure monitoring failures don't break controller:

```yaml
management:
  monitoring:
    health_check_interval: 30s
    max_retries: 3
    retry_backoff: 5s
    treat_unavailable_as_error: false
```

**Behavior**:
- On first failure: log warning, skip forwarding
- On second failure: increase backoff
- After max retries: stop retrying, continue operating locally
- Re-enable automatically when destination responds

## Security Checklist for Observability Work

- [ ] VictoriaLogs adapter created in `internal/controllerruntime/adapter_victorialogs.go`
- [ ] Privacy filter middleware implemented
- [ ] Bootstrap log preservation hook in bootstrap script template
- [ ] Journal cursor implementation for durable forwarding
- [ ] Health check with fail-closed behavior
- [ ] No credentials in forwarded logs
- [ ] Telemetry consent flag in configuration
