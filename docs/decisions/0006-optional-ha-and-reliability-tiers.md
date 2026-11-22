# ADR 0006: Optional High Availability, Reliability Tiers, and Safe Distributed Ownership

- **Status:** Proposed
- **Date:** 2026-11-22
- **Decision owners:** Project maintainers
- **Scope:** Controller runtime, operation persistence, regional deployment and service-specific reliability. This ADR does not change the wire protocol or authorize production deployment.
- **Related decisions:** [ADR 0003: VM-first execution](0003-execution-agnostic-runtime.md), [ADR 0004: Protocol encoding](0004-protocol-encoding-and-bindings.md), [ADR 0005: bbolt operation store](0005-operation-store.md)
- **Supporting design:** [Core design](../CORE_DESIGN.md), [Architecture](../ARCHITECTURE.md), [Workflow engine](../WORKFLOW_ENGINE.md), [Security model](../SECURITY_MODEL.md), [Development roadmap](../ROADMAP.md), [Operation protocol](../../protocol/operations.md)

## 1. Context and decisions considered

Game Server Controller (GSC) is a language-neutral, provider-neutral lifecycle controller. The first Go implementation uses a local bbolt operation store, durable operations, actor-scoped idempotency, logical server locks, versioned adapter contracts, and reconciliation of ambiguous provider effects. The concrete reference network uses a persistent Velocity proxy/lobby, with a Paper backend created on demand on a standard Debian VM through Vultr, a verified world restored from Wasabi, and players transferred **only after fresh, exact-instance readiness**. Bootstrap scripts prepare provider-standard OS images; snapshots and custom VM images are not prerequisites. Existing gameplay must continue independently while the controller or database is unavailable. Automatic production VM destruction is currently disabled and remains disabled.

We reviewed several architectures: single Go+bbolt with systemd recovery; active/passive Go+bbolt with replicated local state; multiple active Go controllers and a transactional PostgreSQL/Patroni/etcd HA store; Elixir/Erlang OTP as an optional additional runtime; independent regional controller cells; selectively replicated read services; and long-term high-assurance/distributed-systems research. We also considered Kubernetes/Agones, application management panels such as Pterodactyl, Ansible, provider snapshots, colocation and alternative databases. These are not all needed for a small deployment of roughly ten Minecraft players.

The design must balance correctness, incremental implementation, operational cost, implementation-language portability, and failure isolation. This ADR formalizes the preferred direction while leaving advanced implementation details for follow-up ADRs. **It does not claim 99.99%, 99.999%, 99.9999% or 99.99999999% has been achieved.**

## 2. Decision summary

1. **Keep a lightweight standalone Go+bbolt controller as the default.** Harden recovery and monitor it. A narrowly scoped four-nines target can be pursued on this profile, but a single VM/disk remains a failure domain, so no availability guarantee is inferred.
2. **Add optional HA without changing the public protocol.** Start with multiple active Go controllers using a transactional PostgreSQL store, Patroni primary failover and appropriate quorum coordination (initial candidate: three etcd voters). Keep bbolt fully supported and do not make PostgreSQL a mandatory dependency.
3. **Make durable resource ownership and fencing the correctness authority.** Keep operation resource locks independent of expiring worker claims, reconcile uncertain side effects, and never claim exactly-once cloud mutations.
4. **Develop Elixir/Erlang OTP, if desired, as a separate conformant implementation or runtime option.** OTP's supervision is useful but not a substitute for durable distributed ownership, transactional authority or provider-side reconciliation.
5. **Treat regional cells as an optional later deployment profile.** Each region owns its authoritative state and explicitly assigned resources. Failover and failback require verified fencing and reconciliation; no heartbeat-only automatic multi-region mutation takeover.
6. **Use service-specific availability targets.** Four nines first, five nines for advanced transactional services, six nines for selected fault-isolated reads or status functions, and ten nines as ongoing research rather than a product SLA.
7. **Keep the gameplay data plane independent of the controller control plane.** Routine controller, database and management-API outages do not automatically stop a healthy Velocity/Paper session. Destructive automation stays disabled until separately approved.

