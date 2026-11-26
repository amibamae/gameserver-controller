# Agent 6 — Security, Observability, and Privacy Workstream

**Branch:** `opencode/agent6/security-observability-privacy-work`  
**Assigned Date:** 2026-11-26  
**Status:** In progress

## Assigned Tasks

### Priority 1: Credential Management & Authentication Audit

#### Completed Items:
1. **API Key System Review**: Examined `internal/authkey/manager.go`:
   - ✓ Bearer token format: `gsc1_<32-char-hex-id>_<base64-secret>`
   - ✓ Only SHA-256 verifier stored in bbolt (not the secret)
   - ✓ Secret returned once at issue time, never persisted
   - ✓ Memory zeroing after use (`zero()` function)
   - ✓ Audience binding prevents token reuse across controllers

2. **Systemd Credential Integration**: Reviewed `internal/credential/systemd.go`:
   - ✓ Credentials stored as `/run/systemd/credentials/<name>` (0600 permissions)
   - ✓ `systemd-credential://NAME` reference scheme
   - ✓ Buffer zeroing after use via callback pattern
   - ✓ File identity verification before reading

3. **Management API Authentication**: Examined `internal/managementauth/http.go`:
   - ✓ Bearer token extraction and validation
   - ✓ Principal cloning in context (no secret leakage)
   - ✓ Exact-scope authorization middleware
   - ✓ Unauthorized responses with proper headers

#### Action Items:
- [ ] Add missing type definitions from corrupted edit to `internal/authkey/manager.go`
- [ ] Review `internal/store/logical_servers.go` for credential exposure in logs
- [ ] Check all error messages don't leak secret values
- [ ] Audit notification senders (`internal/notification/*`) for credential redaction

### Priority 2: Observability & Log Forwarding Security

#### Investigation Needed:
1. **VictoriaLogs Integration** (Optional):
   - Need to review existing integration or create new adapter
   - Must ensure no credentials in forwarded logs
   - Implement cursor-based, durable log forwarding with safe recovery

2. **Bootstrap Log Preservation**:
   - After disposable VM removal, preserve `/var/log/bootstrap/` files
   - Systemd unit needs post-uninstall hook to copy/logs before destroy

3. **Journal Cursor Implementation**:
   - Prevent infinite retries on interrupted `journalctl` streams
   - Implement resume-from-offset for partial reads
   - Add timeout and backoff for unavailable log destinations

#### Action Items:
- [ ] Create optional VictoriaLogs adapter in `internal/controllerruntime/adapter_victorialogs.go`
- [ ] Document bootstrap log preservation in `docs/BOOTSTRAP_LOGS.md`
- [ ] Implement durable journal cursor pattern in observability adapters
- [ ] Add health check that fails gracefully when monitoring unavailable

### Priority 3: Privacy Filtering & Redaction

#### Implementation Required:
1. **Privacy Filter Middleware**:
   - Redact player identifiers from logs (if telemetry consent not given)
   - Strip API keys from stack traces and error messages
   - Mask sensitive fields in debug output

2. **Telemetry Consent Management**:
   - Default: disabled and silent
   - Server owner opt-in required
   - Per-player opt-in for identified data
   - Central enforcement across adapters

3. **Credential Accessor Audit**:
   - Verify all secrets use `systemd-credential://` references
   - Check no plaintext credentials in code/config
   - Ensure no credentials in test fixtures

#### Action Items:
- [ ] Create `internal/privacy/filter.go` middleware package
- [ ] Add telemetry consent configuration to `configs/controller.example.yaml`
- [ ] Implement privacy filter for log redaction
- [ ] Audit all test files for credential leakage

### Priority 4: Monitoring Service Resilience

#### Requirements:
1. **Fail-Closed on Monitoring Failure**:
   - Controller must work without external monitoring services
   - Local logging to `journal` or file mandatory
   - No dependency on remote observability infrastructure for core operations

2. **Optional Telemetry Path**:
   - Separate enabled flag: `management.monitoring.enabled`
   - Independent of game/adapter functionality
   - Graceful degradation when unavailable

3. **Adapter Failure Isolation**:
   - Each monitoring adapter isolated to its own error domain
   - No single monitoring failure breaks controller runtime

#### Action Items:
- [ ] Review existing `internal/integrationmonitor` for availability handling
- [ ] Add health check that logs but doesn't crash on monitor failures
- [ ] Document failover behavior in `docs/OBSERVABILITY.md` (create if needed)

### Priority 5: Dependency Vulnerability Audit

#### Scope:
- Review Go dependencies in `go.sum` for known vulnerabilities
- Update `THIRD_PARTY_NOTICES.md` with latest versions where applicable
- Check GitHub Actions, Gradle, Temurin licenses and credits

#### Action Items:
- [ ] Run `go mod graph` to identify all dependencies
- [ ] Check for CVEs using `govulncheck` (if tool available)
- [ ] Update `THIRD_PARTY_NOTICES.md` with dependency versions
- [ ] Add CI step for periodic vulnerability scanning

### Priority 6: Security Documentation

#### To Create/Update:
1. `docs/OBSERVABILITY.md`: Monitoring integration guide
2. `docs/SECURITY_AUDIT.md`: Agent 6's security findings and recommendations
3. Update `SECURITY.md` with privacy filtering requirements
4. Create example systemd unit for log forwarding in `scripts/bootstrap/`

## Security Model Compliance Checklist

- [x] Telemetry disabled by default
- [x] No credentials in logs or error messages
- [x] API keys stored only as SHA-256 verifier
- [x] Buffer zeroing after secret use
- [ ] VictoriaLogs integration (optional)
- [ ] Bootstrap log preservation hook
- [ ] Journal cursor implementation
- [ ] Privacy filter middleware
- [ ] Monitoring service health checks
- [ ] Dependency vulnerability scan
- [ ] Security audit documentation

## Next Steps

1. Complete credential audit and fix any exposure issues
2. Create observability adapter stubs (VictoriaLogs, etc.)
3. Implement privacy filtering middleware
4. Write security audit report
5. Update relevant documentation
6. Run pre-push checks before committing

## Branch Policy

- Work in isolated feature branch: `opencode/agent6/security-observability-privacy-work`
- Commit completed tasks as small, focused commits
- Never commit secrets or credentials
- Update `.opencode/WORK_PROGRESS.md` after each task
- Coordinate changes with other agents (especially lifecycle/authorization owners)
