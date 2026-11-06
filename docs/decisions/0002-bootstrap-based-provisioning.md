# 0002 — Bootstrap-based instance provisioning

- **Status:** Accepted
- **Date:** 2026-11-06
- **Decision owners:** Project maintainers

## Context

The project considered deploying new instances from project-built, preinstalled disk images or custom installation ISOs. That approach would introduce image build, upload, storage, versioning, promotion, and provider-specific lifecycle concerns.

The initial reference deployment already uses a repeatable bootstrap process to prepare standard Debian instances. Persistent game data is kept separately in object storage and restored after instance provisioning.

The project should publish reusable source code and let each operator customize and run it in their own infrastructure account. It should not require operators to use a machine image created or distributed by the project.

## Decision

The default provisioning workflow uses a standard operating-system offering supplied by the hosting provider, followed by project-provided, operator-customizable bootstrap scripts.

The controller and protocol should model:

1. Selection of a provider-supported operating-system distribution/version.
2. Instance creation with the requested region, plan, networking, and access references.
3. Application of an explicitly selected and versioned bootstrap revision plus operator configuration.
4. Validation of bootstrap completion and resulting host readiness.
5. Secure, separately authorized delivery of credentials only to the operation steps that require them.
6. Restore of the selected verified game-data backup from persistent storage.
7. Game startup and game-specific readiness validation.

Bootstrap source, configuration inputs, and execution results should be recorded so an operator can identify exactly how an instance was prepared and reproduce it later.

The public repository distributes source code, templates, protocol specifications, tests, and documentation. Each operator provisions standard provider OS instances and runs a customized copy or release of the bootstrap scripts in their own account.

Project-maintained preinstalled machine images, snapshot-based default deployments, custom ISO creation, and image promotion are not part of the default architecture.

## Rationale

- Avoids recurring storage costs and provider-specific snapshot/image management.
- Avoids assuming that snapshot restoration is faster than ordinary instance provisioning.
- Keeps provisioning behavior visible, reviewable, and customizable as source code.
- Makes the deployment protocol independent of provider-specific custom-image features.
- Preserves a clean boundary between replaceable compute and persistent world data.
- Lets operators control their own OS selection, package choices, security policy, and bootstrap configuration.

## Consequences

- Hosting adapters must model standard OS offerings and provider-specific OS identifiers as instance-creation inputs.
- The protocol must represent bootstrap references/revisions, configuration inputs, execution state, and validation results.
- Bootstrap scripts are privileged code and require review, integrity checks, isolated testing, and clear versioning.
- Bootstrap scripts must not contain secret values. Credentials are injected or resolved separately with least-privilege scope.
- New bootstrap revisions should be tested against fresh instances created from supported standard OS offerings before being recommended for production.
- Persistent worlds, backups, and release artifacts remain separate from the instance bootstrap.
- Rollback to an earlier host-preparation baseline means reprovisioning with a previously approved bootstrap revision; it does not mean reverting a custom machine image.

## Reconsideration criteria

Custom preinstalled images or ISO-based installation workflows may be considered as optional provider-specific extensions only if measured deployment tests demonstrate a meaningful operational benefit or a concrete operator requirement justifies them. They must not become a required project artifact or default deployment dependency without a separate reviewed decision.
