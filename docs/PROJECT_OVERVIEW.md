# Project Overview

**Audience:** A new contributor or operator who wants to understand what Game
Server Controller is, how the pieces fit together, and where to go for the
detail. This document is an orientation layer. Every section ends with a link
to the document that owns that subject in depth; this file does not duplicate
them.

**Status:** Maintained overview. Keep it in sync with the linked documents
when the architecture, protocol, or roadmap changes. If a section here
disagrees with the linked document, the linked document wins.

---

## 1. What this project is

Game Server Controller (GSC) is a lifecycle controller for game servers. Its
concrete job, in the owner's words:

> Bring a backend online when players need it, restore the world, send players
> across only after the game is ready, and remove the VM only after a fresh
> backup has been verified.

It is built against a real reference stack so the first adapters have something
concrete to handle:

- **Vultr** – cloud provider for disposable VMs
- **Wasabi** – S3-compatible object storage for world backups
- **Paper** – the Minecraft Java server
- **Velocity** – the player-facing proxy
- **Debian** – the host OS, provisioned by versioned bootstrap scripts

The protocol is intentionally provider- and game-agnostic. Adding a new cloud
provider, storage backend, or game must be possible without rewriting the core.
That constraint is the single most important architectural decision in the
project.

The project is **in development**. The Phase 1 protocol package is reviewed for
draft `0.21.0-draft`. The durable operation model, in-process adapter boundary,
workflow engine, credential accessor, and a supervised pilot exist and are
tested. The end-to-end production lifecycle (real Vultr/Wasabi/Paper/Velocity
adapters wired to the core, with standard workflows `server.start` through
`server.destroy`) is **not yet complete**. See [the roadmap](ROADMAP.md) for
what is done and what is not, phase by phase.

---

## 2. The one-paragraph mental model

A player connects to the Velocity proxy. When the proxy needs a backend and
there isn't one, the **core** accepts a durable operation, provisions a fresh
Debian VM on Vultr, restores the latest verified world backup from Wasabi,
waits until Paper reports ready, and only then tells Velocity to route
traffic. When the server goes idle, the core drains the proxy, runs a game
save, uploads a new backup, **verifies** it, and only then destroys the VM.
Every destructive step is gated on a verified backup; every unclear provider
answer is reconciled before retrying; staging can never write to production
storage.

That is the target behavior. The repository has the scaffolding for all of it;
the production wiring is still in progress.

---

## 3. Architecture at a glance

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
   (Vultr)    (Wasabi)   (Paper)    (Paper)  (Velocity/
                                         DNS)
