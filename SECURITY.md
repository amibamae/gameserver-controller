# Security and Privacy

Security and privacy are requirements for this project. Use this page to report a vulnerability and to understand the rules for security records and optional telemetry.

## Report a vulnerability

Please give maintainers a reasonable chance to investigate before you disclose exploitable details.

Use GitHub’s **Report a vulnerability** option on the repository’s Security tab when it is available. If private reporting is unavailable, contact the repository owner through their GitHub profile and ask for a private channel. Do not include exploit details in a public issue.

Reports are welcome for problems with authentication, authorization, credentials, command execution, provider APIs, storage, backups, restores, destructive lifecycle actions, updates, telemetry, or privacy.

Test only systems you own or have explicit permission to test. Do not access, change, or delete another person’s infrastructure or data.

## Security design rules

- Give each provider, storage service, operating-system process, and game component only the permissions it needs.
- Keep credentials out of source control, logs, telemetry, and test fixtures.
- Guard destructive operations and make them fail safely.
- Verify backups and required synchronization before destroying infrastructure.
- Isolate staging from production write access.
- Keep extensions and adapters within documented, narrowly scoped interfaces.
- Require maintainer review and relevant tests for security-sensitive changes.

## Optional telemetry

### Off by default

Optional telemetry starts disabled. Installers, upgrades, and migrations must not turn it on silently. The system must not send optional telemetry externally unless the administrator explicitly enables that category and destination.

### Two separate consents

Optional telemetry that identifies a player requires both:

1. Explicit opt-in from the server owner or administrator.
2. Separate, informed opt-in from each affected player.

The owner cannot opt in on a player’s behalf. If either consent is missing or withdrawn, the system must stop collecting and transmitting that player’s optional identifiable telemetry.

Players who decline optional telemetry keep ordinary gameplay access. A narrowly scoped service-security function may still be necessary, but it must not become a pretext for bypassing the optional telemetry rules.

### Minimize data

Security and operations telemetry may include limited events such as service failures, suspicious sign-in attempts, firewall events, backup failures, and unauthorized access attempts. Collect only the fields needed for the stated purpose.

Usernames, UUIDs, IP addresses, connection history, gameplay activity, and player-specific security events identify or relate to a player. If you collect these as optional telemetry, get the two separate consents above.

Never collect passwords, API keys, access tokens, private keys, or other authentication secrets. Do not use private player messages for general analytics. Do not use telemetry for advertising, selling personal data, or unrelated profiling.

### Explain each telemetry feature

Document:

- Why you collect the data and what need it serves.
- Every field collected and whether it can identify someone.
- Whether data stays local or leaves the server.
- The destination, transport protection, and recipients.
- Retention, deletion, and access controls.
- How the owner and players give, review, and withdraw consent.
- What still works when telemetry is off.

Encrypt external transmission. Protect stored logs from unauthorized access and modification. Set retention and rotation limits.

### Local operational and security records

Necessary local records used to run and protect the service differ from optional telemetry. Examples include controller startup failures, backup and restore results, provisioning events, and infrastructure authentication failures. Still minimize these records, limit access, and define retention.

If a record identifies a player and is collected as optional monitoring, both the owner and that player must opt in. There is no general security-monitoring exception to this rule. If a necessary service-security function genuinely must process identifying data without consent, design it as a separate, necessary security operation. Review its necessity, proportionality, and legal requirements before implementation. Do not relabel it as telemetry or reuse it for analytics.

### Enforce consent centrally

A central project component or policy interface must enforce consent for everything the controller collects or sends. Hosting and storage providers, integrations, adapters, and the controller's own plugins must not create a second path around that decision.

Games and game or proxy plugins that monitor players are allowed. Examples are anti-cheat, chat logging, moderation tools, and gameplay analytics. Their monitoring stays inside the game and is not controller telemetry, so the consent rule above applies to it only where data reaches the controller:

- **The controller does not collect it.** Adapters must not pass player monitoring data to the controller, its logs, its operation records, or its telemetry. A game's monitoring must not become a second path into them.
- **The server owner is responsible.** The owner chooses to run monitoring software and is responsible for meeting the privacy law that applies to them and their players.
- **Warn players where possible.** Players should be told before or when they join. Where the game or proxy can show a message, such as a join notice, server list description, or welcome message, the project should show a short warning. It names what is monitored, for example "This server uses anti-cheat and logs chat", and says where to read the owner's privacy notice. If the game or proxy offers no way to show a warning, the adapter's docs say so, so the owner can warn players another way.
- **Declare it.** A game adapter's docs must list each player-monitoring feature it installs or knows the release enables, so the owner can decide and write the warning.

The warning is information, not consent. It does not replace the separate opt-in that optional identifiable telemetry requires.

When you add or change telemetry, include tests showing that collection and external transmission stop when required consent is missing or withdrawn. When you add a game adapter, include a test that no player monitoring data reaches controller records.

## Security design principles

- Use least privilege for provider, storage, operating-system, and game-server access.
- Keep credentials out of source control, logs, telemetry, and fixtures.
- Verify a backup before destructive cleanup.
- Isolate staging from production writes.
- Review adapter and extension trust boundaries.
- Keep an audit trail for security-sensitive changes.

## Incident response

Maintainers should assess reports promptly, identify affected versions and components, coordinate a fix, and share mitigations or release details when appropriate. Coordinate public disclosure to reduce harm to users.