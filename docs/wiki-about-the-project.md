# About the Project

## What is Game Server Controller?

Game Server Controller (GSC) manages Minecraft Java servers running Paper by handling their full lifecycle—from spinning up fresh VMs when needed to backing up worlds and tearing down after verification.

### Core Purpose

The owner put it this way:

> Bring a backend online when players need it, restore the world, send players across only after the game is ready, and remove the VM only after a fresh backup has been verified.

Under the hood, GSC works through provider-neutral contracts: the core manages lifecycle policy, authorization, state transitions, recovery, and audit logs. Adapters handle vendor-specific or game-specific calls and report what they observe. They never override core policy.

---

## Reference Stack

I'm using this deployment stack as a reference implementation:

- **Cloud Hosting**: Vultr for disposable VMs
- **Object Storage**: Wasabi for S3-compatible backups
- **Game Engine**: Minecraft Java running on Paper
- **Proxy**: Velocity handling player connections
- **Operating System**: Debian, bootstrapped with versioned scripts

This isn't required—just concrete targets to validate the adapters. The protocol stays agnostic to providers and games; adding a new cloud provider, storage backend, or game engine should work without touching the core.

---

## Key Design Principles

Here are the design principles guiding this project:

1. **Provider-Agnostic Core** - New cloud providers, storage backends, or games should work without rewriting core lifecycle logic.

2. **Language-Neutral Protocol** - The wire protocol uses JSON Schema; Go is just the first implementation. Read the schema, and you can build an adapter in any language.

3. **Protocol-First** - Contracts come first. Any language can implement adapters, and the project publishes a versioned management API for third-party clients.

4. **No Web Panel Required** - Basic operation uses CLI/API calls only; a browser UI isn't needed.

5. **Independent Clients** - Third parties can build web, desktop, mobile, or command-line clients using the documented API.

6. **Core Authority** - Clients and adapters cannot bypass authorization, lifecycle policy, environment isolation, approval gates, audit logs, or consent mechanisms.

7. **Safe Failure** - Timeouts are explicit; a timeout doesn't prove an action succeeded or failed. Destructive actions require verified backups.

8. **Privacy by Default** - Telemetry is off by default. Player-identifying data requires separate informed consent from both the server owner and each affected player.

---

## How It Works

### The One-Paragraph Mental Model

Here's what happens in a nutshell: a player connects to the Velocity proxy. When that proxy needs a backend and there isn't one, the **core** accepts a durable operation—provisioning a fresh Debian VM on Vultr, restoring the latest verified world backup from Wasabi, waiting until Paper reports ready, and only then telling Velocity to route traffic. When the server goes idle, the core drains the proxy, runs a game save, uploads a new backup, **verifies** it, and only then destroys the VM. Every destructive step gates on a verified backup; every unclear provider answer reconciles before retrying; staging never writes to production storage.

### Lifecycle States

The server lifecycle flows through these states:

The server lifecycle follows these states:

```
defined → provisioning → restoring → starting → running → ready
            ↓               ↓             ↓             ↓
         failed          failed         failed        (ready)
                                                                 ↓
                                                              draining → stopping
                                                                         ↓
                                                                   saving → backing up
                                                                           ↓
                                                                      verified
                                                                           ↓
                                                                       destroying
                                                                           ↓
                                                                      destroyed
```

### Safety Invariants

These rules must hold across all adapters:

1. **Don't destroy an instance** with unsaved data until its backup is uploaded and verified.
2. **A failed or uncertain backup blocks destructive cleanup.** If the backup fails or the outcome is unclear, don't delete anything yet.
3. **Staging cannot write to production storage** or sync a test world back into production.
4. **Configuration stores secret references**, not credential values. Never store raw secrets in config files.
5. **Every operation is auditable** with explicit status, deadlines, and recovery behavior built in.
6. **A failed candidate cannot silently replace an approved production release.** Failed attempts can't corrupt what's running.
7. **Before destructive work**, verify the exact target and environment to prevent accidental deletions.
8. **Adapters cannot bypass core authorization**, consent, or environment isolation. They report outcomes but don't make safety decisions.

---

## Architecture Overview

### High-Level Structure

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
  (Vultr)    (Wasabi)   (systemd)   (Paper)  (Velocity/DNS)
```

### Core vs Adapter Responsibilities

| Aspect | Core Controller | Adapter |
|--------|-----------------|---------|
| Makes safety decisions | **Yes** | No |
| Calls vendor APIs | **No** | Yes |
| Knows about "staging" | Yes, enforces boundary | Reports environment |
| Owns durable operations | Yes | No |
| Can retry on unclear outcomes | Yes (reconciliation) | Reports outcome |

### Five Adapter Categories

1. **Hosting:** Create/destroy/inspect VMs, attach storage, manage firewalls
2. **Storage:** Upload/download/list/verify backups
3. **Execution:** Start/stop game processes, collect readiness signals
4. **Game:** Game-native save, install releases, restore backups
5. **Integration:** Route player traffic, manage DNS handoff, monitoring

> Note: The split between **execution** (process management) and **game** (game semantics) allows supporting different game engines without touching the execution adapter.

---

## Repository Structure

```
gameserver-controller/
├── protocol/                 # Wire protocol definitions
│   ├── README.md            # Protocol overview
│   ├── lifecycle.md         # State machine
│   ├── operations.md        # Durable operation model
│   ├── operation-catalog.md # Standard operations
│   └── adapters/            # Adapter contract specifications
│       ├── hosting.md
│       ├── storage.md
│       ├── execution.md
│       ├── games.md
│       └── integrations.md
├── docs/                     # Design and reference docs
│   ├── ARCHITECTURE.md      # Core vs adapters, safety invariants
│   ├── CORE_DESIGN.md       # Lifecycle, workflow, scaling
│   ├── ADAPTER_CONTRACTS.md # Adapter contract rules
│   ├── CONFIGURATION.md     # Config v1 schema
│   ├── SECURITY_MODEL.md    # Threat model and controls
│   └── ROADMAP.md           # Development phases
├── cmd/                      # CLI tools
│   ├── controller/          # Main controller binary
│   ├── config-check/        # Configuration validator
│   └── protocol-check/      # Protocol conformance checker
└── internal/                 # Implementation details
    ├── adapter/             # In-process adapter API
    ├── workflow/            # Durable workflow engine
    ├── store/               # bbolt operation store
    └── fakeadapter/         # Fake adapters for testing
```

---

## Documentation Navigation

For more information, check these sections:

- **[Architecture Overview](architecture)** - Deep dive into core vs adapter architecture
- **[Quick Start Guide](../quick-start)** - Setup instructions
- **[Configuration Guide](configuration)** - YAML schema reference
- **[Protocol Specification](protocol-spec)** - Full protocol definition
- **[Development Workflow](development)** - Contributing guidelines

---

*Last updated: November 2026*