```

The core is the only component that makes safety decisions. Adapters report
capabilities and outcomes; they never decide whether an action is safe. The
core never calls a vendor API directly; it always goes through an adapter.

### 3.1 Core vs. adapter: the non-negotiable split

| | Core | Adapter |
|---|---|---|
| Makes safety decisions? | **Yes** | No |
| Calls vendor APIs? | **No** | Yes |
| Knows about "staging"? | Yes, enforces the boundary | Reports which environment it is in |
| Knows about "Vultr"? | No | Yes (hosting adapter) |
| Owns durable operation records? | Yes | No |
| Can retry on unclear outcome? | Yes (reconciliation) | Reports the outcome; core decides |

If you find yourself writing a provider `if/else` branch in core lifecycle
code, or a safety check in an adapter, you are in the wrong file. This is the
most common structural mistake and the one the code review rules are strictest
about.

**Deep link:** [docs/ARCHITECTURE.md](ARCHITECTURE.md) – core vs. adapters,
safety invariants, component responsibilities.

### 3.2 The five adapter categories

The adapter boundary is split into exactly five categories. Keep them
distinct; do not merge game logic into the execution adapter, and do not put
provider branches into core.

| Category | Reference implementation | Responsibility |
|---|---|---|
| **hosting** | Vultr | Create/destroy/inspect VMs, attach storage, manage firewall rules |
| **storage** | Wasabi | Upload, download, list, verify world backups |
| **execution** | Paper on systemd | Start/stop the game process, collect readiness signals |
| **game** | Paper | Game-native save, install a release, restore a backup, report game state |
| **integration** | Velocity / DNS | Route player traffic, drain a backend, manage DNS handoff |

Note the split between **execution** (process management: is Paper running?)
and **game** (game semantics: is the world saved? is the backup restored?).
This split is what lets the project support a different game engine without
touching the execution adapter.

**Deep link:** [docs/ADAPTER_CONTRACTS.md](ADAPTER_CONTRACTS.md) – the
contract rules for each category, capability reporting, and the operator
quick reference for the everyday start/stop/save/drain flow
([docs/OPERATOR_OPERATIONS.md](OPERATOR_OPERATIONS.md)).

---

## 4. The protocol

GSC is a **protocol first**. The wire format is the contract; Go is just the
first implementation. If you can read the JSON Schema, you can implement an
adapter in any language.

- **Message format:** JSON
- **Schema:** JSON Schema 2020-12
- **RPC:** JSON-RPC 2.0 for core↔adapter calls
- **Management API:** OpenAPI 3.2.1 for operator/client→controller calls
- **Current version:** `0.21.0-draft` (Phase 1 reviewed; stable 1.0 pending)

The protocol documents are the source of truth for message shapes, operation
names, state machine transitions, and compatibility rules. The Go code is
checked against them by `cmd/protocol-check`.

### 4.1 What the protocol covers

The `protocol/` directory is organized by concern:

| Document | Covers |
|---|---|
| `protocol/README.md` | Protocol overview, design principles, BCP 14 keywords |
| `protocol/COMPATIBILITY.md` | Versioning and compatibility rules for schema changes |
| `protocol/common-types.md` | Shared types: identifiers, timestamps, errors, pagination |
| `protocol/lifecycle.md` | The server lifecycle state machine |
| `protocol/operations.md` | Durable operation model: acceptance, idempotency, recovery |
| `protocol/operation-catalog.md` | The standard operation catalog (`server.start`, `server.stop`, etc.) |
| `protocol/identity.md` | Identity, capabilities, and trust declarations |
| `protocol/permissions.md` | Permission model and approval flow |
| `protocol/conformance/` | Conformance requirements and test vectors |

### 4.2 Standard operations (the catalog)

These are the operations the core understands. They are the stable surface
that operators and clients call:

- `server.define` – declare a server definition
- `server.start` – provision, restore, and bring a server online
- `server.stop` – drain, save, back up, and tear down a server
- `server.backup` – take and verify a backup without stopping the server
- `server.restore` – restore a named backup
- `server.destroy` – permanently remove a server (gated on a verified backup)
- `server.status` – query current lifecycle state

The full catalog, including optional operations and the approval gates, is in
[protocol/operation-catalog.md](../protocol/operation-catalog.md).

**Deep link:** [protocol/README.md](../protocol/README.md) – the protocol
overview. Start here if you are implementing a new adapter or client in any
language.

---

## 5. The lifecycle, in detail

The server lifecycle is a state machine defined by the protocol, not by any
single Go package. Understanding the states and the invariants that guard each
transition is the most important thing for a new contributor to internalize.

### 5.1 The states

```
  defined ──► provisioning ──► restoring ──► starting ──► running
     ▲           │                │             │             │
     │           │                │             │             │
     │           ▼                ▼             ▼             ▼
     │        failed          failed         failed         stopping
     │                                          │             │
     │                                          │             ▼
     │                                          │        draining
     │                                          │             │
     │                                          │             ▼
     │                                          │          saving
     │                                          │             │
     │                                          │             ▼
     │                                          │         backing up
     │                                          │             │
     │                                          │             ▼
     │                                          │        verifying backup
     │                                          │             │
     │                                          │             ▼
     │                                          │          destroying
     │                                          │             │
     │                                          └─────────────┘
     │                                          (only if backup verified)
     └──────────────────────────────────────────────────────────────────────
                        (failed states can be retried via reconciliation)
