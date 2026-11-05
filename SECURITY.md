# Security and Privacy Policy

Security and privacy are core project requirements. This document describes vulnerability reporting expectations and the rules governing security logs and telemetry.

## Reporting a vulnerability

Please do not disclose exploitable vulnerability details in a public issue before maintainers have had a reasonable opportunity to investigate.

Use GitHub's **Report a vulnerability** feature in the repository's Security tab when it is enabled. If private vulnerability reporting is not available, contact the repository owner through their GitHub profile and request a private reporting channel. Do not include exploit details in a public issue.

Reports are welcome for vulnerabilities involving authentication, authorization, credential handling, command execution, provider APIs, storage access, backups, restores, destructive lifecycle operations, update mechanisms, telemetry, and privacy.

Only test systems you own or have explicit permission to test. Do not access, modify, or delete another person's infrastructure or data.

## Security design principles

- Least privilege for provider, storage, operating-system, and game-server permissions.
- Credentials must be kept out of source control, logs, telemetry, and test fixtures.
- Destructive operations must be guarded and should fail safely.
- Verify backups and required synchronization before destroying infrastructure.
- Staging environments must be isolated from production write access.
- Extensions and adapters must use documented, narrowly scoped interfaces.
- Security-sensitive changes require maintainer review and relevant tests.

## Telemetry and consent

### Default: disabled

Optional telemetry must be disabled by default. Installation, upgrades, updates, or configuration migrations must not silently enable it. Telemetry must not be transmitted externally unless the server administrator has explicitly enabled the relevant telemetry category and destination.

### Two levels of consent

Telemetry involving identifiable player information requires **both**:

1. Explicit opt-in by the server owner or administrator; and
2. Separate, informed opt-in by each affected player.

Server-owner consent does not enroll players. If either consent is absent or withdrawn, the system must not collect or transmit the optional identifiable player telemetry for that person.

Players who decline optional telemetry should retain ordinary gameplay access. A separate, narrowly tailored security control may be necessary to protect the service, but it must not be used as a pretext to bypass optional telemetry consent.

### Data minimization and prohibited data

Permitted security or operational telemetry may include carefully limited events such as service failures, suspicious authentication attempts, firewall events, backup failures, and unauthorized access attempts.

Collection must be limited to fields reasonably necessary for the documented purpose. Player usernames, UUIDs, IP addresses, connection histories, gameplay activity, and player-specific security events are player-related information and require the two levels of consent above when collected as optional telemetry.

Never collect passwords, API keys, access tokens, private keys, or other authentication secrets. Do not collect private player messages or chat content for general analytics. Telemetry must not be used for advertising, sale of personal data, unrelated profiling, or unrelated commercial purposes.

### Transparency and controls

Every telemetry feature must document:

- Its purpose and the security or operational need.
- Every data field collected and whether it can identify a person.
- Whether collection is local or transmitted externally.
- Destination, transport security, and recipients.
- Retention period, deletion behavior, and access controls.
- How server-owner and player consent are obtained, reviewed, and withdrawn.
- What functionality remains when telemetry is disabled.

External transmission must use encrypted transport. Stored logs must have appropriate access controls, integrity protections, and retention/rotation limits.

### Local operational and security records

Necessary local records used to operate and protect the service are distinct from optional telemetry. Examples include controller startup failures, backup/restore outcomes, provisioning events, and infrastructure authentication failures. These records must still follow data minimization, access control, and retention requirements.

Any telemetry or optional monitoring record that includes identifiable player information requires both server-owner opt-in and separate opt-in from each affected player. There is no general security-monitoring exemption from this consent rule. If a security function genuinely requires processing player-identifying information without consent, it must be designed and documented as a separate necessary service-security operation, not optional telemetry, and reviewed for necessity, proportionality, and applicable legal requirements. It must not be repurposed for analytics or external telemetry.

### Central enforcement

Consent must be enforced by a centralized project component or policy interface. Individual games, hosting providers, storage providers, plugins, and integrations must not create alternate paths that bypass consent decisions.

Contributors adding or changing telemetry must include tests proving that collection and external transmission do not occur when the required consent is absent or withdrawn.

## Incident response

Maintainers should assess reports promptly, determine affected versions and components, coordinate a fix, and communicate mitigations and release information when appropriate. Public disclosure should be coordinated to reduce harm to users.
