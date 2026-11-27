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

Game Server Controller (GSC) is an open-source, self-hosted lifecycle
controller for running game servers on demand. It aims to reduce cloud costs,
local electricity consumption and operator workload while protecting persistent
game data. The first measure of success is safe operation of the owner's
production Minecraft network, before broader competitive expansion.

The initial reference stack is:

- **Vultr:** cloud provider for disposable VMs.
- **Wasabi:** S3-compatible off-site object storage for verified world backups
  and versioned artifacts.
- **Paper:** Minecraft Java server.
- **Velocity:** player-facing proxy, with existing Java/Bedrock and voice-chat
  connectivity to be preserved.
- **Debian and systemd:** host bootstrap and execution environment.

The protocol is provider- and game-agnostic. Adding a provider, storage backend
or game should not require rewriting core lifecycle rules.

### 1.1 Intended deployment models

| Model | Goal |
|---|---|
| Cloud-only | Vultr hosting with no local server, Proxmox or LAN wake agent required. |
| Self-hosted | Optional Proxmox VE hosting, with on-demand guest start and stop. |
| Hybrid | Local hosting plus optional cloud fallback or additional capacity within configured limits. |

Vultr is a first-class option for all users, not merely a fallback. Proxmox
support is planned. The owner's eventual preference is local consolidation if
measured workload capacity, connectivity, cost and recovery justify it. Wasabi
remains the reference off-site store so a local host failure does not also
remove the only backup.

The project is **in development**. The Phase 1 protocol package is reviewed for
draft `0.21.0-draft`. A durable store, workflow engine, adapter boundary,
configuration loading and supervised pilot exist. Only `server.define` is
registered as a runnable operation in the normal controller runtime in the
reviewed source. Other standard workflow definitions exist and are fake-tested;
a complete live lifecycle, Proxmox support and minigame orchestration have not
been demonstrated. See [the roadmap](ROADMAP.md) for priorities, phase status
and evidence gates.

---

## 2. The one-paragraph mental model

For a persistent world, a player requests a backend through the lobby. The
core durably records the operation, allocates or starts the required
infrastructure, restores the selected verified world when necessary, starts
Paper, validates fresh readiness and permits routing. When idle, it drains
players, saves the world and verifies a backup before any authorized
destruction of the exact instance. A stopped local guest can instead be
retained. Reconcile uncertain provider outcomes before retrying, and never
allow staging to write to production storage.

For a minigame, the planned flow creates or starts a session from a reviewed
versioned template, queues and routes players after readiness, completes the
match, persists and verifies required results, returns players to the lobby
and reclaims session resources. Only explicitly classified disposable arena
state may be discarded under a reviewed policy. Unknown state fails closed.

Cloud idle policies reduce billed compute time; guest shutdown on Proxmox
reclaims local resources. Optional physical-host shutdown requires a dedicated
enrolled host, no active or protected workloads, and a trusted independent
always-available wake path. The public entry point and wake-request functions
cannot depend exclusively on the sleeping machine.

