# Security Model and Threat Assessment

**Status:** Initial baseline for design review. This is not a security certification or a complete operational assessment.

## Assets

Protect:

- Production worlds and backups.
- Provider tokens, storage credentials, SSH keys, and proxy secrets.
- Player identifiers and personal information.
- Server availability and integrity.
- Approved bootstrap revisions, OS selections, and software releases.
- Controller state, operation history, approvals, and audit records.
- Production network boundaries and backend access restrictions.

## Trust boundaries

The design crosses these boundaries:

1. Operator to controller: commands, settings, approvals, credentials.
2. Core to adapters: policy and adapter execution.
3. Controller to provider APIs: cloud infrastructure.
4. Controller to object storage: backups, releases, and manifests.
5. Controller to game runtime: processes, files, console/RCON, and health checks.
6. Controller to proxy: player routing and backend registration.
7. Production to staging: copied world data and read-only access.
8. Public source to bootstrap execution: reviewed code, artifact origin, and privileged scripts.
9. Deployment to optional telemetry destination: separately consented data leaving the server.

Do not trust an adapter, plugin, download, provider response, or game file just because the controller uses it.

## Threats and controls

| Threat | Example | Required control |
|---|---|---|
| Credential leak | Token appears in logs or a commit | Secret references, redaction, least privilege, rotation |
| Wrong-instance deletion | Operator selects the wrong VM | Exact target, environment binding, approval, verified backup |
| Lost world data | VM is destroyed after upload failure | Fail closed; verify backup before destroy |
| Staging writes to production | Test world overwrites live data | Separate credentials and storage boundaries |
| Malicious adapter | Adapter exfiltrates credentials | Narrow permissions, review, restricted secret access, audit |
| Compromised supply chain | Malicious plugin or binary | Provenance, checksums, pinned versions, review, isolated tests |
| Exposed backend | Players bypass the proxy | Firewall restrictions and authenticated forwarding |
| Incompatible world | Old server opens migrated data | Version metadata, pre-migration backup, compatibility gates |
| Crashed controller | Operation stops halfway | Durable operation record, recovery markers, reconciliation |
| Replayed request | Duplicate create or destroy | Operation IDs, idempotency, state validation |
| Unsafe extraction | Archive writes outside target | Safe extraction, canonical paths, symlink checks |
| Privacy misuse | Player data sent without consent | Disabled by default, separate consent, central enforcement |

## Least privilege

Separate credentials by purpose and environment:

- Production runtime: only permissions for its own backup lifecycle.
- Staging restore: read-only access to selected production backup objects.
- Staging runtime: write only to staging-owned objects.
- Controller: provider and storage actions required by enabled features.
- Release reader: read-only access to approved release artifacts.
- Bootstrap: OS privileges needed for its instance, but no provider-wide API or production-storage credentials by default.

Avoid shared administrator keys. A disposable game VM should not control the hosting account or unrelated storage buckets. Use scoped, short-lived credentials where supported.

## Destructive operations

Before destroying anything, the controller must:

- Identify the logical server, environment, provider, and exact instance.
- Show the operator a readable plan.
- Require approval according to environment policy.
- Recheck the target immediately before the call.
- Confirm a recent, complete, integrity-verified backup for that target.
- Confirm no restore, migration, or conflicting operation is in progress.
- Record approval, preconditions, and result.
- Stop if a precondition is unknown or fails.

A generic yes flag must not bypass production safeguards. Plan and dry-run modes must not write.

## Network and game-server boundaries

For the initial Minecraft deployment:

- Keep the backend game port off the public internet when players should enter through Velocity.
- Use authenticated Velocity forwarding and protect the shared secret.
- Restrict RCON and admin interfaces to localhost or trusted networks.
- Separate management ports from player-facing ports.
- Validate firewall changes before applying them to a live host.
- Never log RCON passwords, forwarding secrets, or player authentication tokens.

Phase 1 does not modify firewall rules.

## Bootstrap and software supply chain

- Record the bootstrap revision, configuration, package versions, and downloaded checksums.
- Version bootstrap scripts separately from game releases.
- Start from a provider-supported OS offering, then apply reviewed, versioned bootstrap scripts.
- Treat bootstrap as privileged code. Review it, verify source integrity, minimize hidden assumptions, and test it on fresh isolated instances.
- Retain previously approved bootstrap and release references for controlled rollback.
- Do not promote a candidate just because a process started.
- Review adapters and plugins as executable code across a trust boundary.

## Privacy and telemetry

Optional telemetry starts disabled. If optional telemetry identifies a player, require:

1. Explicit server-owner opt-in.
2. Separate informed opt-in from each affected player.
3. Disclosure of fields, purpose, destination, retention, and withdrawal.
4. Central enforcement across adapters and integrations.
5. Ordinary gameplay access for players who decline.

Never collect passwords, API keys, access tokens, private keys, or private chat for general analytics. Do not sell telemetry, use it for advertising, or repurpose it for unrelated profiling.

Minimize and restrict necessary local security logs. Before processing player-identifying information outside the optional telemetry consent model, conduct a separate legal and privacy review. Do not relabel that processing as telemetry to avoid consent.

## Audit and incident response

Record authorization decisions, approvals, secret-reference failures, adapter changes, backup verification, restores, release promotions, and destructive actions.

Keep secrets and unnecessary player data out of audit records. Define retention, access, integrity, and export before production. Report suspected vulnerabilities through [SECURITY.md](../SECURITY.md); do not post exploit details or credentials publicly.

## Phase 1 security checks

Before Phase 1 is complete:

- Test core policy independently from adapters.
- Verify fake adapters cannot bypass authorization or environment boundaries.
- Confirm failed backup verification blocks destruction.
- Confirm staging cannot write to or delete production backups.
- Confirm logs and errors contain no secret values.
- Reject unsafe restore paths and archive links.
- Fail closed on unknown capabilities.
- Use no production credentials or resources in tests.
- Keep optional telemetry inactive and silent.