# ADR 0001: Initial Implementation Language and Runtime

- **Status:** Proposed / decision pending
- **Date:** 2026-11-05
- **Decision owners:** Project maintainers

## Context

The controller needs a maintainable CLI and orchestration runtime with:
- Testable contracts between core and adapters.
- Strong validation for configuration and operation records.
- Reliable asynchronous provider operations, retries, and timeouts.
- HTTP/API client support for hosting and object-storage providers.
- Safe process, filesystem, and system-service integration on Linux.
- A practical path for community adapter contributions.
- Installation and upgrades on small Debian-based controller hosts.

Existing operational scripts are primarily shell-based. Shell remains useful for bootstrap and system tasks, but alone is not an ideal long-term foundation for provider-independent orchestration, durable state, structured errors, concurrency control, and contract testing.

## Options to evaluate

### Go
Potential strengths:
- Static typing and interfaces.
- Concurrency and cancellation primitives.
- Convenient single-binary Linux deployment.
- Strong fit for CLI/controller software and HTTP integrations.

Potential trade-offs:
- Contributors need Go familiarity.
- Some provider/game SDKs may be more mature in other ecosystems.

### Python
Potential strengths:
- Accessible contributor ecosystem.
- Mature HTTP, cloud, YAML/JSON validation, and test tooling.
- Convenient integration with existing operational scripts.

Potential trade-offs:
- Runtime and dependency packaging need deliberate management.
- Type checking and interface discipline require project conventions.
- Distribution may require a virtual environment or packaged runtime.

### Other languages
Rust, Java, and other options remain possible if maintainability, contributor access, deployment, and adapter ecosystem justify them.

## Decision

No implementation language is selected in this ADR. Phase 1 contracts and schemas remain language-neutral until maintainers explicitly choose a runtime.

## Decision criteria

Evaluate candidates against:
1. Contract/type safety and testability.
2. CLI and long-running orchestration support.
3. Linux process/filesystem/systemd integration.
4. HTTP, cloud-provider, and S3-compatible storage ecosystem.
5. Packaging and upgrades on Debian.
6. Community contributor accessibility.
7. Operational reliability and maintainability for a small project.

## Consequences

- No language-specific source tree or runtime dependency is introduced by Phase 1 documentation.
- Adapter contracts must be implementable in the selected language without changing core semantics.
- A later ADR must record the selected language, minimum supported version, dependency policy, packaging model, and supported Debian versions.
