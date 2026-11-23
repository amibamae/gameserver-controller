# ADR 0006: Optional High Availability, Reliability Tiers, and Safe Distributed Ownership

- **Status:** Proposed
- **Date:** 2026-11-22
- **Decision owners:** Project maintainers
- **Scope:** Controller runtime, operation persistence, regional deployment, service-specific reliability, world-data durability, and anti-rollback restore safety. This ADR identifies future protocol work but does not itself change wire schemas or authorize production deployment.
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
8. **Adopt a gated eleven-nines world-protection product requirement.** Wasabi advertises eleven-nines durability for already stored objects; GSC must separately implement continuous write protection, final complete capture, recoverability, latest-world authority and anti-rollback. The high-assurance profile may not claim its eleven-nines objective before sufficient evidence. No current end-to-end guarantee is implied.

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
12. **Latest world protection.** No approved destruction may proceed without a verified final backup matching the source write barrier; no stale/incomparable backup may replace an authoritative world. Failure or uncertainty retains the current VM and recoverable copies.
13. **Logical server removal never deletes stored world data.** Removing a server from active controller management must preserve verified Wasabi backups, exact version references and the durable recovery/anti-rollback archive. The normal controller lifecycle never invokes object deletion, retention expiration or backup garbage collection. Only the storage account operator can separately decide to delete retained data outside the controller, with the limitations of Object Lock.

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

## 7A. World-data durability and monotonic, anti-rollback restore safety

This is a **separate reliability objective** from service availability. Protect two distinct outcomes: (a) the last *committed and acknowledged* player/world state survives approved source-VM destruction; and (b) no older, stale, conflicting or incomparable world silently replaces a newer authoritative world. Both are safety invariants we must design and test. **99.999999999% (eleven-nines) end-to-end world retention and no-unintended-rollback are explicit high-assurance product release targets.** A release may not advertise conformance to those targets until the underlying capture, continuous-write, retention and restore guarantees have been implemented, modeled and independently validated. They are **not achieved or guaranteed today**.

### 7A.1 Scope of Wasabi's advertised eleven nines

Wasabi's documentation states that its storage is *designed for* **99.999999999% durability of already stored objects over a year**. The provider separately recommends backups, tested recovery and safeguards against accidental or malicious actions. This object-level claim does not establish end-to-end Minecraft save correctness, complete upload, protection against deletion/overwrite, freshness of restored worlds, or successful recovery.