These are target behaviors, not a claim of working end-to-end orchestration.
Production destruction remains disabled pending separate authorization.
See [the roadmap](ROADMAP.md).

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
   (Vultr)    (Wasabi)   (systemd)  (Paper)  (Velocity/
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

| Category | Reference target | Responsibility |
|---|---|---|
| **hosting** | Vultr; Proxmox planned | Create/destroy/inspect instances, manage provider resources and firewall rules |
| **storage** | Wasabi | Upload, download, list, verify world backups and release artifacts |
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

These are the standard operations in the draft protocol. They are the intended
client surface, not a list of all currently runnable runtime operations:

- `server.define` – declare a server definition
- `server.provision` – create and prepare an instance, optionally restore an explicitly selected backup, start and validate
- `server.start` – start the recorded execution unit and validate readiness
- `server.stop` – drain, save and stop; retain the logical server and VM
- `server.backup` – capture and verify a consistent backup, with resume behavior governed by policy
- `server.restore` – restore an explicitly identified verified backup
- `server.destroy` – remove the exact infrastructure instance after backup and authorization checks; retain logical metadata and backup references

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

A simplified persistent-world lifecycle uses the protocol's actual state names:

```
unprovisioned -> provisioning -> preparing -> restoring (if requested)
    -> starting -> validating -> ready
    -> draining -> stopping -> stopped
    -> backing_up (when required) -> stopped
    -> destroying (only when authorized and backup verified) -> destroyed
```

A ready server may become degraded. Failure may lead to `failed` with known
partial effects retained for recovery. The diagram is illustrative, not a
complete transition graph: backup, restore, start and recovery have distinct
operation-specific transitions. See [protocol/lifecycle.md](../protocol/lifecycle.md)
for normative transitions and [the operation catalog](../protocol/operation-catalog.md)
for preconditions. A stopped server may retain its VM; a destroyed server
retains logical metadata and backup references.

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

The normal runtime currently registers only `server.define`. Provision,
start, stop, backup, restore and destroy workflow definitions are implemented
and fake-tested, not fully connected to production adapters. Recovery and
reconciliation primitives exist; continuous scheduling of waiting work and
queue-saturation recovery still need verification and remediation.

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

### Setting limits for a server

Operators control when and how a server scales through per-server `lifecycle`
settings. These are the knobs that matter day to day:

**`idle_shutdown`** (policy implemented; runtime automation pending) – the
minimum amount of time a server must be idle before the planned controller
shutdown sequence can begin. While the server is running, the
controller only starts or stops servers inside the billing decision window
(default: minutes 58–59 of each running hour), so a server that idles at minute
50 will not be destroyed until the next window opens. If the plan's hourly
cost is below `ignore_billing_below_hourly_cost`, the window is ignored and the
server scales down as soon as it is idle. Both cost values are optional; set
them in the provider account's currency to the cost of the plan the server
actually runs on.

```yaml
lifecycle:
  idle_shutdown: 15m
  scaling:
    billing_increment: 1h                # default
    decision_window_start: 58m           # default
    decision_window_end: 59m             # default
    ignore_billing_below_hourly_cost: "0.005"  # optional
```

**`migration_targets`** (planned; validator rejects until accepted) – controls
how many migration target servers may run alongside a live server at the same
time. A migration target is the new server a world migration moves to; it runs
side by side with the live server until the handover completes. Set
`enabled: false` to prevent migration targets entirely. `max_servers: 0`
removes the concurrency limit.

```yaml
# controller-level default (planned)
controller:
  capacity:
    migration_targets:
      enabled: true
      max_servers: 1

# per-server override (planned)
lifecycle:
  migration_targets:
    enabled: true
    max_servers: 1
```

**`scaling.vertical`** (planned; validator rejects until accepted) – turns on
vertical auto-scaling for one server. When enabled, the controller moves the
server one rung at a time through a plan ladder (cheapest first) via a
migration to a migration target server. A manual scaling command pauses
auto-scaling for that server until it completes.

```yaml
lifecycle:
  scaling:
    vertical:
      enabled: true
      plans:
        - vc2-1c-2gb
        - vc2-2c-4gb
        - vc2-4c-8gb
```

> **Note:** The planned options (`migration_targets`, `scaling.vertical`) are
> documented in CONFIGURATION.md so operators can plan for them, but adding
> them to a configuration file today will make it fail validation. The
> controller runtime does not schedule idle shutdowns yet; the validator
> checks the policy, not the execution.

**Deep link:** [docs/CONFIGURATION.md](CONFIGURATION.md) – the full v1 schema
reference, including the network profile, scaling and billing policy, backup
schedule and retention, the planned vertical-scaling options, and the secret
reference syntax.

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
│   └── decisions/            #   Architecture Decision Records (ADR 0001–0006)
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
│   ├── vultrapi/             #   Vultr API client
│   ├── wasabistorage/        #   Wasabi storage adapter
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

The roadmap is the authoritative planning document. This snapshot distinguishes
implemented components from runtime integration and live verification.

| Component | Status |
|---|---|
| Protocol schemas (`0.21.0-draft`) and Go conformance checker | Reviewed draft and implemented validation; stable 1.0 pending |
| bbolt operation store and adapter boundary | Implemented, tested |
| Workflow engine | Implemented recovery/reconciliation primitives; continuous scheduling and real integration need work |
| `server.define` | Runnable metadata-only workflow in the normal runtime |
| Provision, start, stop, backup, restore and destroy | Workflow definitions exist and are fake-tested; not runnable end to end through the normal runtime |
| Management API | Authenticated capability, server, operation inspection and submission handlers; runnable operation types limited; cancellation/events pending |
| Vultr API client and supervised pilot | Guarded real provider provisioning and backup-service tools; not a complete runtime hosting adapter |
| Wasabi (`internal/wasabistorage`) | Initial storage adapter and runtime registration; lifecycle wiring and isolated live verification incomplete |
| Velocity and Cloudflare | Initial adapters and runtime registration; full routing/DNS lifecycle integration and verification incomplete |
| Billing-aware idle policy | Policy and scheduler components exist; runtime wiring pending |
| VictoriaLogs and Homelable | Optional tools implemented, disabled by default; production deployment not implied |
| On-demand minigames, Proxmox and hybrid hosting | Planned; no completed reference implementation |
| Release tooling, rollback and automated staging updates | Planned in Phases 5 and 6 |

See [the roadmap](ROADMAP.md) for current priorities and evidence gates.
Passing CI and fake-adapter tests does not establish live production readiness.

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
and [CLAUDE.md](../CLAUDE.md). Read [the coordination guide](../GSC_COORDINATION.md),
[CLI documentation](COORDINATION_CLI.md) and [agent handoff](AGENT_HANDOFF.md)
before starting. Use the live coordination registry, not the committed
snapshot or historical handoff, to claim work and reserve files. Keep task
branches and worktrees isolated and coordinate overlapping changes.

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
| [0006](decisions/0006-optional-ha-and-reliability-tiers.md) | Optional HA and reliability tiers | Standalone remains supported; advanced reliability profiles are optional goals, not measured guarantees |

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
