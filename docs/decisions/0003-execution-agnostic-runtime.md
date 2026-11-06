# 0003 — Execution-agnostic, VM-first reference architecture

- **Status:** Accepted
- **Date:** 2026-11-05
- **Decision owners:** Project maintainers

## Context

The project needs to support small deployments using ordinary virtual machines and service managers, while remaining extensible to container and cluster-based execution platforms. Making Kubernetes or Agones mandatory would impose additional infrastructure and operating assumptions on operators who only need a small number of persistent game servers.

The initial reference deployment uses provider-created Debian VMs, versioned bootstrap scripts, systemd-managed Paper, Wasabi-compatible object storage, and Velocity.

## Decision

The protocol MUST be execution-agnostic and MUST NOT require Kubernetes, Docker, containers, or Agones.

The initial reference execution adapter is `execution.systemd` on a conventional Linux VM. Optional future adapters MAY support container runtimes, Kubernetes, Agones, or other execution/orchestration systems.

The architecture separates:
1. Hosting adapter: creates and manages the underlying infrastructure instance.
2. Execution adapter: creates, starts, inspects, and stops an execution unit on an execution environment.
3. Game adapter: installs/configures game software and defines game-specific health, save, backup, and compatibility semantics.
4. Storage adapter: persists and verifies backup/release objects.
5. Integration adapter: coordinates proxies and other external systems.

A logical server identity and its persistent data MUST survive replacement of an infrastructure instance. Execution technology MUST NOT define the protocol's lifecycle or backup policy.

## Rationale

- Avoids making cluster infrastructure a prerequisite for small deployments.
- Keeps ordinary Debian/systemd deployments first-class.
- Allows larger operators to integrate mature schedulers rather than requiring a custom scheduler.
- Preserves provider, storage, game, and execution independence.
- Avoids coupling lifecycle semantics to Pods, Deployments, containers, or a particular orchestration engine.

## Consequences

- An execution adapter contract is part of the protocol.
- Lifecycle state is distinct from provider instance state, execution-unit state, and operation status.
- The first end-to-end reference implementation should use a conventional VM and systemd.
- Agones and Kubernetes integrations are optional future work and must pass the same adapter conformance requirements.
- The project will not implement a fleet scheduler as an initial requirement.

## Reconsideration criteria

A mandatory cluster/orchestration dependency may be reconsidered only if a protocol requirement cannot reasonably be met through the adapter model or real deployments demonstrate that the simpler execution model is inadequate. Such a change requires a new reviewed architecture decision.
