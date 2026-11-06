# Security Model and Threat Assessment

**Status:** Initial threat-model baseline for design review. This is not a security certification or complete operational assessment.

## Assets to protect

- Production world data and backups.
- Provider API tokens, storage credentials, SSH keys, and proxy secrets.
- Player identifiers and personal data.
- Server availability and integrity.
- Approved bootstrap revisions, provider OS selections, and software releases.
- Controller state, operation history, approvals, and audit records.
- Production network boundaries and backend access restrictions.

## Trust boundaries

1. Operator to controller: commands, configuration, approvals, credentials.
2. Controller core to adapters: policy and adapter execution.
3. Controller to provider APIs: cloud resources and infrastructure control.
4. Controller to object storage: backups, release artifacts, manifests.
5. Controller to game runtime: process control, files, RCON/console, health checks.
6. Controller to proxy: player routing and backend registration.
7. Production to staging: copied world data and restricted read-only access.
8. Public source repository to bootstrap execution: reviewed code, release provenance, and privileged script execution.
9. Optional telemetry destination: separately consented data leaving deployment.

Adapters, plugins, downloaded artifacts, provider responses, and game files are not trusted merely because the controller uses them.

## Threats and mitigations

| Threat | Example | Required mitigation |
|---|---|---|
| Credential disclosure | Token appears in logs or repository | Secret references, redaction, least privilege, rotation |
| Unauthorized destruction | Wrong VM selected for deletion | Exact target confirmation, environment binding, approval, backup verification |
| Data loss | VM destroyed after failed upload | Fail closed; verified backup required before destroy |
| Staging contamination | Test world overwrites production | Separate credentials, storage paths, explicit environment checks |
| Adapter compromise | Malicious adapter exfiltrates secrets | Adapter permissions, review, restricted secret access, audit |
| Supply-chain compromise | Malicious plugin or binary | Provenance, checksums, pinned versions, review, isolated validation |
| Proxy exposure | Backend accepts public direct connections | Firewall restrictions and authenticated proxy forwarding |
| World incompatibility | Old server opens migrated world | Version metadata, pre-migration backup, compatibility gates |
| State corruption | Controller crashes mid-operation | Durable operation records, recovery markers, reconciliation |
| Replay/duplicate action | Repeated create/destroy request | Operation IDs, idempotency keys, state validation |
| Path traversal | Restore archive writes outside target | Safe extraction, canonical path validation, symlink controls |
| Privacy misuse | Player telemetry sent without consent | Disabled-by-default, dual consent, central enforcement |

## Least privilege

Separate credentials by purpose and environment:
- Production runtime: only permissions needed for its own backup lifecycle.
- Staging restore: read-only access to selected production backup objects.
- Staging runtime: write access only to staging-owned objects.
- Controller: only provider/storage actions required by enabled features.
- Release reader: read-only access to approved release artifacts.
- Bootstrap execution: only the OS-level privileges needed to prepare its own instance; no provider-wide API credentials or production storage credentials by default.

Avoid shared administrator keys. Do not give a disposable game VM broad control over the hosting account or unrelated storage buckets. Prefer scoped and short-lived credentials where supported.

## Destructive operation safeguards

Destruction is high risk:
- Identify exact logical server, environment, provider, and instance reference.
- Display a human-readable plan.
- Require explicit approval according to environment policy.
- Re-check target identity immediately before execution.
- Require a recent, complete, integrity-verified backup associated with the target.
- Confirm no conflicting restore, migration, or operation exists.
- Record approval, preconditions, and outcome.
- Stop if any precondition is unknown or fails.

A generic `--yes` flag must not bypass production safeguards. Plan/dry-run must be read-only.

## Network and game-server security

For the initial Minecraft deployment:
- Backend game ports should not be generally reachable from the public internet when players are expected to enter through Velocity.
- Preserve authenticated Velocity forwarding and protect its forwarding secret.
- Restrict RCON/administrative interfaces to localhost or explicitly trusted networks.
- Keep management ports separate from player-facing ports.
- Validate firewall changes before applying them to a live server.
- Never log RCON passwords, forwarding secrets, or player authentication tokens.

The controller must not modify existing firewall rules during Phase 1.

## Bootstrap and software supply chain

- Pin or record the exact bootstrap source revision, configuration inputs, package versions, and downloaded software checksums.
- Keep bootstrap scripts and game software releases separately versioned.
- Provision from a provider-supported standard OS offering, then run reviewed bootstrap scripts; do not depend on project-maintained preinstalled machine images.
- Treat bootstrap scripts as privileged executable code. Review changes, verify source integrity, minimize embedded assumptions, and validate changes on fresh isolated instances.
- Keep previous approved bootstrap revisions and software-release references available for controlled rollback/reprovisioning.
- Do not promote a candidate based only on process startup.
- Review third-party adapters/plugins as executable code with trust boundaries.

## Privacy and telemetry

Optional telemetry is disabled by default. Optional identifiable player telemetry requires:
1. Explicit server-owner opt-in.
2. Separate informed opt-in by each affected player.
3. Disclosure of fields, purpose, destination, retention, and withdrawal.
4. Central enforcement across adapters and integrations.
5. No loss of ordinary gameplay access for declining optional telemetry.

Do not collect passwords, API keys, access tokens, private keys, or private chat content for general analytics. Do not sell data, use it for advertising, or repurpose it for unrelated profiling.

Necessary local operational/security logs must be minimized, access-controlled, documented, and retained only as long as needed. Any processing of identifiable player information outside the optional telemetry consent model requires separate legal/privacy review before implementation and must not be relabeled as telemetry to evade consent.

## Audit and incident response

Record security-relevant actions: authorization decisions, approvals, secret-reference resolution failures, adapter changes, backup verification, restore, release promotion, and destructive operations.

Audit records avoid secret values and unnecessary player data. Define retention, access, integrity protection, and export before production rollout.

Suspected vulnerabilities follow the private reporting process in [SECURITY.md](../SECURITY.md). Do not publish exploit details or credentials in public issues.

## Phase 1 security acceptance criteria

- Core policy tested independently of adapters.
- Fake adapters cannot bypass authorization or environment boundaries.
- Failed backup verification blocks destruction.
- Staging cannot write/delete production backup objects.
- Secret values are absent from logs and errors.
- Restore rejects path traversal and unsafe archive entries.
- Unknown capabilities fail closed.
- Tests use no real production credentials or resources.
- No optional telemetry is active or transmitted.