## 3. Architecture alternatives we reviewed

| Design | Decision | Rationale and limits |
| --- | --- | --- |
| Single Go+bbolt controller on one VM, managed by systemd | **Default / implement first** | Cheapest, simplest and compatible with current code. Can restart quickly and reconcile durable operations, but machine, storage and network remain single failure domains. |
| Go+bbolt active/passive with shared file or naïve file replication | **Do not adopt as the default HA design** | bbolt's one-process file ownership does not provide cluster-wide commit consensus. Copying, network-mounting or promoting state without tested fencing risks state rollback and duplicate provider operations. A carefully fenced replication solution may be researched, but is not automatically simpler than PostgreSQL. |
| Three active Go API/workers + PostgreSQL/Patroni + quorum DCS | **Preferred optional HA reference design** | Strong transactional coordination and familiar operational tooling. Synchronous durability can deliberately suspend mutation acceptance, and safe leader fencing and recovery must be validated. |
| Elixir/Erlang OTP implementation with supervised processes | **Optional parallel implementation** | Supervision and isolation help runtime recovery. The BEAM cluster itself must not be considered the authoritative distributed lock or commit mechanism, and an Elixir rewrite is not a four-nines prerequisite. |
| Three independent regional controller cells | **Optional long-term design** | Limits regional failure blast radius. Requires per-resource mutating ownership, state reconciliation and conservative DR; not a globally active writable database. |
| Dedicated replicated read/status plane | **Advanced, scoped six-nines candidate** | Can keep contractually valid historical views readable through storage outages; freshness, authorization and status semantics must remain explicit. Does not make mutations six-nines available. |
| Kubernetes/Agones required for orchestration | **Not required** | Not justified by the initial small VM-based deployment. Optional execution/provider adapters may support them later. |
| Pterodactyl or another hosting panel as controller core | **Not chosen** | A panel does not replace GSC's language-neutral protocol, resource policy, custom lifecycle evidence and reconciliation requirements. UI clients may be added independently. |
| Ansible for provisioning | **Optional implementation mechanism** | Useful for idempotent host configuration if adopted, but not a replacement for the controller's durable operation state, safe retries, approval policy and ownership. Versioned bootstrap/systemd remains supported. |
| VM snapshots/custom prebuilt images | **Not required** | Existing provider-standard Debian + versioned bootstrap + Wasabi restore remains the first reference path. |
| One global, cross-region synchronous write database | **Not the preferred cell design** | Adds WAN latency and correlated availability failure; independent regional authority with explicit transfer is favored. |
| Other distributed stores, colocation and specialty fault-tolerant hardware | **Future research / replaceable behind interfaces** | Do not require significant cost, premature migrations or operator complexity without evidence of a needed reliability improvement. |

## 4. Deployment topologies

### A. Standalone: one small VM

~~~text
Operator / management CLI or API
                |
        Go controller (systemd)
         |        |         |
         |        |         +-- five versioned adapter categories
         |        |               hosting/storage/execution/game/integration
         |        +-- durable workflow runner and reconciliation
         +-- bbolt on local persistent disk
                  (exclusive writer)
                |
       Provider / host / proxy adapters
                |
        Vultr VM, Wasabi, systemd,
        Paper, Velocity and DNS
~~~

- Local controller process supervision and monitored API availability.
- Durable accepted operations and resource locks; periodic recovery scan in addition to bounded in-memory work notifications.
- Versioned bbolt snapshots, tested restore, credential/keyring backup and conservative corrupt-state handling.
- An interruption to the one VM can make management unavailable; **this is not equivalent to multi-host HA**.
- Standalone installation must not require any external database, etcd, Kubernetes, paid monitoring provider or second controller language.

### B. Optional HA: one authoritative transactional cell