```

The exact state names and allowed transitions are defined in
[protocol/lifecycle.md](../protocol/lifecycle.md). The diagram above is an
orientation aid; the protocol document is authoritative.

### 5.2 The safety invariants

These invariants are the whole reason the project exists. They are enforced by
the core, not by the adapters:

1. **Durable before destructive.** The operation record is committed to the
   operation store *before* any mutating provider call is made. A crash
   between record and call is recoverable; a crash after the call is also
   recoverable because the record says what was in flight.

2. **Verify before destroy.** A VM is never destroyed unless a backup has been
   uploaded **and** verified. A failed or uncertain backup blocks destruction.

3. **Reconcile on unclear outcomes.** If a provider call times out or returns
   an ambiguous answer, the core does not blindly retry. It first inspects the
   actual resource state and decides whether the operation already succeeded.

4. **Staging isolation.** A staging deployment cannot write to production
   storage or sync a test world back to production. Staging restores one
   selected, timestamped production backup under read-only credentials.

5. **Ready before routing.** Velocity does not route players to a backend
   until both the game (Paper) and the network report ready.

**Deep link:** [docs/CORE_DESIGN.md](CORE_DESIGN.md) – lifecycle states,
durable operation records, workflow orchestration, and scaling policy.
[protocol/lifecycle.md](../protocol/lifecycle.md) – the authoritative state
machine. [protocol/operations.md](../protocol/operations.md) – the durable
operation model, including idempotency and recovery.

---

## 6. The workflow engine

The workflow engine (`internal/workflow`) is the generic durable execution
runtime that drives multi-step operations like `server.start` and
`server.stop`. It is not tied to any specific game or provider.

Key properties:

- **Durable:** Each step's outcome is persisted before the next step begins.
  A crash mid-workflow resumes from the last completed step.
- **Reconciliation-aware:** When a step's provider outcome is unclear, the
  engine invokes a reconciliation callback before deciding to retry or fail.
- **Idempotent steps:** Steps are designed so that re-running a completed step
  is safe. The engine may re-run steps after a crash.
- **Restart recovery:** On controller restart, the engine lists all
  in-flight operations from the operation store and resumes them.

The first fully-wired workflow is `server.stop` (graceful stop with save and
backup). `server.start` with a fake adapter is implemented for testing.
Production workflows are still being wired to real adapters.

**Deep link:** [docs/WORKFLOW_ENGINE.md](WORKFLOW_ENGINE.md) – the engine's
API, step model, reconciliation contract, and the currently-implemented
workflows.

---

## 7. The operation store

The first controller runs as a single local process with no external database.
Durable state lives in an embedded **bbolt** database on a user-private
filesystem path, protected by an exclusive file lock.

The store exposes:

- Atomic operation acceptance (actor-scoped idempotency key + resource lock
  in one transaction)
- Reads and compare-and-swap updates
- Recovery listing (all in-flight operations, for restart recovery)
- Resource-lock inspection (which server is locked, by whom)

The idempotency index uses versioned HMAC keys so that the same actor
submitting the same operation twice does not create a duplicate.

**Deep link:** [docs/decisions/0005-operation-store.md](decisions/0005-operation-store.md)
– the ADR that chose bbolt and defined the store's contract.

---

## 8. Security model

GSC treats player worlds and provider credentials as high-value assets. The
security model is a first-class design document, not an afterthought.

### 8.1 The core principles

- **Least privilege.** Each adapter credential is scoped to only what that
  adapter needs. A staging adapter cannot write to production storage.
- **Safe failure.** When uncertain, the system fails toward preserving data,
  not toward completing the operation.
- **Approval gates.** Destructive or risky operations (`server.destroy`, major
  version upgrades, world migrations) require explicit operator approval.
- **Audit trail.** Every accepted operation and every state transition is
  recorded in the durable operation store.
- **Telemetry opt-in.** Telemetry is off by default. Player-identifying
  telemetry requires separate informed consent from both the server owner and
  each affected player.

### 8.2 Credential handling

Credentials are never stored in configuration files, logs, or commits. The
controller reads them through a **bounded credential accessor**
(`internal/credential`) at the moment a provider call needs them. On the
reference Debian deployment, credentials live in encrypted systemd credential
files and are loaded only by the controller service unit.

The protocol itself uses **secret references** in messages, never serialized
secret values.

**Deep link:** [docs/SECURITY_MODEL.md](SECURITY_MODEL.md) – assets, trust
boundaries, threat model, and controls. [docs/SECRET_MANAGEMENT.md](SECRET_MANAGEMENT.md)
– the credential accessor design and the systemd credential deployment
procedure. [SECURITY.md](../SECURITY.md) – the security and privacy policy and
the vulnerability reporting process.

---

## 9. Configuration

The controller is configured through a single YAML file. The schema is
validated by `cmd/config-check` before the controller starts; a misconfigured
file is a hard error, not a runtime surprise.

Key sections:

- **`controller`** – state directory path, log level, management API bind address
- **`adapters`** – which adapter implementations are loaded and their
  configuration (secret references, not secret values)
- **`network`** – the network profile: proxy address, backend port range,
  firewall rules to apply on the VM
- **`scaling`** – when to start/stop servers based on player count and idle
  timeout
- **`billing`** – cost guards: maximum concurrent VMs, monthly spend cap
- **`approvals`** – which operations require approval and who can approve

The example configuration file at
[configs/controller.example.yaml](../configs/controller.example.yaml) is
documented line by line in [docs/CONFIGURATION.md](CONFIGURATION.md).

**Deep link:** [docs/CONFIGURATION.md](CONFIGURATION.md) – the full v1 schema
reference, including the network profile, scaling policy, billing policy, and
secret reference syntax.

---

## 10. Repository layout

```
gameserver-controller/
├── README.md                 # Project overview and "start here" links
├── AGENTS.md                 # Rules for AI coding agents (Codex)
├── CLAUDE.md                 # Same rules, for Claude Code
├── CONTRIBUTING.md           # How to contribute: PR rules, tests, security
├── SECURITY.md               # Security policy and vulnerability reporting
├── THIRD_PARTY_NOTICES.md    # Third-party license credits (AGPL requirement)
├── LICENSE                   # GNU AGPL-3.0-only
│
├── protocol/                 # The wire protocol (JSON Schema, JSON-RPC, OpenAPI)
│   ├── README.md             #   Protocol overview and design principles
│   ├── COMPATIBILITY.md      #   Versioning rules
│   ├── common-types.md       #   Shared types
│   ├── identity.md           #   Identity and capability declarations
│   ├── permissions.md        #   Permission and approval model
│   ├── lifecycle.md          #   Server lifecycle state machine
│   ├── operations.md         #   Durable operation model
│   ├── operation-catalog.md  #   Standard operation catalog
│   └── conformance/          #   Conformance requirements and test vectors
│
├── docs/                     # In-depth design and reference documents
│   ├── ARCHITECTURE.md       #   Core vs. adapters, safety invariants
│   ├── CORE_DESIGN.md        #   Lifecycle, durable ops, workflow, scaling
│   ├── WORKFLOW_ENGINE.md    #   internal/workflow: steps, reconciliation
│   ├── ADAPTER_CONTRACTS.md  #   The five adapter categories and their contracts
│   ├── OPERATOR_OPERATIONS.md#   Operator quick reference (start/stop/save/drain)
│   ├── CONFIGURATION.md      #   Config v1 schema reference
│   ├── SECURITY_MODEL.md     #   Assets, trust boundaries, threat model
│   ├── SECRET_MANAGEMENT.md  #   Credential accessor and systemd deployment
│   ├── NO_PROXY_DNS_HANDOFF.md # Optional DNS handoff without a proxy
│   ├── WASABI_STORAGE_ADAPTER.md # Wasabi adapter design
│   ├── VULTR_BOOTSTRAP.md    #   Vultr VM bootstrap procedure
│   ├── CONTROLLER_SETUP.md   #   First-time controller setup
│   ├── SUPERVISED_PILOT.md   #   The supervised pilot (Vultr + Wasabi)
│   ├── RELEASING.md          #   Release process
│   ├── NOTIFICATIONS.md      #   Notification system design
│   ├── PROXY_BRIDGE_API.md   #   Velocity proxy bridge API
│   ├── PROTOCOL_INTEROPERABILITY_PLAN.md # Cross-language interop plan
│   ├── AI_DISCLOSURE.md      #   AI-agent use disclosure
│   └── decisions/            #   Architecture Decision Records (ADR 0001–0005)
│
├── cmd/                      # Go command-line tools
│   ├── controller/           #   The main controller binary
│   ├── controller-setup/     #   First-time setup wizard
│   ├── config-check/         #   Config validator (run before starting)
│   ├── protocol-check/       #   Protocol conformance checker
│   ├── complexity-check/     #   Cyclomatic complexity gate for PRs
│   ├── supervised-pilot/     #   The supervised pilot runner
│   ├── vultr-bootstrap/      #   Vultr VM bootstrap tool
│   └── pilot-capacity-check/ #   Pilot capacity policy checker
│
├── internal/                 # Go implementation (not importable externally)
│   ├── adapter/              #   In-process adapter API (the boundary)
│   ├── adapterprocess/       #   Adapter process management
│   ├── authkey/              #   Auth key management
│   ├── backuppolicy/         #   Backup verification policy
│   ├── config/               #   Config parsing and validation
│   ├── controllerruntime/    #   Core controller runtime loop
│   ├── credential/           #   Bounded credential accessor
│   ├── fakeadapter/          #   Scripted fake adapters (for tests)
│   ├── managementapi/        #   Operator/client HTTP + JSON-RPC API
│   ├── notification/         #   Notification dispatch
│   ├── protocolstate/        #   Protocol state machine enforcement
│   ├── scalingpolicy/        #   Start/stop scaling policy
│   ├── schemaregistry/       #   Local JSON Schema registry
│   ├── store/                #   bbolt operation store
│   ├── velocityproxy/        #   Velocity proxy integration adapter
│   ├── vultrapi/             #   Vultr hosting adapter
│   ├── wasabisto/            #   Wasabi storage adapter
│   └── workflow/             #   Durable workflow engine
│
├── plugins/                  # Out-of-tree plugin examples (if any)
│
└── configs/                  # Example and test configuration files
    └── controller.example.yaml
