# Contributing

Thank you for your interest in contributing! This project welcomes contributions from everyone, regardless of experience level. We want people to report bugs, propose features, improve documentation and tests, and develop game, hosting-provider, and storage-provider adapters.

**Open participation does not mean automatic acceptance.** The project maintainers are responsible for protecting the security, reliability, and integrity of the official project.

## Ways to contribute

- Report bugs and propose improvements.
- Submit fixes, tests, documentation, and features.
- Develop adapters and integrations.
- Review proposals and improve project documentation.

For substantial changes, open an issue or discussion first so the design can be considered before significant work begins.

## Pull request process

All changes to the official repository must be submitted through a pull request. Contributors should:

1. Explain what the change does and why it is useful.
2. Identify affected components, compatibility implications, and possible side effects.
3. Include tests or explain why tests are not applicable.
4. Update documentation and examples when behavior or configuration changes.
5. Disclose new dependencies, their purpose, and their licenses.
6. Confirm they have the right to submit the contribution under the project's licensing terms.

Do not include credentials, private keys, access tokens, production data, player data, or private infrastructure details in commits, issues, pull requests, logs, or test fixtures.

No contributor may merge their own pull request into the official project. Maintainers may request changes, reject a contribution, or close a pull request. Passing automated checks does not guarantee acceptance.

## Security-sensitive changes

Additional scrutiny is required for changes involving:

- Hosting-provider authentication and API access.
- Storage-provider authentication, bucket permissions, and object deletion.
- Provisioning, shutdown, destruction, and lifecycle operations.
- SSH access, firewall rules, operating-system permissions, and privilege boundaries.
- Backup, restore, synchronization, and data retention.
- Release management, software updates, deployment, and rollback.
- Telemetry, logging, consent, and personal information.
- Plugin, adapter, extension, and third-party code execution.

Security-sensitive changes must explain their threat model, permissions, failure behavior, and tests. Prefer least privilege and fail-safe behavior. Destructive actions must include safeguards against accidental data loss.

Contributors must not introduce backdoors, hidden behavior, credential exfiltration, undocumented network connections, intentionally harmful functionality, or changes designed to weaken security or reliability.

## Testing and dependencies

Contributors should provide relevant unit, integration, and security tests. Tests involving infrastructure must use isolated test resources and must never target production systems or production data.

New dependencies must be justified and reviewed for maintenance health, security, licensing, and necessity. Avoid uncontrolled downloads and unpinned executable artifacts.

## Telemetry and privacy

Telemetry is subject to the requirements in [SECURITY.md](SECURITY.md) and the project's telemetry/privacy design. In particular:

- Optional telemetry is disabled by default and requires explicit server-owner opt-in.
- Telemetry involving identifiable player information requires both server-owner consent and the individual player's opt-in.
- A server owner's consent does not enroll players.
- Contributors must document every collected field, purpose, destination, retention period, access controls, and consent behavior.
- Consent must be enforced centrally and cannot be bypassed by an adapter, plugin, or integration.
- Never collect passwords, API keys, authentication tokens, or private credentials.
- Do not collect private player messages for general analytics.

Necessary local operational or security records are distinct from optional telemetry. Any collection of player-identifying information for security monitoring must follow the project's consent requirements and applicable law.

## Production safety

The software may control real servers and valuable player data. Changes must be designed to preserve data and contain failures:

- Verify backups before destroying infrastructure that contains data requiring preservation.
- Do not allow staging to write to production backup locations.
- Do not promote an update solely because a process started; validate required services and health checks.
- Failed updates must not silently replace a working production release.
- Keep credentials outside source control and out of logs.
- Use least-privilege credentials and explicit permission boundaries.

## Security vulnerability reports

Do not publish details of an exploitable vulnerability in a public issue before maintainers have had an opportunity to investigate. Use the repository's private vulnerability reporting feature when available. See [SECURITY.md](SECURITY.md).

Do not test vulnerabilities against systems you do not own or have explicit permission to test.

## License and contribution terms

The project is licensed under **GNU Affero General Public License version 3.0 only (AGPL-3.0-only)**. By submitting a contribution, you confirm that you have the right to submit it and agree that the contribution may be distributed under that license. You retain ownership of your contribution; no copyright assignment is requested by this policy.

If you are unsure whether a contribution's license is compatible, disclose its origin and license before submitting it. Do not copy code from sources whose licensing is unknown or incompatible.

## Maintainer authority

Maintainers control merging, releases, repository permissions, security requirements, and project direction. They may revert changes that introduce security, reliability, licensing, or compatibility problems.

We aim to be welcoming and community-driven while maintaining high standards for the official project. **Contributions are welcome; the security and integrity of the project are not negotiable.**
