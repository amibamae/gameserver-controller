# ADR 0001: Initial Implementation Language and Runtime

- **Status:** Accepted
- **Date:** 2026-11-06
- **Decision owners:** Project maintainers

## Decision

Use Go for the initial controller, command-line tools, and protocol validation tooling. The repository's Go module currently targets Go 1.21.

Keep protocol meaning defined by the published contracts, schemas, and conformance fixtures. Community members may contribute adapters, clients, or implementations in other languages. A second-language implementation is not required for project releases.

## Context

The project needs a maintainable CLI and orchestration runtime with strict validation, asynchronous provider calls, bounded retries and timeouts, HTTP clients, and safe Linux process, filesystem, and systemd integration. Go provides static types, concurrency and cancellation tools, and single-binary deployment.

Existing shell scripts remain appropriate for bootstrap and host tasks. They are not the long-term home for provider-independent orchestration, durable state, structured errors, or contract tests.

## Consequences

- New controller and tooling code should use Go unless a later accepted ADR changes this decision.
- Adapters and clients can use other languages when contributors choose to build them.
- The project will continue to publish schemas and fixtures that describe protocol behavior without relying on Go-specific types.
- Cross-language implementations are welcome but do not gate a release.
- A later ADR may revise the Go version, dependency policy, packaging model, or supported Debian releases.