```

### Where to look when…

| You want to… | Look here |
|---|---|
| Understand the big picture | [README.md](../README.md), then this file |
| Understand the architecture | [docs/ARCHITECTURE.md](ARCHITECTURE.md) |
| Understand the lifecycle states | [protocol/lifecycle.md](../protocol/lifecycle.md) |
| Understand the durable operation model | [protocol/operations.md](../protocol/operations.md) |
| Understand the standard operations | [protocol/operation-catalog.md](../protocol/operation-catalog.md) |
| Implement a new adapter | [docs/ADAPTER_CONTRACTS.md](ADAPTER_CONTRACTS.md) + [protocol/README.md](../protocol/README.md) |
| Understand the workflow engine | [docs/WORKFLOW_ENGINE.md](WORKFLOW_ENGINE.md) |
| Understand the security model | [docs/SECURITY_MODEL.md](SECURITY_MODEL.md) |
| Configure the controller | [docs/CONFIGURATION.md](CONFIGURATION.md) + [configs/controller.example.yaml](../configs/controller.example.yaml) |
| Do everyday operations (start/stop/save/drain) | [docs/OPERATOR_OPERATIONS.md](OPERATOR_OPERATIONS.md) |
| Understand what is done and what is next | [docs/ROADMAP.md](ROADMAP.md) |
| Make a contribution | [CONTRIBUTING.md](../CONTRIBUTING.md) |
| Read the ADRs | [docs/decisions/](decisions/) |

---

## 11. Current implementation status

This section is deliberately short and points to the roadmap for detail. The
roadmap is the authoritative status document; this table is a snapshot.

| Component | Status |
|---|---|
| Protocol schemas (`0.21.0-draft`) | Reviewed, Phase 1 complete |
| `cmd/protocol-check` conformance checker | Implemented |
| bbolt operation store (`internal/store`) | Implemented, tested |
| In-process adapter API (`internal/adapter`) | Implemented |
| Durable workflow engine (`internal/workflow`) | Implemented; `server.stop` wired |
| `server.define` | Implemented |
| `server.start` (fake adapter) | Implemented for testing |
| `server.stop` (graceful, with save) | Implemented |
| Vultr hosting adapter (`internal/vultrapi`) | Implemented (pilot scope) |
| Wasabi storage adapter (`internal/wasabisto`) | Implemented (pilot scope) |
| Supervised pilot (Vultr VM + Wasabi backup) | Implemented, tested |
| Production `server.start` (real adapters) | **Not yet wired** |
| `server.destroy` (production) | **Not yet wired** |
| `server.backup` / `server.restore` (standard workflows) | **In progress** |
| Scaling policy enforcement in the runtime loop | **In progress** |
| Release tooling and rollback | **Planned (Phase 6)** |

**Deep link:** [docs/ROADMAP.md](ROADMAP.md) – the full phase-by-phase status
with what each phase needs to prove before the next one starts.

---

## 12. Development workflow

### 12.1 Branching and PRs

- Work on a dedicated feature branch, never on `main`.
- One branch and one PR per change.
- Run the pre-push checks locally before pushing:
  - `gofmt -l .` (must print nothing)
  - `go vet ./...`
  - `go test ./...`
  - `go test -race -count=1 ./...`
  - `go run ./cmd/protocol-check`
  - `go run ./cmd/complexity-check -base origin/main`
  - `go run ./cmd/config-check validate configs/controller.example.yaml`
- Never push, merge, or delete branches you do not own without owner approval.

**Deep link:** [CONTRIBUTING.md](../CONTRIBUTING.md) – the full contribution
guide, including the PR checklist and the security-change disclosure
requirements.

### 12.2 Testing rules

- Cover every new branch; pin exact error messages in tests.
- Before refactoring, add characterization tests that pass on the old code.
- Tests must not touch production systems, production data, or real
  credentials. Use the scripted fakes in `internal/fakeadapter`.
- Run `go test -race` in addition to `go test` before committing.

### 12.3 The AI-agent working protocol

This repository is developed under a multi-agent AI workflow with explicit
coordination rules. The standing requirements are in [AGENTS.md](../AGENTS.md)
and [CLAUDE.md](../CLAUDE.md). The current coordination state (which agent owns
which file group, which branches are in flight) is in
[docs/AGENT_HANDOFF.md](AGENT_HANDOFF.md) – **read it before starting work**
so you do not collide with another agent's in-progress changes.

---

## 13. The ADRs

Architecture decisions that are not obvious from the code are recorded as
Architecture Decision Records in [docs/decisions/](decisions/). Read these
before proposing a change that touches the same area; the ADR explains why the
current design was chosen and what alternatives were rejected.

| ADR | Title | Key decision |
|---|---|---|
| [0001](decisions/0001-language-runtime.md) | Initial language and runtime | Go 1.23, bbolt v1.4.3; other languages allowed for adapters |
| [0002](decisions/0002-bootstrap-based-provisioning.md) | Bootstrap-based provisioning | Versioned bootstrap scripts, not prebuilt VM images |
| [0003](decisions/0003-execution-agnostic-runtime.md) | Execution-agnostic runtime | VM-first, but the protocol must not require Kubernetes |
| [0004](decisions/0004-protocol-encoding-and-bindings.md) | Protocol encoding and bindings | JSON + JSON Schema 2020-12 + JSON-RPC 2.0 + OpenAPI 3.2.1 |
| [0005](decisions/0005-operation-store.md) | Initial operation store | bbolt embedded store, one process, exclusive file lock |

---

## 14. Licensing and third-party credits

The project is licensed **GNU AGPL-3.0-only**. Every third-party dependency
must be credited in [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md) in the
same PR that adds, upgrades, or removes it. This is a hard requirement from
[CLAUDE.md](../CLAUDE.md) and is enforced in code review.

To cite the project, use [CITATION.cff](../CITATION.cff). The copyright notice
is in [COPYRIGHT.md](../COPYRIGHT.md).

---

## 15. Where to go next

If you are new to the project, the recommended reading order is:

1. **This file** – you are here
2. [README.md](../README.md) – the project's own words on what it is and why
3. [docs/ARCHITECTURE.md](ARCHITECTURE.md) – core vs. adapters, safety invariants
4. [protocol/README.md](../protocol/README.md) – the protocol overview
5. [protocol/operation-catalog.md](../protocol/operation-catalog.md) – the standard operations
6. [docs/CORE_DESIGN.md](CORE_DESIGN.md) – how the core drives the lifecycle
7. [docs/ADAPTER_CONTRACTS.md](ADAPTER_CONTRACTS.md) – how to build an adapter
8. [docs/SECURITY_MODEL.md](SECURITY_MODEL.md) – what the system must protect
9. [docs/ROADMAP.md](ROADMAP.md) – what is done, what is next
10. [CONTRIBUTING.md](../CONTRIBUTING.md) – how to make a contribution

If you have a specific task, skip to the relevant document using the
"Where to look when…" table in [section 10](#10-repository-layout).