~~~text
Operators / management clients
                |
         Redundant ingress
          /      |      \
       Go A     Go B     Go C
    API/worker API/worker API/worker
          \      |      /
         Transactional store contract
                   |
       PostgreSQL / Patroni cluster
           Primary + 2 standbys
                   |
          3-member DCS quorum
       (etcd candidate, independent)
                   |
       Workflow ownership + fencing
                   |
         Versioned external adapters
~~~

- API and worker replicas can run concurrently, but **each conflicting logical resource has one authorized mutating owner**.
- Shared database commits are the authority for operation acceptance, locks and claims, not process-local memory, proxy identity or cluster membership.
- Separate expiring worker claim from durable operation resource lock; use monotonic claim generation and compare-and-swap updates. A lease timeout does not prove the old worker's side effects stopped.
- Patroni, leader promotion and synchronous settings need explicit independent failure-domain deployment and tested fencing. When durability cannot be preserved, **pause mutation acceptance**, not silently weaken consistency.
- Keep published adapter and management protocols unchanged; do not turn PostgreSQL schema details into wire protocol requirements.

### C. Single-VM software QA topology (not production HA)

~~~text
One isolated test VM
  3 separately configured Go controllers
  3 PostgreSQL processes (primary + 2 standbys)
  3 etcd voter processes
  independent local ports, directories and identities
  synthetic provider responses and fault injection
~~~

This proves software orchestration only: process loss, DB promotion behavior, claims, partition simulations, old-primary scenarios and recovery. The single VM, hypervisor, disk and power supply are still common failure domains. Production HA distributes controllers, PostgreSQL nodes, quorum voters and redundant routing across independent hosts/networks when possible.

### D. Optional multi-region: independent authoritative cells

~~~text
            Global discovery (optional)
                /      |      \
         Region A   Region B   Region C
           API        API        API
        workers     workers     workers
        local DB    local DB    local DB
          |           |           |
     owned set A  owned set B  owned set C
                |
       Explicit, fenced transfer
       and verified reconciliation
~~~

- One cell owns any given logical resource for mutations at a time, even when it manages servers located elsewhere.
- Cells do not share a single writable multi-region authority by default.
- A healthy cell should not have to synchronously contact all other regions for its ordinary requests.
- Cross-region recovery uses bounded, documented RPO/RTO, controlled promotion and failback, old-owner fencing, and real-provider inventory reconciliation.
- Network partition or stale heartbeat alone MUST NOT trigger two independent mutating owners. If effective fencing cannot be verified, fail closed and require operator intervention.
- Async replicated disaster-recovery state may lag or lose previously acknowledged transactions. Never blindly replay an incomplete restored history.

### E. Selected six-nines historical/read architecture

~~~text
       Redundant regional API ingress
              /         \
        Read node A   Read node B
         verified, versioned snapshots
              \         /
        authoritative write cell
        (five-nines *target*, not claim)
~~~

Read nodes MAY continue answering **contractually historical** queries during a write-store outage, provided the response displays snapshot version, observed timestamp, freshness and appropriate authorization. They MUST NOT report stale provider/player health as current, permit unauthorized disclosure through old permissions, or acknowledge uncommitted operations. Keep read-only availability, liveness, durable mutation acceptance and workflow execution as separate SLIs.

## 5. Safety invariants shared by all profiles

The following override any uptime optimization:

