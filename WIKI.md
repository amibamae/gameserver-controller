# Game Server Controller Wiki

> **Repository:** [amibamae/gameserver-controller](https://github.com/amibamae/gameserver-controller)  
> **Status:** In Development - Phase 1 Protocol Complete  
> **License:** GNU AGPL-3.0-only  

---

## Table of Contents

- [About This Project](#about-this-project)
- [Quick Start Guide](#quick-start-guide)
- [Architecture Overview](#architecture-overview)
- [Protocol Specification](#protocol-specification)
- [Implementation Status](#implementation-status)
- [Security Model](#security-model)
- [Configuration](#configuration)
- [Development Workflow](#development-workflow)
- [Contributing](#contributing)
- [Code of Conduct](#code-of-conduct)
- [Third-Party Notices](#third-party-notices)
- [Related Documentation](#related-documentation)

---

## About This Project

### What is Game Server Controller?

Game Server Controller (GSC) is a lifecycle controller for game servers, specifically designed to manage Minecraft Java servers running Paper. 

**Core Purpose:** As stated by the owner:

> Bring a backend online when players need it, restore the world, send players across only after the game is ready, and remove the VM only after a fresh backup has been verified.

### Reference Stack

The project uses a concrete reference deployment stack:

| Component | Provider/Technology |
|-----------|---------------------|
| Cloud Hosting | Vultr (disposable VMs) |
| Object Storage | Wasabi (S3-compatible backups) |
| Game Engine | Minecraft Java with Paper |
| Proxy | Velocity (player-facing proxy) |
| Operating System | Debian (via versioned bootstrap scripts) |

### Key Design Principles

1. **Provider-Agnostic Core:** Adding new cloud providers, storage backends, or games should work without rewriting the core lifecycle logic
2. **Language-Neutral Protocol:** The wire protocol is defined in JSON Schema; Go is just the first implementation
3. **Protocol-First:** The contract comes first; any language can implement adapters
4. **No Web Panel Required:** A browser UI is not needed for basic operation
5. **Independent Clients:** Third parties can build their own clients using the management API
6. **Core Authority:** Adapters cannot bypass authorization, lifecycle policy, or environment isolation
7. **Safe Failure:** Timeouts and interruptions are explicit; destruction requires verified backups
8. **Privacy by Default:** Telemetry is off by default; player-identifying data requires separate consent

---

## Quick Start Guide

### First-Time Setup

```bash
# 1. Install the controller setup tool
go install ./cmd/controller-setup

# 2. Validate your configuration
go run ./cmd/config-check validate configs/controller.example.yaml

# 3. Run the setup wizard on an existing Debian/systemd machine
controller-setup install --config configs/controller.example.yaml

# 4. Start the controller service
controller serve --config configs/controller.example.yaml
```

### Everyday Operations

For common operations like starting/stopping servers, see [Operator Operations](docs/OPERATOR_OPERATIONS.md).

### Testing Locally

Before pushing changes, run these pre-push checks:

```bash
go mod verify
gofmt -l $(git ls-files '*.go')
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build ./...
go run ./cmd/protocol-check
go run ./cmd/config-check validate configs/controller.example.yaml
```

---

## Architecture Overview

### High-Level Architecture

```
┌────────────────────────────────────────────────────────────┐
│                    Operator / Client                        │
│           (HTTP / JSON-RPC, OpenAPI 3.2.1)                  │
└───────────────────────────┬────────────────────────────────┘
                            │ management API
┌───────────────────────────▼────────────────────────────────┐
│                     Core Controller                        │
│  lifecycle rules · permissions · approvals · audit         │
│  durable operation store (bbolt) · workflow engine         │
│  staging / production boundary enforcement                 │
│                                                            │
│  ┌────────────────────────────────────────────────────┐    │
│  │        Adapter boundary (in-process API)           │    │
│  └────────────────────────────────────────────────────┘    │
└──────┬──────────┬──────────┬──────────┬──────────┬────────┘
       │          │          │          │          │
  hosting     storage   execution    game   integration
  (Vultr)    (Wasabi)   (Paper)    (Paper)  (Velocity/DNS)
```

### Core vs. Adapter Responsibilities

| Aspect | Core Controller | Adapter |
|--------|-----------------|---------|
| Makes safety decisions | **Yes** | No |
| Calls vendor APIs | **No** | Yes |
| Knows about "staging" | Yes, enforces boundary | Reports environment |
| Owns durable operations | Yes | No |
| Can retry on unclear outcomes | Yes (reconciliation) | Reports outcome |

### Five Adapter Categories

1. **Hosting:** Create/destroy/inspect VMs, attach storage, manage firewalls
   - Reference: Vultr
   
2. **Storage:** Upload/download/list/verify backups
   - Reference: Wasabi
   
3. **Execution:** Start/stop game processes, collect readiness signals
   - Reference: systemd on Linux
   
4. **Game:** Game-native save, install releases, restore backups
   - Reference: Minecraft Paper
   
5. **Integration:** Route player traffic, manage DNS handoff, monitoring
   - Reference: Velocity proxy, Cloudflare DNS

> Note: The split between **execution** (process management) and **game** (game semantics) allows supporting different game engines without touching the execution adapter.

---

## Protocol Specification

### Current Version

**Protocol Version:** `0.21.0-draft` (Phase 1 reviewed; stable 1.0 pending)

### Communication Standards

| Concern | Selection | RFC/Standard |
|---------|-----------|--------------|
| Interchange format | JSON | RFC 8259 |
| Structural schemas | JSON Schema | 2020-12 |
| Timestamps | ISO 8601 | RFC 3339 |
| Canonicalization | JCS digest | RFC 8785 |
| Adapter RPC | JSON-RPC | 2.0 |
| Management API | OpenAPI | 3.2.1 |

### Standard Operations (The Catalog)

The core understands these standard operations:

| Operation | Description |
|-----------|-------------|
| `server.define` | Declare a server definition |
| `server.start` | Provision, restore, and bring a server online |
| `server.stop` | Drain, save, back up, and tear down a server |
| `server.backup` | Take and verify a backup without stopping the server |
| `server.restore` | Restore a named backup |
| `server.destroy` | Permanently remove a server (gated on verified backup) |
| `server.status` | Query current lifecycle state |

### Durable Operation Model

Operations are durable, meaning:

1. The operation record is persisted to the operation store **before** any external side effect
2. Each operation has a unique ID and can be retried safely (idempotent)
3. Unclear outcomes are reconciled by inspecting actual resource state
4. Operations track progress through steps with timestamps and deadlines

---

## Implementation Status

### Complete Components ✅

- [x] Protocol schemas (`0.21.0-draft`) - Reviewed, Phase 1 complete
- [x] `cmd/protocol-check` conformance checker
- [x] bbolt operation store (`internal/store`)
- [x] In-process adapter API (`internal/adapter`)
- [x] Durable workflow engine (`internal/workflow`)
- [x] `server.define` operation
- [x] `server.stop` graceful workflow (with save and backup)
- [x] Vultr hosting adapter (`internal/vultrapi`) - Pilot scope
- [x] Wasabi storage adapter (`internal/wasabisto`) - Pilot scope
- [x] Supervised pilot (Vultr VM + Wasabi backup) - Tested

### In Progress 🚧

- [ ] Production `server.start` workflow (real adapters wired)
- [ ] `server.destroy` production workflow
- [ ] `server.backup` / `server.restore` standard workflows
- [ ] Scaling policy enforcement in the runtime loop

### Planned for Future Phases 🔮

| Feature | Phase |
|---------|-------|
| Release tooling and rollback | Phase 5 |
| Vertical auto-scaling | Phase 4+ |
| Staging deployments from production backups | Phase 6 |
| Automated updates with compatibility validation | Phase 6 |
| Additional hosting providers (e.g., OVH) | Phase 7 |
| Multi-language adapter implementations | Ongoing |

---

## Security Model

### Assets to Protect

1. Production worlds and backups
2. Provider tokens, storage credentials, SSH keys, proxy secrets
3. Player identifiers and personal information
4. Server availability and integrity
5. Approved bootstrap revisions, OS selections, and software releases
6. Controller state, operation history, approvals, and audit records

### Trust Boundaries

```
1. Operator → Controller (commands, settings, approvals, credentials)
2. Core ↔ Adapters (policy and adapter execution)
3. Controller → Provider APIs (cloud infrastructure)
4. Controller → Object Storage (backups, releases, manifests)
5. Controller → Game Runtime (processes, files, console/RCON)
6. Controller → Proxy (player routing, backend registration)
7. Production ↔ Staging (copied world data, read-only access)
8. Public Source → Bootstrap Execution (reviewed code)
9. Deployment → Telemetry Destination (separately consented data)
```

### Security Principles

1. **Least Privilege:** Each credential is scoped to only what it needs
2. **Safe Failure:** Fail toward preserving data when uncertain
3. **Approval Gates:** Destructive operations require explicit operator approval
4. **Audit Trail:** Every accepted operation is recorded durably
5. **Telemetry Opt-In:** Off by default; player-identifying data requires consent

### Credential Handling

- Never stored in configuration files, logs, or commits
- Use encrypted systemd credentials on Debian deployments
- Credentials loaded only when needed via bounded accessor
- Protocol uses secret references in messages, never serialized values

### Privacy and Player Consent

- Optional telemetry is disabled by default
- Player-identifying telemetry requires:
  - Server owner opt-in
  - Separate informed consent from each affected player
- Never collect passwords, API keys, or private chat for analytics

---

## Configuration

### Configuration File Structure

The controller uses a YAML configuration file with these main sections:

```yaml
# Example controller configuration
schema_version: 1
controller:
  state_store: "state.db"
  log_level: "info"
  
adapters:
  # Declared adapter profiles (with secret references)
  - id: storage.wasabi-staging
    category: storage
    implementation: wasabi
  - id: integration.velocity
    category: integration
    implementation: velocity

servers:
  # Server definitions with lifecycle configuration
  - id: example-server
    environment: production
    hosting: hosting.vultr-prod
    storage: storage.wasabi-staging
    game: game.paper-minecraft
    network:
      join_mode: proxy
      proxy: integration.velocity
```

### Network Profiles

| Join Mode | Required Settings |
|-----------|-------------------|
| `proxy` | Proxy adapter, at least one backend |
| `direct_dns` | DNS adapter, join hostname, fallback records |
| `none` | No network configuration |

### Scaling and Billing

```yaml
lifecycle:
  idle_shutdown: 15m                    # Minimum idle time before shutdown
  scaling:
    billing_increment: 1h              # Billable hour increment
    decision_window_start: 58m         # When to consider scaling (min 58)
    decision_window_end: 59m           # Decision window end (min 59)
    hourly_cost: "0.036"               # Optional: plan cost in provider currency
```

### Secret References

All credentials use the `systemd-credential://` format:

```yaml
secret_refs:
  access_key_id:
    secret_ref: systemd-credential://wasabi-access-key-id
    purpose: Wasabi access key ID
  secret_access_key:
    secret_ref: systemd-credential://wasabi-secret-key
    purpose: Wasabi secret access key
```

---

## Development Workflow

### Branching Strategy

- Work on dedicated feature branches, never directly on `main`
- One branch and one PR per change
- Run pre-push checks locally before pushing

### Pre-Push Checklist

```bash
# Code formatting
gofmt -l .  # Must print nothing

# Static analysis
go vet ./...

# Tests
go test -count=1 ./...
go test -race -count=1 ./...

# Protocol conformance
go run ./cmd/protocol-check

# Configuration validation
go run ./cmd/config-check validate configs/controller.example.yaml

# Complexity check (for changed functions)
go run ./cmd/complexity-check -base origin/main
```

### Code Health Requirements

For new and changed code:

- **Functions:** cyclomatic complexity ≤10, never above 15; nesting ≤3 levels
- **Conditions:** if/return conditions combine at most 3 boolean operators
- **Duplication:** do not copy 5+ lines; share through helpers or generic functions
- **Files:** one responsibility per file; aim for <500 lines
- **Parameters:** at most 5 parameters; group related ones in structs

### Testing Rules

1. Cover every new branch with tests
2. Pin exact error messages in test assertions
3. Before refactoring, add characterization tests that pass on old code
4. Never point tests at production systems or use production data
5. Use scripted fakes in `internal/fakeadapter` for adapter testing

---

## Contributing

### How to Contribute

We welcome:
- Bug reports
- Design criticism  
- Documentation fixes
- Tests
- Features
- Adapters for new games, hosting providers, or storage providers

You do not need to be an expert, and AI is not required.

### Pull Request Requirements

For each PR, explain:
1. What you changed and why
2. Which components are affected (including compatibility changes)
3. Which tests you added or ran (or why testing doesn't apply)
4. Which docs and examples changed
5. Any new dependency, its purpose, and license
6. That you have the right to contribute under AGPL-3.0

### Security-Sensitive Changes

Give extra detail when your change touches:
- Provider authentication or API access
- Storage credentials, bucket permissions, object deletion
- Provisioning, shutdown, destruction operations
- SSH, firewall rules, OS permissions
- Backups, restores, retention policies
- Releases, software updates, deployment, rollback
- Telemetry, logging, consent, personal information
- Plugins, adapters, third-party code execution

Explain the threat model, permissions, failure behavior, and tests.

### License Compliance

The project uses **GNU AGPL-3.0-only**. By contributing:
- You confirm you have the right to submit your work
- You agree distribution under AGPL-3.0
- You keep ownership; no copyright assignment required
- Credit all third-party dependencies in `THIRD_PARTY_NOTICES.md`

---

## Code of Conduct

### Our Commitment

This project welcomes people of every experience level and background. Help keep the conversation respectful, useful, and free from harassment.

### How We Work Together

- Critique the proposal or code. Do not attack the person who wrote it.
- Give specific, actionable feedback, especially when a technical choice could put data or users at risk.
- Discuss disagreements in good faith.
- Protect privacy. Do not publish another person's credentials or personal information.
- Follow the private vulnerability-reporting process in `SECURITY.md`.

### Behavior We Do Not Accept

Harassment, discrimination, intimidation, threats, abusive language, personal attacks, and deliberate disruption have no place here.

---

## Third-Party Notices

All third-party dependencies must be credited in `THIRD_PARTY_NOTICES.md` in the same PR that adds, upgrades, or removes them. This is enforced by code review per project policy.

See `THIRD_PARTY_NOTICES.md` for current credits.

---

## Related Documentation

### Core Documentation Files

| File | Purpose |
|------|---------|
| `README.md` | Project overview and links |
| `docs/ARCHITECTURE.md` | Core vs adapters, safety invariants |
| `docs/CORE_DESIGN.md` | Lifecycle model, durable operations, scaling |
| `docs/ADAPTER_CONTRACTS.md` | Five adapter categories and contracts |
| `docs/CONFIGURATION.md` | Configuration v1 schema reference |
| `docs/SECURITY_MODEL.md` | Assets, trust boundaries, threat model |
| `docs/SECRET_MANAGEMENT.md` | Credential accessor and deployment |
| `docs/ROADMAP.md` | Development phases and timeline |

### Protocol Documentation

| File | Purpose |
|------|---------|
| `protocol/README.md` | Protocol overview and principles |
| `protocol/lifecycle.md` | Server lifecycle state machine |
| `protocol/operations.md` | Durable operation model |
| `protocol/operation-catalog.md` | Standard operation catalog |
| `protocol/adapters/README.md` | Adapter contract specifications |
| `protocol/common-types.md` | Shared types and errors |

### Decision Records (ADRs)

| ADR | Title |
|-----|-------|
| [0001](docs/decisions/0001-language-runtime.md) | Initial language and runtime choice |
| [0002](docs/decisions/0002-bootstrap-based-provisioning.md) | Bootstrap-based provisioning |
| [0003](docs/decisions/0003-execution-agnostic-runtime.md) | Execution-agnostic runtime design |
| [0004](docs/decisions/0004-protocol-encoding-and-bindings.md) | Protocol encoding and bindings |
| [0005](docs/decisions/0005-operation-store.md) | Initial operation store choice (bbolt) |

### Security Documentation

| File | Purpose |
|------|---------|
| `SECURITY.md` | Security policy and vulnerability reporting |
| `docs/SECURITY_MODEL.md` | Detailed threat model and controls |

---

## Quick Reference Cards

### Lifecycle Flow

```
defined → provisioning → restoring → starting → running → ready
           ↓              ↓             ↓             ↓            ↓
        failed         failed         failed         (ready)     draining
                                                           ↓
                                                       stopping → saving
                                                                     ↓
                                                               backing up
                                                                       ↓
                                                                 verified
                                                                     ↓
                                                                   destroying
                                                                     ↓
                                                                   destroyed
```

### State Safety Rules

1. **Durable before destructive:** Operation record persisted before mutating calls
2. **Verify before destroy:** No destruction without verified backup
3. **Reconcile on unclear outcomes:** Inspect actual state before retrying
4. **Staging isolation:** Staging cannot write to production storage
5. **Ready before routing:** Route only when fully ready, not transitional

---

## Glossary

| Term | Definition |
|------|------------|
| **Logical Server** | Durable identity of a deployment, independent of its VM |
| **Instance** | Concrete provider resource (VM) that hosts a server |
| **Operation** | A durable work request with status tracking and recovery |
| **Capability** | An operation an adapter declares it supports |
| **Secret Reference** | Pointer to protected storage; never a secret value in messages |
| **Bootstrap Revision** | Versioned source/configuration reference for OS preparation |
| **Release** | Immutable software manifest plus artifacts |
| **Environment** | Isolation boundary (production, staging, testing) |

---

## Getting Help

- Read the relevant documentation sections for your use case
- Check the decision records for context on design choices
- Review CONTRIBUTING.md before making contributions
- For security issues, use private vulnerability reporting

---

*Last updated: November 2026*
