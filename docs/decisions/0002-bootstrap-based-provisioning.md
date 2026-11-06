# ADR 0002: Bootstrap-Based Instance Provisioning

- **Status:** Accepted
- **Date:** 2026-11-06
- **Decision owners:** Project maintainers

## Context

We considered distributing preinstalled base-system images or custom installation media. That would add a second artifact lifecycle: build, upload, store, version, promote, and maintain provider-specific host images.

The reference deployment already prepares standard Debian instances with repeatable bootstrap scripts. Minecraft worlds live in object storage and return after the instance is provisioned.

I want operators to use source they can inspect and adapt in their own provider accounts. They should not depend on a base image built or hosted by this project.

## Decision

The default workflow starts with an operating-system offering supplied by the hosting provider, then applies project bootstrap scripts that each operator can customize.

The controller and protocol will model:

1. The provider-supported operating system and version.
2. Instance creation, including region, plan, networking, and access references.
3. An explicitly chosen, versioned bootstrap revision and operator configuration.
4. Bootstrap completion and host-readiness checks.
5. Secure, separately authorized delivery of credentials only to steps that need them.
6. Restore of one selected, verified game-data backup.
7. Game startup and game-specific readiness checks.

Record the bootstrap source, inputs, and execution result so you can reproduce how an instance was prepared.

The public repository distributes source, templates, protocol specifications, tests, and documentation. Each operator provisions a standard OS instance and runs a customized copy or release of the scripts in their own account.

Project-maintained preinstalled images, custom installation media, and provider-specific image-promotion pipelines are outside the default architecture.

## Why this fits

- Avoids ongoing storage and provider-specific image management.
- Does not assume image restoration is faster than ordinary VM provisioning.
- Keeps host preparation visible, reviewable, and customizable.
- Keeps the protocol independent of provider-specific preinstalled-image features.
- Separates replaceable compute from persistent world data.
- Lets operators choose the OS, packages, security policy, and bootstrap settings.

## Consequences

- Hosting adapters must model standard OS offerings and provider-specific identifiers.
- The protocol must represent bootstrap revisions, configuration inputs, execution state, and validation results.
- Bootstrap scripts run with elevated privileges, so review them, verify their integrity, test them on isolated instances, and version them clearly.
- Never put secret values in bootstrap scripts. Resolve credentials separately with least-privilege scope.
- Test new bootstrap revisions on fresh instances before recommending them for production.
- Keep worlds, backups, and release artifacts separate from host bootstrap.
- To return to an earlier host baseline, reprovision with a previously approved bootstrap revision.

## When to reconsider

We may consider preinstalled images or custom installation media as optional provider-specific extensions if measured tests show a meaningful benefit or an operator has a concrete need. They must not become a required artifact or default dependency without a separate reviewed decision.