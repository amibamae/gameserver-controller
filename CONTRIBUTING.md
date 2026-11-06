# Contributing

I welcome bug reports, design criticism, documentation fixes, tests, features, and adapters for games, hosting providers, and storage providers. You do not need to be an expert, and you do not need to use AI.

Contributions still need review. The maintainers are responsible for the security, reliability, licensing, and integrity of the official project.

## Before you make a large change

For a substantial proposal, open an issue or discussion first. That gives us a chance to review the design before you invest time in an implementation.

For a pull request, explain:

1. What you changed and why it matters.
2. Which components it affects, including compatibility changes and side effects.
3. Which tests you added or ran, or why testing does not apply.
4. Which docs and examples changed with the behavior or configuration.
5. Any new dependency, its purpose, and its license.
6. That you have the right to contribute the work under this project’s license.

Submit all changes through a pull request. Do not merge your own pull request into the official project. Maintainers may request changes, decline a contribution, or close a pull request. A green CI run does not guarantee acceptance.

Never include credentials, private keys, access tokens, production data, player data, or private infrastructure details in commits, issues, pull requests, logs, or test fixtures.

## Security-sensitive changes

Give extra detail when your change touches:

- Provider authentication or API access.
- Storage credentials, bucket permissions, or object deletion.
- Provisioning, shutdown, destruction, or other lifecycle operations.
- SSH, firewall rules, operating-system permissions, or privilege boundaries.
- Backups, restores, synchronization, or retention.
- Releases, software updates, deployment, or rollback.
- Telemetry, logging, consent, or personal information.
- Plugins, adapters, extensions, or third-party code execution.

Explain the threat model, permissions, failure behavior, and tests. Prefer least privilege and safe failure. Guard destructive actions against accidental data loss.

Do not add backdoors, hidden behavior, credential exfiltration, undocumented network connections, harmful functionality, or changes that weaken security or reliability.

## Tests and dependencies

Add relevant unit, integration, and security tests. If a test uses infrastructure, isolate it. Never point a test at production systems or production data.

Explain why a dependency is needed. Review its maintenance, security, license, and whether the project can avoid it. Do not add uncontrolled downloads or unpinned executable artifacts.

## Privacy and telemetry

Follow the telemetry rules in [SECURITY.md](SECURITY.md). Optional telemetry must be off by default and requires explicit server-owner opt-in. If it identifies a player, that player must opt in separately. The owner’s choice does not enroll players.

For each telemetry field, document its purpose, destination, retention, access controls, and consent behavior. Enforce consent centrally. An adapter, plugin, or integration must not bypass it. Never collect passwords, API keys, tokens, or private credentials. Do not use private player messages for general analytics.

Necessary local records that help operate or secure a server are not automatically optional telemetry. If a local security record identifies a player, follow the project’s consent rules and applicable law.

## Production safety

The controller may eventually operate real servers and valuable player worlds. Keep those consequences in view:

- Verify the backup before destroying infrastructure that holds data.
- Prevent staging from writing to production backup locations.
- Validate required services and health checks before promoting an update. A process starting is not enough.
- Do not silently replace a working release after a failed update.
- Keep credentials out of source control and logs.
- Use credentials with the least authority needed.

## Report a vulnerability privately

Do not post an exploitable vulnerability in a public issue before maintainers can investigate. Use GitHub’s private vulnerability reporting feature when available. If it is unavailable, contact the repository owner privately. See [SECURITY.md](SECURITY.md).

Only test systems you own or have clear permission to test.

## License and ownership

The project uses GNU AGPL-3.0-only. By contributing, you confirm you have the right to submit your work and agree that it may be distributed under that license. You keep ownership of your contribution; this policy does not ask you to assign copyright.

If you are unsure whether a source’s license is compatible, disclose its origin and license before contributing. Do not copy code with unknown or incompatible terms.

## Maintainer responsibility

Maintainers decide what to merge, release, and support. They may revert work that creates security, reliability, licensing, or compatibility problems. We want a welcoming project, and we will protect the integrity of its official code.