1. **Persist before effect.** Validate authorization, scope, exact targets and request bindings; durably record accepted operation and immutable step intent before calling adapters.
2. **Atomic acceptance and locks.** Actor-scoped idempotency, accepted operation record, logical server resource lock and audit metadata must be transactionally consistent. Release lock only with safe terminal state.
3. **No duplicate unsafe mutations.** Never equate a timeout/failed HTTP response with a non-executed provider call. Inspect and reconcile actual resources before retrying. Exactly-once provider-side mutation is not assumed.
4. **Authoritative fencing.** Reject stale worker database commits. Epoch/generation must cover state transitions and, where possible, external action authority. Database fencing alone does not revoke an already sent Vultr/Wasabi request.
5. **Safe uncertainty.** Preserve unresolved intent and recovery evidence through claims expiring, host failure, network partitions, restore from backups and failover. When safety cannot be established, pause and request operator review.
6. **Data integrity and permissions.** Correct target/environment, scoped provider credentials, forwarding secrets, least privilege and audit trail are required; staging is isolated from production and cannot write back into the live world.
7. **Backups before irreversible cleanup.** A provider acknowledgment is not backup proof. Confirm game-consistent save, uploads, immutable identity/checksums/readback and compatibility before any destructive cleanup where required.
8. **Readiness before player routing.** Detect fresh readiness from the **current** backend instance (including a fresh Paper "Done (...)" signal and relevant checks); then instruct Velocity from its authorized side to transfer waiting players, never transfer on stale evidence.
9. **Controller/data-plane separation.** Controller outages must not deliberately disconnect already-connected players or stop otherwise healthy Paper/Velocity instances.
10. **No automatic destruction.** Automatic destruction of production instances remains disabled until separate explicit authorization. Retain guarded and approved destructive workflow semantics for future operator-controlled use.
11. **All languages and deployment modes conform.** Two different implementations must never independently mutate one resource without verified single ownership and protocol-conformant authorization.

## 6. Persistence and workflow design

The operation-store abstraction MUST express transactionally enforceable domain semantics, not just generic CRUD: atomic acceptance, actor-scoped dedupe, immutable request/plan snapshots, version-checked updates, durable lock acquisition/release, claim issuance and renewal, claim generations, outcome uncertainty and recovery listing.

- **Go+bbolt today:** one controller process holds a local exclusive file lock, with atomic write transactions under ADR 0005. A periodic due-work scanner must rediscover accepted eligible operations even if the in-memory queue drops a notification. Restart recovery alone is insufficient for liveness.
- **Optional distributed store:** PostgreSQL transactions and explicit consistency guarantees. Only a committed durable transaction may acknowledge acceptance. A client timeout around a commit must be investigated by idempotency lookup rather than risking duplicate acceptance.
- **Durable resource lock versus worker execution claim:** an operation lock remains across claim expiry, worker restarts or takeovers. The next worker must reconcile the previous attempt before causing more external effects.
- **Scheduling:** bounded workers, observable queues, bounded retries, per-resource serialized mutations, backpressure and fairness. Optional monitoring/notification failure cannot block unrelated safe workflows.
- **Recovery:** restore versioned state and idempotency keyring safely, inspect external resources and side effects, and account for potential database rewind or out-of-band changes before continuing.
- **Replication limits:** strict synchronous PostgreSQL replication may block writes if the required synchronous standby is lost. That is an explicit safety/availability tradeoff, not something to hide with a misleading liveness check.
- **Independent testing:** verify split-brain prevention, at-most-one valid mutating authority (not false exactly-once effects), stale-worker rejection, commit ambiguity, restored-state reconciliation and full-disk behavior.

Elixir/OTP supervision may use supervised workflows and adapters in another implementation, but the store/ownership boundary remains equivalent in safety terms. Do not use an inter-node BEAM message or worker's PID as a durable ownership proof.

## 7. Service-specific reliability objectives and evidence gates

These numbers are **engineering targets**, not measured SLO results or public SLAs:

| Tier | Scoped example | 365-day *time-based* downtime budget | Proof requirement |
| --- | --- | --- | --- |
| 99.99% (four nines) | Standalone reliability improvements and initial optional HA control-plane functions | ~52m 34s/year | Clearly scoped success/latency SLI, monitored process recovery, representative staging + fault injection; sustained measurements needed to claim achieved availability |
| 99.999% (five nines) | Optional advanced HA durable operation acceptance and critical workflow infrastructure | ~5m 15s/year | Safe distributed ownership, independent physical redundancy, tested failover, deployment continuity and operational evidence |
| 99.9999% (six nines) | **Only selected** independently useful read-only management/historical-status endpoints | ~31.54s/year | Explicit freshness and isolated dependencies, redundant ingress/read serving, fault-injection validation and statistically meaningful independent observations |
| 99.99999999% (ten nines) | Extreme fault-tolerance research only | ~3.15ms/year | Exploratory models and experiments, **not an achievable end-to-end SLA commitment** |

