# ADR 0001: Initial Implementation Language and Runtime

- **Status:** Accepted
- **Date:** 2026-11-06
- **Decision owners:** Project maintainers

## Decision

Use Go for the first controller, command-line tools, and protocol validation. The module targets Go 1.23 because the initial durable operation store uses bbolt v1.4.3.

The published contracts, schemas, and conformance fixtures define protocol meaning. Contributors may build adapters, clients, or other implementations in any language. A second-language implementation is not a release requirement.

## Context

The project needs a maintainable CLI and orchestration runtime with strict validation, asynchronous provider calls, bounded retries and timeouts, HTTP clients, and safe Linux process, filesystem, and systemd integration. Go gives the first implementation static types, concurrency and cancellation tools, and a single-binary deployment.

Shell remains useful for bootstrap and host tasks. I don’t want provider-independent orchestration, durable state, structured errors, or contract tests to live in a growing pile of shell scripts.

## Consequences

- Write new controller and tooling code in Go unless a later accepted ADR changes this decision.
- Contributors may use other languages for adapters and clients.
- Keep schemas and fixtures language-neutral; protocol behavior must not depend on Go types.
- Other-language implementations are welcome, but they do not gate a release.
- A later ADR may revise the Go version, dependency policy, packaging model, or supported Debian releases.