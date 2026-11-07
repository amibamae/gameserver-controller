# ADR 0003: Execution-Agnostic, VM-First Reference Architecture

- **Status:** Accepted
- **Date:** 2026-11-05
- **Decision owners:** Project maintainers

## Context

Small deployments need ordinary virtual machines and service managers. The architecture should also leave room for containers and cluster schedulers. Requiring Kubernetes would impose extra infrastructure on operators who run only a few persistent game servers.

The reference deployment uses provider-created Debian VMs, versioned bootstrap scripts, Paper managed by systemd, Wasabi-compatible object storage, and Velocity.

## Decision

The protocol MUST remain execution-agnostic. It MUST NOT require Kubernetes, Docker, or containers.

The first reference execution adapter is execution.systemd on a conventional Linux VM. Future adapters MAY support containers, Kubernetes, or other execution and orchestration systems.

Keep these responsibilities separate:

1. **Hosting adapter:** creates and manages the infrastructure instance.
2. **Execution adapter:** creates, starts, inspects, and stops a workload on an allocated environment.
3. **Game adapter:** installs and configures game software, then defines its health, save, backup, and compatibility behavior.
4. **Storage adapter:** stores and verifies backup and release objects.
5. **Integration adapter:** connects proxies and other external services.

A logical server and its persistent data MUST survive replacement of an infrastructure instance. Execution technology MUST NOT define lifecycle or backup policy.

## Why

- Small deployments do not need cluster infrastructure.
- Debian and systemd remain first-class.
- Larger operators can connect their existing schedulers through adapters.
- Provider, storage, game, and execution choices stay independent.
- Lifecycle rules do not become tied to Pods, Deployments, containers, or one scheduler.

## Consequences

- Execution has its own protocol contract.
- The system keeps logical lifecycle, provider instance, execution unit, and operation status distinct.
- The first complete implementation should use a conventional VM and systemd.
- Kubernetes remains optional and must pass the same adapter conformance requirements.
- The initial project does not need to build a fleet scheduler.

## When to reconsider

We should consider a mandatory cluster dependency only if the adapter model cannot meet a protocol requirement or real deployments show the simpler model is inadequate. That change requires a new reviewed architecture decision.