A six-nines goal is not an across-the-board goal for provisioning, fresh live health, durable mutations, world restoration, backup workflows or provider-dependent operations. False success responses do not count. A liveness endpoint that answers 200 while durable operation acceptance is blocked does not establish write availability.

**Separate gates** for every tier: (1) architecture designed, (2) functionality implemented and safety-invariant tests passing, (3) defined faults survived in fault injection, and (4) meaningful operational measurements supporting a claim. Do not advertise a percentage based on redundancy diagrams, passing CI or short synthetic tests alone. Report confidence, observation window, measurement method and remaining common-mode risks. A low-traffic controller may lack enough requests to establish six nines statistically.

**Buffered planning windows from prior roadmap discussions**, measured from November 2026 and overlapping rather than additive:

| Milestone | Conservative planning window |
| --- | --- |
| Supervised staging pilot | 3–9 months |
| First production lifecycle | 9–18 months |
| Full original product roadmap Phases 0–7 | 24–36 months |
| Optional single-VM HA QA prototype | 12–24 months |
| Four-nines-capable optional HA | 18–36 months |
| Five-nines advanced control plane | 36–72 months |
| Six-nines service architecture design | 6–18 months (parallel investigation) |
| Selected six-nines implementation/fault testing | 18–48 months |
| Selected six-nines operational evidence | 36–108+ months, evidence-gated, **no guaranteed date** |
| Ten-nines research | Continuous, no delivery date |