Authoritative references:
- [Wasabi: Durability, Security and Reliability](https://docs.wasabi.com/docs/durability-security-reliability) (updated May 2026)
- [Wasabi: Shared Responsibility Model](https://docs.wasabi.com/docs/shared-responsibility-model)
- [Wasabi: Object Lock requirements](https://docs.wasabi.com/docs/object-locking)
- [Wasabi: Bucket Versioning](https://docs.wasabi.com/docs/bucket-versioning-enabledisable)

A world often contains multiple required objects, all of which must survive and remain mutually consistent. The survival probability of one Wasabi object is not automatically that of the *whole world*. A conservative decomposition is:

~~~text
P(loss of last committed player/world state)
  <= P(inconsistent or missed final save)
   + P(incomplete capture / upload / verification)
   + P(loss of a required stored object)
   + P(accidental, malicious or policy-driven deletion)
   + P(stale overwrite, incorrect restore or promotion)
   + P(other correlated failure of recovery/credentials/control)
~~~

This expression is a union-bound illustration; no probabilities are currently established. Adding nonzero application risks to a component advertised at an eleven-nines target does **not** prove an eleven-nines end-to-end result. Extra independent recovery copies and fault-tolerant checks may improve safety; do not assume probability independence or promise an unmeasured aggregate.

### 7A.2 Mandatory final-state backup gate before any manually approved destruction

For a planned VM replacement or an **explicitly approved** destruction, the controller MUST:

1. Take and retain the durable logical-world operation lock, verify exact environment/server/instance identity and current fenced writer authority, and prevent concurrent operations that could modify the world.
2. Drain new joins, persist an acknowledged game-specific save, and **stop all game and plugin writers**. Verify that Paper/systemd and auxiliary persistence processes have stopped. Prefer a fully **offline** final capture; if the game cannot prove a consistent quiescent snapshot, do not call the backup final.
3. Capture **all required persistent data**, including dimensions, playerdata and inventories, ender chests, advancements, statistics and declared plugin/mod state. The capture must bind the exact source instance, world lineage/generation, game release, required data-set inventory and a verified final write barrier.
4. Upload under unique non-reused backup/object names, create an immutable backup manifest, and verify all object lengths, independent SHA-256 hashes, exact object-version references where supported and game-specific validation. A successful PUT, HEAD or ETag alone is not enough; ETag is not a guaranteed content hash.
5. Retrieve and check the exact stored objects/versions. Perform independent isolated *test restores* on a defined schedule, and before introducing any new destructive execution mechanism; verify an internally consistent startable world with the expected player state.
6. Establish **latest finalized-state equality**: after the final save barrier, no source writes are permitted; the backup corresponds to the current authoritative world head and matches independent source evidence. A backup taken before subsequent player activity, even if the most recent object in Wasabi, is NOT a valid final backup.
7. Atomically persist the verified backup association and final-state evidence. **Immediately before** a provider destruction request, recheck exact instance, authorization, world head, fencing epoch, write barrier, protected objects and recovery evidence. If any check changed or is inconclusive, **fail closed and retain the VM**.
8. Keep **automatic production destruction disabled**; passing these gates does not itself authorize destruction. If the provider response is uncertain, inspect that exact instance rather than blindly issuing another delete.

These gates target **zero loss of acknowledged saved state during a successfully quiesced planned destruction**. They do not establish RPO zero for unexpected VM failure, unflushed writes, game/plugin implementation bugs or storage objects never uploaded. Define and measure those distinct failure cases separately.

### 7A.3 Ordered world generations, lineage and no stale promotion

The current protocol provides a backup manifest with a data_generation_id and checked object references. However, that ID is **not a sortable generation counter**. The optional game write_watermark is explicitly **informational only** because draft 0.21.0 has no interoperable verifier; see [game protocol](../../protocol/adapters/games.md) and [backup manifest](../../protocol/schemas/storage/backup-manifest.schema.json). A timestamp, S3 version ID or higher game version also cannot establish world recency.

**Proposed new, versioned, language-neutral authority model**, not yet implemented:

~~~text
world_id / environment_id
lineage_id and parent_generation
ownership_epoch (monotonic fenced writer authority)
head_revision (monotonic committed world-state transition)
verified_write_barrier (source game-save proof)
head_backup_id + immutable manifest digest + object versions
active_instance_ref
compare_and_swap_revision (authoritative store transaction)
~~~

- Each logical world has **exactly one authorized writer**. Acquire authority using durable locks and explicit fencing. Never interpret worker timeout, remote heartbeat loss or host unreachability as proof the old runtime stopped.
- Under the authoritative store, commit monotonic head revisions, a comparable ancestry/parent chain and a verified source-save barrier. If the store is restored from an older backup, reconcile with an **independently retained protected generation anchor**, source-instance state and backup evidence before accepting mutating work. A rolled-back database must not silently reset world recency.
- On **normal restart, recovery, migration and cell takeover**, choose only the *designated current verified authoritative head*. Do **not** choose the backup with the latest timestamp, arbitrary lexical UUID order, highest Wasabi version or a mismatched/incomparable lineage.
- Restore only into a **fresh, empty, isolated target root** with Paper stopped and no writers. Verify complete manifest, provenance, lineage, source generation, compatibility and content. Never unpack directly over an actively running or newer local world. An *equal-generation* restore into a new empty VM can be correct/idempotent and is not a stale overwrite.
- Make publish/cutover a **fenced compare-and-swap** against the expected head revision and writer epoch. Handle crashes between filesystem activation, head commit and proxy transfer through a durable recoverable handoff record; do not serve two authoritative writable runtimes.
- A live server with unbacked writes is newer than an earlier checkpoint, even if the earlier backup has a more recent upload timestamp. Block any replacement, quiesce it, capture/verify the final state and only then consider migration.
- An explicitly **approved deliberate rollback** to an older backup is a separate break-glass operation: first preserve a verified copy of the current higher state, record approval and rollback provenance, and create a **new descendant recovery revision**. Do not silently claim the historical state was newer. Count and report unintended rollbacks separately from approved recovery.
- After fenced activation, validate the expected player/world content and the **fresh exact-instance Paper readiness** before Velocity transfers players. Provider fencing and database CAS alone cannot cancel already dispatched external game writes.

If any source/head relationship or writer authority is unknown, **fail closed**, retain both recoverable copies and request operator reconciliation. The no-stale-activation rule is a hard correctness condition, not a statistical permission to overwrite one world in some number of attempts.

### 7A.4 Wasabi protection and retention design for high assurance

- Write backup objects/manifests using unique, effectively immutable keys, not a mutable live /server prefix as the **only** backup. A mutable current-head pointer, if used, is merely a cache: the authoritative durable head and a protected independent audit anchor govern recovery.
- The high-assurance profile requires **bucket versioning plus appropriately enforced Object Lock/WORM retention**, with least-privilege writers unable to delete or shorten protected versions. Wasabi says Object Lock is enabled at **bucket creation** and needs versioning, so an existing production bucket may require an explicitly approved new protected bucket and safe migration. **Do not assume the current bucket's versioning status or alter live storage in this ADR.**
- Distinguish governance-mode bypass privileges, retention expiry and compromised credentials. Verify lock and retention state on exact object versions, and validate that backups are retrievable before depending on them.
- Keep a separately administered recovery copy if the operator adopts the high-assurance profile (distinct account, region or provider when feasible), with independent retrieval and test-restore drills. This reduces correlated failure exposure but increases storage/egress/cost and does not automatically justify an eleven-nines numerical claim.
- Set both backup **count** and a **minimum retention horizon** sufficient for delayed discovery of corruption. Retaining five recent copies may be cost-effective for baseline deployments, but cannot prove survival of a correct generation if all copies stem from an earlier corrupted state. **Do not automatically purge any retained world backups or archived server data**, even after lock retention expires or the logical server is removed. Require operator approval for new storage, Object Lock, retention or replication costs.

### 7A.5 Separate eleven-nines objectives and measurable safety rules

| Property | Proposed measure | Honest claim boundary |
| --- | --- | --- |
| Wasabi stored-object survival | Provider designs for **99.999999999% annual per-object durability** | Not a GSC or player-data SLA; does not prove availability, uploading or recovery |
| End-to-end recoverability of a verified world | **Eleven-nines high-assurance release acceptance target** for correctly defined committed world state | Do not advertise as achieved before independent restore evidence, continuous write protection, quantified risk, retained immutable histories and defensible statistical/model evidence |
| No unintended stale promotion | **Eleven-nines high-assurance release acceptance target**; operational rule is **never knowingly activate stale or incomparable worlds** | Prove ordering, fencing and CAS safety by model checking, injected failures and independent audits; do not claim eleven-nines from a short test suite |
| Approved planned destroy | **100%** of attempts must satisfy the final stopped-world verification gate | An invariant on allowed actions, not a statement that eleven nines has been measured |
| Sudden VM/disk loss | Separately define and measure RPO/RTO | May lose changes since last confirmed durable backup; Wasabi object durability does not cover unbacked state |
| Active-world replacement | No online/in-place overwrite, no stale or incomparable lineage | Require fresh isolated target, verified current head, CAS and safe game/proxy cutover |

Numerically, an eleven-nines failure target is roughly **1 in 100 billion** qualifying cases. Normal Minecraft server traffic, years of clean operation and a few thousand restores cannot empirically establish it. Report **architecture complete**, **safe under defined injected faults**, and **quantitatively demonstrated** separately; the last milestone has no defensible fixed delivery date.

### 7A.6 Required adversarial tests and follow-up protocol work

Inject: missing playerdata; missed flush; plugin writes after final save; upload succeeded but readback failed; incomplete manifest; wrong version ID; checksum mismatch; mutable /server overwrite; Object Lock absent/expired; retention deletes needed ancestor; stale source chosen by timestamp; incomparable lineage; simultaneous writers; lost claim during restore; database backup rewinding head; old VM reconnecting after a promotion; provider outage; and crashes between staging, activation, transactional head update and Velocity routing.

All unsafe cases must refuse destruction/promotion and retain recoverable evidence; test **approved rollback with preservation of the pre-rollback state** separately. Define new versioned schemas, capabilities and conformance vectors for monotonic world heads, ancestry, verified write barriers, retention evidence, exact-version restore, fenced CAS activation and rollback provenance. Preserve the existing language-neutral and Go+bbolt default boundaries.

This section proposes the architecture only. It **does not** enable automatic destruction, change production Wasabi settings, rewrite the protocol without conformance review, provision paid resources, or claim eleven-nines player-data safety has been achieved.


## 7B. Non-destructive logical server removal and no-loss high-assurance release gate

This decision is **mandatory for the product design**, even where eleven-nines end-to-end numerical proof is not yet available. **Deleting a server from controller management is not deleting its world.** A world must remain recoverable until the Wasabi account owner separately performs a storage-side deletion outside Game Server Controller.

### 7B.1 User-visible Delete Server is an archive/decommission operation, not data erasure

- The management UI/CLI may expose a **Remove server** or **Delete server** action, but the backend semantics are **archive/decommission the logical server and hide it from normal active-server lists**. Never interpret that request as authorization to delete Wasabi objects, backup versions, manifests, playerdata or recovery anchors.
- If a VM still exists, a requested provider-instance termination remains a **separate, explicit, exact-target approval** and remains subject to Section 7A.2 final-world quiescence, complete verification and no-stale-world evidence. **Automatic production VM destruction stays disabled.** An archive request must never secretly destroy a VM or interrupt players. It may leave the VM running until a separately approved safe decommission succeeds.
- Once safe decommission is confirmed (or for an already inactive logical server), transition to an **archived/tombstoned management identity**. Remove it from standard active listings and disable new sessions, routine mutations and automatic provisioning; retain a minimal protected, access-controlled recovery catalog containing the logical server ID, environment, world lineage, latest committed head and all necessary exact backup/version/manifests, permissions/audit history and restore instructions. Do **not** erase the metadata required to safely find and recover the world.
- Archived identities and world lineage are reserved against accidental reuse. Recovery or re-import requires authorization, exact owner/lineage checks, versioned head reconciliation and a new safe active generation, not blind recreation under an old or colliding name. Provide a **separate authenticated archived-world recovery view**; archival metadata must be backed up independently of one bbolt database.
- Controller lifecycle credentials SHOULD be **read/write-new-only, no-delete and no-retention-bypass** on the protected Wasabi bucket. No normal server lifecycle, backup rotation, server removal, destroy, restore, operator API call or controller retention worker may issue an S3 DeleteObject/DeleteObjects/DeleteBucket, unprotected overwrite of an existing version, or automatic lifecycle-expiration policy for protected world objects. Privilege barriers must remain effective despite a compromised controller process. Audit denied attempts.
- Retain Wasabi world backups **indefinitely by default from GSC's perspective**, subject to the storage provider/account actually continuing to exist, remaining in good standing, and honoring its retention settings. A finite Object Lock interval does not equal indefinite guaranteed retention; after a lock expires, GSC still must not delete objects. Warn clearly that account closure, unpaid bills, compromised owner credentials, provider failure and storage-side retention policies remain risks.
- At removal, display a clear notice: **"The server has been removed from active management. Your world, player data and verified backups remain in Wasabi and may continue incurring storage charges. Game Server Controller does not delete these files. If you want to reduce storage usage, manage and delete them yourself in your Wasabi account after independently confirming you no longer need them. Object Lock may prevent deletion until the retention period ends."** Never provide a controller action that secretly clears the backup repository.
- Use versioned protocol operations/schemas to distinguish **server decommission/archive** from existing **server.destroy** (which currently concerns provider-instance destruction). Do not silently change the current protocol's meaning, add server deletion to implemented commands, or make a generic storage adapter's optional delete capability a controller-level permission.

### 7B.2 Fresh, not-yet-backed-up player data needs continuous protection

An offline final backup protects **planned** VM destruction, but cannot prevent loss of fresh gameplay changes if a VM dies before its next save/upload. **Periodic Wasabi backups cannot satisfy a zero-loss fresh-data promise.**

The high-assurance profile must choose and validate an end-to-end **write-commit boundary** and a safe continuous durability mechanism. The required property is: *no world/player-data change may be acknowledged as durable to players or used as the authoritative world head before it is protected by a verified, sufficiently independent durable record from which it can be replayed or recovered.*

Candidate implementation approaches for a future design ADR:

1. Instrument game-specific write/save events and all declared persistent data sets with a **durable ordered write-ahead event journal** or versioned state-delta stream; ensure commits and ordering are bound to world lineage, writer epoch, checkpoints and atomic generation commits.
2. Replicate that journal/checkpoint to **independent failure domains**, with a tested confirmation/acknowledgment protocol and durable recovery of player inventories, blocks, entities and configured plugin/mod state. A remote storage request reaching a server is not proof that all game changes were durably captured.
3. For stronger zero-RPO semantics, synchronously await the required independent durable commit **before reporting the corresponding write as durable**. If the durability quorum/storage cannot be reached, pause new authoritative writes, reject admission or safely stop the game: **fail closed, never continue accepting unprotected changes while claiming zero-RPO**.
4. Periodically take full consistent snapshots, seal journal segments, verify complete exact-object and SHA-256 manifests, replay from a snapshot into a clean isolated Paper server, and compare against known committed generation and player-data state before promotion.
5. Evaluate latency and player experience, game tick semantics and failure modes before selecting a journal mechanism. **Vanilla Paper may not expose a transactional acknowledgment hook for every world/player/plugin mutation.** If it is impossible to intercept and durably acknowledge all relevant state changes, do not represent it as a zero-loss end-to-end mode or claim eleven-nines player-data protection. A proxy ACK or controller event is not a proof of durable Minecraft world state.
6. Separate outage and write refusal from corruption/loss SLIs. Stronger synchronous protection can lower *availability* when the storage dependency is unavailable; gameplay should not falsely continue under an unprotected "all changes are safe" assertion. Document expected overhead, replicated copy budget and cost.

This is a design requirement and possible implementation path, **not a statement that the current Go+bbolt + Paper + Wasabi deployment already provides synchronous committed writes**. The default lightweight deployment remains supported and must accurately disclose the maximum unprotected write interval (RPO), whereas any separately advertised eleven-nines high-assurance mode must pass its strict release evidence gates.

### 7B.3 High-assurance release gates, not marketing promises

Before any deployment is marketed as meeting **99.999999999% end-to-end player-world durability** or **99.999999999% no-rollback correctness**, require:

- A written and reviewed **complete player-data inventory** and explicit acknowledged-commit definition for every supported game/version/plugin scope. Unprotected plugin data must be excluded with disclosure or the mode must be refused; do not silently ignore it.
- No controller-originated backup deletion through any normal workflow; protected retention and no-delete IAM demonstrated by policy and negative tests, with independent backup/audit catalog recovery after controller DB loss.
- Crash-safe generation/CAS authority and fencing, stale worker rejection, anti-rewind checkpoint and sealed lineage evidence, tested under partitions, old-primary recovery and target/instance replacement.
- Isolated restoration of last acknowledged writes, including recent changes after full snapshot but before crash, from snapshots plus any required continuous durable journal.
- Formal safety models and adversarial injection covering multiple correlated faults, backup retention mistakes, racing operations, version skew, unintended rollback and controller compromise. Store/provider IAM must also prevent accidental deletion if the operator misclicks *inside GSC*.
- A quantitative error budget, correlated-failure assumptions, measurement methodology, independent security/architecture review and credible operational evidence. One success in a few test restores **cannot establish** a one-in-100-billion failure probability. Until this gate is met, describe the mode as **under development**, not "eleven nines achieved."
- An archive/remove-server acceptance test that proves the active list no longer shows the server while all Wasabi backup object versions, manifests and recovery catalog records remain available; failed final backup must leave the VM and world untouched and report the blocker.

For a strict durability target, unknown outcome is a **release blocker**, not permission to downgrade policy or extrapolate Wasabi's per-object number. Continuous protection is a separate challenging research/engineering track, not a dependency of the initial simple Go+bbolt operator-controlled workflow.


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
| Source continues writing after purported final backup | Final write-barrier proof fails; no destruction or transfer; capture again after quiescence |
| Restore selects stale/incomparable generation | Refuse activation and preserve both worlds for audit and reconciliation |
| Operator removes a logical server from management | Archive and hide from active listings, preserve protected world backup versions and metadata; no S3 DeleteObject or VM destruction without separate approval |
| Normal lifecycle attempts to delete Wasabi data or retention expiry automation | Deny with no-delete IAM and controller policy; never purge protected world objects |
| Active Paper VM fails after acknowledged change but before next full snapshot | In high-assurance mode recover from independently durable continuously committed journal; otherwise disclose actual RPO and do not claim zero-loss |
| Database restore rewinds world-head revision | Detect independently protected head evidence; block mutations and reconcile before promotion |
| Crash during staged restore, CAS promotion or Velocity transfer | Recover exact writer/head state, never serve competing authoritative writable worlds |
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
- **R1:** harden standalone Go+bbolt: restart, durable rescheduling, safe reconciliation, backup and restoration tests, independent monitoring. Require final stopped-world backup and exact-generation verification before any separately approved destructive workflow.
- **R2:** design and implement optional transactional PostgreSQL backend, claims, fencing, three-controller/three-DB/three-etcd single-VM QA; preserve standalone.
- **R3:** distribute optional HA services across independent production failure domains, test primary loss, failback, rolling upgrades and rollback; target four-nines-capable architecture.
- **R4:** eliminate avoidable shared dependencies; improve critical five-nines availability and operational evidence.
- **R5:** implement selectively isolated six-nines reads and historical status, independently validate their service contracts; evidence-gated success. Independently build and validate continuous write protection, permanent no-controller-delete archival, eleven-nines world-generation recoverability and no-stale-world promotion, without equating Wasabi's object claim to end-to-end guarantees.
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
9. Ordered world-head revisions, lineage ancestry, verified game-save barriers, anti-rewind anchors, crash-safe fenced activation, CAS and deliberately approved rollback provenance.
10. World-data loss/error-budget model, multi-object completeness, Wasabi Object Lock/versioning profile, permanent controller-side no-delete/archive semantics, continuous durability/journaling boundary and independently verified recovery copies.
11. A new versioned, language-neutral server archive/decommission operation and distinct provider-instance destroy operation with correct behavior if final backup verification fails.

**Non-decisions:** no requirement to adopt Kubernetes/Agones, Ansible, Pterodactyl, alternative distributed databases, colocation, special hardware or an Elixir rewrite; no declaration that any availability, eleven-nines world durability or stale-overwrite target has been achieved; no automatic production destruction or live Wasabi setting changes. No lifecycle operation may delete protected world data simply because a logical server is removed.

## 11. Proposed acceptance criteria for this ADR

Before marking **Accepted**, maintainers should review that the deployment tiers, non-destructive server removal, permanent protected backup retention, continuous fresh-write durability requirement, final backup and anti-rollback safety design, safety invariants, initial Go+bbolt investment, optional HA/OTP decisions, per-resource owner model, failure testing, budget tradeoffs and tier-specific SLIs accurately reflect project intent. Reconcile the roadmap regression separately. Acceptance of the *architecture direction* does not authorize deploying production clusters, adding dependencies or announcing achieved SLOs.

Related accepted ADRs remain authoritative where implementation is still limited to standalone. Follow-up implementation PRs require passing checks, security review, conformance and tests, and must preserve existing safe game-server behavior.