The current checkout of the development roadmap must be reconciled with previously merged [PR #397](https://github.com/amibamae/gameserver-controller/pull/397) before presenting these intervals as the repository's currently accepted schedule. If a later force-push removed that roadmap update, restore it in a separately reviewed documentation PR rather than silently implying it is present on main. Plan generous contingency; accelerate safely when evidence is available. No Elixir rewrite, Kubernetes or multi-region build-out blocks the first Go+bbolt release.

## 8. Fault-injection and acceptance matrix

Every profile requires a documented, repeatable acceptance suite, plus longer-running production monitoring before SLO claims:

| Failure scenario | Required safe behavior |
| --- | --- |
| Controller process crashes or restarts | Persisted operations/locks survive; recover and reconcile without duplicate effects |
| Bounded in-memory queue overflows | Durable eligible work is eventually rescheduled without requiring restart |
| Local bbolt disk full / corrupted / restored | Do not silently accept work; fail closed; restore from consistent backup and keyring |
| PostgreSQL primary lost or old primary isolated | One authoritative writer; safe promotion/fencing; no double dispatch or falsely acknowledged commit |
| Synchronous standby / quorum lost | Explicit safe refusal of writes where required rather than unacknowledged unsafe operation |
| DB commit succeeds but client times out | Determine acceptance by durable idempotency binding, not duplicate insertion |
| Worker claim expires mid-provider request | New worker rejects stale commit and reconciles external outcome before acting |
| Vultr API times out after creating a VM | Inspect provider state and exact resource identity; avoid duplicate instance creation |
| Wasabi upload returns success but readback fails | Backup is not verified; destructive cleanup stays blocked |
| Restore from stale DB backup | Compare provider inventory and operation history before resuming mutations |
| Velocity or game readiness stale | Do not transfer waiting players until fresh exact-instance readiness is proven |
| Controller fails while players are in game | Existing Velocity/Paper gameplay continues independently |
| Notifications/telemetry unavailable | Core operations continue where safe; bounded durable retry/reporting |
| Regional network partition or attempted takeover | No split-brain resource ownership; fail closed absent verified fencing |
| Deployment/rollback/migration | Safe rolling changes or explicit maintenance window; no untested schema rollback |

Complement unit and conformance tests with repeatable process-kill, network partition, lost/reordered response, database promotion, and isolated restore drills. Consider formal specifications (e.g., TLA+) for claim ownership, epoch fencing, resource locks and multi-region transfer; history/linearizability tests for the transactional store. Do not simulate destructive production actions.

## 9. Roadmap, delivery and ownership

The original Phases 0–7 are the product delivery roadmap and remain necessary: provider-standard Debian bootstrap; real Vultr/Wasabi/Paper/Velocity adapters; approved provision → bootstrap → verified restore → Paper readiness → safe Velocity /send; guarded manual backup and stop; release rollback; isolated staging and updates; additional provider/game adapters. Do not rewrite lifecycle orchestration around HA before the real production interfaces work.

Reliability work is an optional, partly parallel track:

- **R0:** define SLIs, fault domains, RPO/RTO, current gaps, ownership proof and ADRs; verify report claims against actual code.
- **R1:** harden standalone Go+bbolt: restart, durable rescheduling, safe reconciliation, backup and restoration tests, independent monitoring.
- **R2:** design and implement optional transactional PostgreSQL backend, claims, fencing, three-controller/three-DB/three-etcd single-VM QA; preserve standalone.
- **R3:** distribute optional HA services across independent production failure domains, test primary loss, failback, rolling upgrades and rollback; target four-nines-capable architecture.
- **R4:** eliminate avoidable shared dependencies; improve critical five-nines availability and operational evidence.
- **R5:** implement selectively isolated six-nines reads and historical status, independently validate their service contracts; evidence-gated success.
- **R6:** continuously research ten-nines impossibility boundaries and potential long-term designs without committing expensive infrastructure.

Parallel contributors/agents may independently investigate storage, workflow execution, hosting, game integration, testing and review. Implementation requires bounded parallelism, separated file ownership, explicit coordination and independent review. All changes must follow AGENTS.md and CLAUDE.md, conformance rules, security documentation, tests and branch protections. No deployment or paid provider resources without explicit owner approval.

## 10. Consequences and unresolved implementation decisions

**Benefits:** an inexpensive single-VM controller remains useful; more demanding installations can select HA; no forced rewrite or vendor lock-in; safety invariants survive topology changes; failure isolation and operational proof are first-class.

**Costs:** optional HA introduces PostgreSQL/Patroni/DCS operations, ingress, monitoring, credentials, backup/restore drills and more possible correlated failures. Multiple controller implementations multiply conformance burden. Regional cells require careful recovery and fencing.

**Explicit follow-up ADRs / research decisions:**

1. Exact semantic operation-store interface and bbolt/PostgreSQL compatibility behavior.
2. Transaction and lock schema, monotonic generation fencing, claim renewal/expiry and recovery scan semantics.
3. Database failover policy, strict synchronous settings, Patroni/DCS version and deployment, PITR and keyring backup.
4. Work dispatch boundary and methods to reconcile non-fenceable provider API actions.
5. Monitoring SLI definitions, synthetic probing, freshness-labelled read contracts and auth revocation behavior.
6. Regional cell identity, ownership transfer protocol, DR RPO/RTO, rollback/failback and failure-domain requirements.
7. Whether an independent Elixir/OTP implementation provides measured value without compromising protocol portability.
8. How to establish and independently verify any advertised availability figure.

**Non-decisions:** no requirement to adopt Kubernetes/Agones, Ansible, Pterodactyl, alternative distributed databases, colocation, special hardware or an Elixir rewrite; no declaration that any reliability target has been reached; no enabling of automatic production destruction.

## 11. Proposed acceptance criteria for this ADR

Before marking **Accepted**, maintainers should review that the deployment tiers, safety invariants, initial Go+bbolt investment, optional HA/OTP decisions, per-resource owner model, failure testing, budget tradeoffs and tier-specific SLIs accurately reflect project intent. Reconcile the roadmap regression separately. Acceptance of the *architecture direction* does not authorize deploying production clusters, adding dependencies or announcing achieved SLOs.

Related accepted ADRs remain authoritative where implementation is still limited to standalone. Follow-up implementation PRs require passing checks, security review, conformance and tests, and must preserve existing safe game-server behavior.
