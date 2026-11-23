# ADR 0006: Optional High Availability, Reliability Tiers, and Safe Distributed Ownership

- **Status:** Proposed
- **Date:** 2026-11-22
- **Decision owners:** Project maintainers
- **Scope:** Controller runtime, operation persistence, regional deployment, service-specific reliability, world-data durability, and anti-rollback restore safety. This ADR identifies future protocol work but does not itself change wire schemas or authorize production deployment.
- **Related decisions:** [ADR 0003: VM-first execution](0003-execution-agnostic-runtime.md), [ADR 0004: Protocol encoding](0004-protocol-encoding-and-bindings.md), [ADR 0005: bbolt operation store](0005-operation-store.md)
- **Supporting design:** [Core design](../CORE_DESIGN.md), [Architecture](../ARCHITECTURE.md), [Workflow engine](../WORKFLOW_ENGINE.md), [Security model](../SECURITY_MODEL.md), [Development roadmap](../ROADMAP.md), [Operation protocol](../../protocol/operations.md)

## 1. Context and decisions considered

Game Server Controller (GSC) is a language-neutral, provider-neutral lifecycle controller. The first Go implementation uses a local bbolt operation store, durable operations, actor-scoped idempotency, logical server locks, versioned adapter contracts, and reconciliation of ambiguous provider effects. The concrete reference network uses a persistent Velocity proxy/lobby, with a Paper backend created on demand on a standard Debian VM through Vultr, a verified world restored from Wasabi, and players transferred **only after fresh, exact-instance readiness**. Bootstrap scripts prepare provider-standard OS images; snapshots and custom VM images are not prerequisites. Existing gameplay must continue independently while the controller or database is unavailable. Automatic production VM destruction is currently disabled and remains disabled.

We reviewed several architectures: single Go+bbolt with systemd recovery; active/passive Go+bbolt with replicated local state; three Go controllers with one elected primary and two standby authorities backed by transactional PostgreSQL/Patroni/etcd HA; Elixir/Erlang OTP as an optional additional runtime; independent regional controller cells; selectively replicated read services; and long-term high-assurance/distributed-systems research. We also considered Kubernetes/Agones, application management panels such as Pterodactyl, Ansible, provider snapshots, colocation and alternative databases. These are not all needed for a small deployment of roughly ten Minecraft players.

The design must balance correctness, incremental implementation, operational cost, implementation-language portability, and failure isolation. This ADR formalizes the preferred direction while leaving advanced implementation details for follow-up ADRs. **It does not claim 99.99%, 99.999%, 99.9999% or 99.99999999% has been achieved.**

## 2. Decision summary

1. **Keep a lightweight standalone Go+bbolt controller as the default.** Harden recovery and monitor it. A narrowly scoped four-nines target can be pursued on this profile, but a single VM/disk remains a failure domain, so no availability guarantee is inferred.
2. **Add optional HA with one controller primary and two standby controllers, without changing the public protocol.** Elect a single mutating controller primary backed by a transactional PostgreSQL store, Patroni database failover and quorum-based controller election (initial candidate: three etcd voters). Standbys serve safe reads or forward mutations and may take over only after fencing, recovery and promotion. Keep bbolt fully supported; PostgreSQL is not mandatory.
3. **Make durable resource ownership and fencing the correctness authority.** Keep operation resource locks independent of expiring worker claims, reconcile uncertain side effects, and never claim exactly-once cloud mutations.
4. **Develop Elixir/Erlang OTP, if desired, as a separate conformant implementation or runtime option.** OTP's supervision is useful but not a substitute for durable distributed ownership, transactional authority or provider-side reconciliation.
5. **Treat regional cells as an optional later two-level primary/standby profile.** Each region has one elected controller primary with standby replicas. A logical server/world is owned by **one regional primary cell at a time**; other regions remain standbys *for that world* until explicit fenced transfer. A region may simultaneously own its own worlds and be standby for another region's worlds. No heartbeat-only promotion.
6. **Use service-specific availability targets.** Four nines first, five nines for advanced transactional services, six nines for selected fault-isolated reads or status functions, and ten nines as ongoing research rather than a product SLA.
7. **Keep the gameplay data plane independent of the controller control plane.** Routine controller, database and management-API outages do not automatically stop a healthy Velocity/Paper session. Destructive automation stays disabled until separately approved.
8. **Adopt a gated eleven-nines world-protection product requirement.** Wasabi advertises eleven-nines durability for already stored objects; GSC must separately implement continuous write protection, final complete capture, recoverability, latest-world authority and anti-rollback. The high-assurance profile may not claim its eleven-nines objective before sufficient evidence. No current end-to-end guarantee is implied.

## 3. Architecture alternatives we reviewed

| Design | Decision | Rationale and limits |
| --- | --- | --- |
| Single Go+bbolt controller on one VM, managed by systemd | **Default / implement first** | Cheapest, simplest and compatible with current code. Can restart quickly and reconcile durable operations, but machine, storage and network remain single failure domains. |
| Go+bbolt active/passive with shared file or naïve file replication | **Do not adopt as the default HA design** | bbolt's one-process file ownership does not provide cluster-wide commit consensus. Copying, network-mounting or promoting state without tested fencing risks state rollback and duplicate provider operations. A carefully fenced replication solution may be researched, but is not automatically simpler than PostgreSQL. |
| One elected controller primary plus two warm standby controllers, PostgreSQL/Patroni and quorum DCS | **Preferred optional HA reference design** | Exactly one mutating controller authority per cell, with safe read-only standbys, monotonic fencing, deliberate election and state reconciliation. Database leadership is a separate responsibility. |
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

Use a **single elected primary controller (master authority) and two warm standby controllers (replicas)**. Only the primary can accept new mutating requests or dispatch side effects. Standbys may serve authorized consistent reads or forward writes to the primary without independently acknowledging acceptance. Operational documentation calls these roles **primary/standby**.

~~~text
               Operator / management clients
                           |
                    Redundant ingress
                   /       |       \
             Go A         Go B         Go C
            PRIMARY      STANDBY      STANDBY
            writer       read/relay   read/relay
                   \       |       /
              Controller leadership quorum
                  + monotonic fencing epoch
                           |
                 Transactional store
                           |
                PostgreSQL / Patroni
                 DB primary + 2 standbys
                           |
                   3-member DCS quorum
                   (etcd candidate)
                           |
              PRIMARY controller workflow runner
                           |
             Versioned, exact-target adapters
~~~

**Controller leadership and database leadership are independent.** A Patroni PostgreSQL-primary promotion does not automatically appoint a new *controller* primary. A controller primary also must not accept mutations while the authoritative database is unwritable or unsafe.

| Controller role | Mutations and acceptance | External side effects | Reads |
| --- | --- | --- | --- |
| **Primary** | May accept only after durable authoritative transaction and current-epoch verification | Sole dispatcher of new mutating steps, subject to durable resource locks | Authorized reads |
| **Standby** | Forward to current primary or return an explicit not-primary/retry response | No mutating dispatch, provider deletion, backup pruning, restore, rollback or world promotion | Authorized reads with freshness limits |
| **Former primary / candidate** | No writes until valid new election/fencing | No new effects; preserve and reconcile in-flight evidence | Safe read-only operation |

**Election, failover and failback:**

1. Commit a unique primary identity and **monotonically increasing fencing epoch**, protected by quorum-backed leader election and transactional compare-and-swap. Expired heartbeats alone do not prove the old primary is inactive.
2. Check the current primary epoch before every mutating operation acceptance and *every* external adapter dispatch; preserve atomic per-world resource locks and durable operation intent. An expired worker claim does not release the durable operation lock.
3. **Fence the former primary's ability to issue new side effects** through an appropriately designed dispatch/credential boundary wherever feasible. Database fencing blocks stale commits but cannot revoke an already-sent Vultr, Wasabi or game command. If actual fencing is uncertain, **fail closed**.
4. A standby promotes only after establishing a higher authority epoch, verifying the authoritative PostgreSQL writer and reconciling accepted operations and external effects. Inspect ambiguous provider responses; do not blindly retry provisioning, destruction, restores or player transfers.
5. During quorum loss, ambiguity or leader election, stop accepting/dispatching mutations; retain existing healthy Paper/Velocity gameplay independently. Standbys do not signal authoritative-write readiness.
6. Failback requires a fresh fenced handover, not automatic return of authority to a former primary. Advertise separate process liveness, read readiness and authoritative-write readiness.
7. This is **active/passive for mutations**, not three active mutating workers. Preserve the protocol's language-neutral contracts and the standalone Go+bbolt profile.

### C. Single-VM software QA topology (not production HA)

~~~text
One isolated test VM
  3 separately configured Go controllers
  1 controller primary + 2 standbys
  3 PostgreSQL processes (primary + 2 standbys)
  3 etcd voter processes
  independent local ports, directories and identities
  synthetic provider responses and fault injection
~~~

This proves software orchestration only: primary election and standby promotion, controller process loss, DB promotion behavior, claims, partition simulations, stale-primary fencing and recovery. The single VM, hypervisor, disk and power supply are still common failure domains. Production HA distributes controllers, PostgreSQL nodes, quorum voters and redundant routing across independent hosts/networks when possible.

### D. Optional multi-region: independent authoritative cells with primary/standby authority

Apply the **same primary/standby authority model at two levels**:

1. **Controller authority inside each region:** each cell has **one elected, fenced controller primary** and two warm standby controllers (or an explicitly justified standby count). Each cell maintains an independent transactional database, local quorum and region-specific operation history.
2. **Regional authority for each logical server/world:** **one region is the primary owning cell** for that resource. Other regions are **standby cells for the same resource** and have no right to mutate it until a separately fenced, explicit transfer. One region may own World X and act as standby for World Y simultaneously.

~~~text
                  Global discovery (optional)
                      /             \
              REGION A             REGION B
         +-----------------+  +-----------------+
         | Go A1 PRIMARY   |  | Go B1 PRIMARY   |
         | Go A2 STANDBY   |  | Go B2 STANDBY   |
         | Go A3 STANDBY   |  | Go B3 STANDBY   |
         | PostgreSQL HA   |  | PostgreSQL HA   |
         | local quorum    |  | local quorum    |
         +-----------------+  +-----------------+
                |                    |
           World X OWNER       World X STANDBY
           World Y STANDBY     World Y OWNER
                 \                  /
                Explicit, independently
                 fenced ownership transfer
                 + verified reconciliation
                            |
             Exactly one authorized writer
             per logical server/world globally
~~~

**Two different failovers, two different epochs:**

- **Local controller failover:** e.g. A1 to A2, changing the controller primary **within Region A** while Region A continues to own World X. Requires the single-cell authority, fencing and recovery protocol from Section B.
- **Cross-region resource failover:** e.g. World X's owning cell moves **from Region A to Region B**. This is a distinct change in global ownership, not merely promotion of B1's local controller role. Require a verified cross-region ownership epoch, old-owner/provider/game-writer fencing and durable operation/world-head reconciliation.

**Regional ownership and transfer rules:**

1. Persist a globally unique world lineage and per-resource owning-cell identity, monotonic **regional ownership epoch**, exact authoritative world-head, latest verified backup and protected continuous journal references where supported.
2. **No two regions may mutate the same logical world at once.** Two independent writable regional databases and exchanged heartbeats are not enough to establish cross-region fencing. A shared independent ownership witness/consensus authority or verifiably revoked old-region credentials and writers is required for takeover.
3. Network partition or missed regional heartbeat does not prove that the owner stopped. If old-region control/instance fencing cannot be verified, **block automated takeover** and allow only safe operator-assisted recovery, preserving worlds and evidence.
4. Regional standby promotion must check acknowledged operation history, external provider inventory and authoritative world-head ancestry. An asynchronously replicated copy may be behind; do not silently lose acknowledged mutations or fresh player data. If a zero-loss requirement is in scope, the continuous acknowledged-write protection design and evidence gates of Section 7B.2 apply before enabling it.
5. A newly owning region's elected **local controller primary** first acquires committed cross-region ownership, then reconciles ambiguous effects, stages only a verified authoritative world into an isolated stopped target, and proves the previous game writer fenced before activation.
6. After fresh exact-instance Paper readiness, the authorized Velocity proxy may transfer waiting players. Do not route players to two simultaneous writable instances or an older world.
7. Failback requires a new audited fenced ownership transfer. A recovered former owner remains a **standby** for that world and must not reclaim ownership by restarting its controller.
8. Each healthy region should not need synchronous communication with every other region for routine requests; cross-region *ownership transfer* necessarily needs trustworthy authority/witness or independently verified old-owner shutdown. Mutating clients must reach the **owning region's elected controller primary**; other cells may serve clearly freshness-labelled, authorized read-only status.

This is **independent regional cells with one authoritative owning region per resource**, not an implicitly safe global multi-primary writer or globally writable database. Local and global controller elections, PostgreSQL primary promotion and game-writer fencing must all be kept distinct. Preserving existing gameplay and avoiding false eleven-nines durability promises take priority over fast but unsafe automatic failover.

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
13. **Logical server removal never deletes stored world data.** Removing a server from active controller management must preserve all backups that still exist at archival time, exact version references and the durable recovery/anti-rollback archive. **Scheduled retention for an active server is a separate, explicitly configurable exception:** after verifying a replacement backup, a narrowly scoped retention worker may remove only eligible *unprotected routine* backups. Retention stops upon archival, and the archive/remove action itself never deletes stored data. The storage account operator may separately delete archived data outside the controller, subject to Object Lock.

## 6. Persistence and workflow design

The operation-store abstraction MUST express transactionally enforceable domain semantics, not just generic CRUD: atomic acceptance, actor-scoped dedupe, immutable request/plan snapshots, version-checked updates, durable lock acquisition/release, claim issuance and renewal, claim generations, outcome uncertainty and recovery listing.

- **Go+bbolt today:** one controller process holds a local exclusive file lock, with atomic write transactions under ADR 0005. A periodic due-work scanner must rediscover accepted eligible operations even if the in-memory queue drops a notification. Restart recovery alone is insufficient for liveness.
- **Optional distributed store and primary authority:** PostgreSQL transactions and explicit consistency guarantees; exactly one quorum-elected **controller primary per region** may acknowledge and dispatch mutations, and only for that region's currently owned resources. Mutations must validate local controller epoch and, in multi-region, per-world regional ownership epoch. Standbys never independently acknowledge mutating requests. A client timeout around a commit must be investigated by idempotency lookup rather than risking duplicate acceptance.
- **Durable resource lock versus worker execution claim:** an operation lock remains across claim expiry, worker restarts or takeovers. The next worker must reconcile the previous attempt before causing more external effects.
- **Scheduling:** only the elected primary runs mutating workers for resources owned by its cell; use bounded workers, observable queues, bounded retries, durable per-resource serialization, backpressure and fairness. Standbys do not dispatch mutations. Optional monitoring/notification failure cannot block unrelated safe workflows.
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
- An explicitly **approved deliberate rollback** to an older backup is a separate high-risk operation: show warnings about newer progress and player data that may be replaced, require exact-target operator confirmation, then preserve a fully verified **pre-rollback snapshot of the current live world** in the separate rollback-history pool (Section 7A.4b). Only after that preservation succeeds may a staged, fenced restore be considered. Record approval and rollback provenance and create a **new descendant recovery revision**. Do not silently claim the historical state was newer. Count and report unintended rollbacks separately from approved recovery.
- After fenced activation, validate the expected player/world content and the **fresh exact-instance Paper readiness** before Velocity transfers players. Provider fencing and database CAS alone cannot cancel already dispatched external game writes.

If any source/head relationship or writer authority is unknown, **fail closed**, retain both recoverable copies and request operator reconciliation. The no-stale-activation rule is a hard correctness condition, not a statistical permission to overwrite one world in some number of attempts.

### 7A.4 Wasabi protection and retention design for high assurance

- Write backup objects/manifests using unique, effectively immutable keys, not a mutable live /server prefix as the **only** backup. A mutable current-head pointer, if used, is merely a cache: the authoritative durable head and a protected independent audit anchor govern recovery.
- The high-assurance profile requires **bucket versioning plus appropriately enforced Object Lock/WORM retention**, with least-privilege writers unable to delete or shorten protected versions. Wasabi says Object Lock is enabled at **bucket creation** and needs versioning, so an existing production bucket may require an explicitly approved new protected bucket and safe migration. **Do not assume the current bucket's versioning status or alter live storage in this ADR.**
- Distinguish governance-mode bypass privileges, retention expiry and compromised credentials. Verify lock and retention state on exact object versions, and validate that backups are retrievable before depending on them.
- Keep a separately administered recovery copy if the operator adopts the high-assurance profile (distinct account, region or provider when feasible), with independent retrieval and test-restore drills. This reduces correlated failure exposure but increases storage/egress/cost and does not automatically justify an eleven-nines numerical claim.
- Set both backup **count** and a **minimum protected retention horizon** sufficient for delayed discovery of corruption. The existing policy defaults to **one scheduled backup every 9 hours and retaining the newest 5 verified backups**; allow safe pruning of routine, explicitly unprotected snapshots only after the replacement is fully verified. **`keep_last: 0` disables routine pruning and retains all backups.** Do not prune required current-head/final-recovery/rollback-anchor/dependency snapshots or any remaining archived-server objects. Protected copies may cause retained counts to exceed five. Require operator approval for new storage, Object Lock, retention or replication costs.

### 7A.4a Configurable five-backup routine rotation and protection boundaries

The existing Go `internal/backuppolicy` policy and `configs/controller.example.yaml` **already specify**:

~~~yaml
lifecycle:
  backups:
    schedule:
      every: 9
      unit: hours
    retention:
      keep_last: 5   # default: retain newest five verified routine backups
      # keep_last: 0 # alternative: disable routine pruning, retain indefinitely
~~~

**Current implementation status:** `backuppolicy.Resolve` preserves explicit zero; `PruneCandidates` selects oldest verified, unprotected candidates; callers must invoke it only after the new backup has been verified. **The controller runtime does not yet schedule these backups or perform retention cleanup** (see [configuration](../CONFIGURATION.md)). This ADR states the future behavior; it does not assert that deletions or recurring backups are running now.

For an **active** logical server, when `keep_last: 5` is selected, the intended sequence is:

1. Create backup #6 with a **new immutable backup ID and exact object keys**, never in-place overwrite backup #1 or reuse the same version reference.
2. Verify full game/world capture, source generation/lineage, all required object checksums, upload completion and independent readback. If any step is uncertain or fails, **keep every previous backup**.
3. Refresh authoritative world-head/backup dependencies and classify every version as *ordinary eligible rotation*, *current authoritative recovery*, *final stopped-world copy*, *pinned rollback/migration/restore dependency*, *Object Lock protected*, or *archived*. Any unknown classification is protected by default.
4. Only then may a **separately scoped retention worker** remove the oldest **verified, unprotected routine** backup version(s) needed to meet `keep_last`. Execute exact version-specific, authorized storage deletes with audit and failed/partial-delete reconciliation; never delete a live backup head, required dependency or protected object. If retention or Object Lock forbids a deletion, keep the extra copy; the configured five is a target, **not a hard data-loss-inducing quota**.
5. Treat the backup group and manifest atomically for eligibility: do not prune partial sets or a manifest still needed to reconstruct a world. Never count an unfinished or unverified upload toward the five good backups.
6. When `keep_last: 0`, or when a logical server is archived/removed, issue **no scheduled backup-prune deletes** for that server. A switch to unlimited retention affects future pruning and **does not recover copies already lawfully pruned**.

Scheduled retention is **not** an operator's Remove/Delete Server action. At archival, stop rotation first and preserve all surviving snapshots and the protected archive catalog; do not purge the remaining five simply because the active server listing is removed. Final verified recovery and anti-rollback anchors may be preserved separately from the routine five-copy ring.

For the high-assurance profile, the normal controller/runtime and management API must not possess broad `DeleteObject`, `DeleteObjects`, `DeleteBucket`, lifecycle-expiry-management or retention-bypass authority. **If routine pruning is enabled**, use narrowly constrained, independently auditable maintenance credentials restricted to the eligible routine-backup prefix and exact versions, with protections preventing deletion of the protected archive/final-recovery namespace. Do not promise that Wasabi IAM alone can express all manifest/world-head business rules: the controller must perform authoritative policy checks as well as enforce physical access separation. The high-assurance immutable Object Lock retention term can prevent pruning; do not bypass it to force five copies.

Never equate scheduled pruning with assurance that five surviving snapshots provide end-to-end eleven-nines durability. Protect the current committed head, continuous write journal and independently controlled recovery copies with separate retention rules.

### 7A.4b Configurable pre-rollback recovery history (default five)

Operators MAY explicitly restore an older, verified world. An approved rollback is an intentional recovery action, **not** an exception permitting an unverified or silently stale world to become current. Keep the world that was live immediately **before** each rollback as a **separate, recoverable pre-rollback snapshot**, independent of the nine-hour routine backup ring.

**Configuration proposal (not yet a supported runtime/schema field):**

~~~yaml
lifecycle:
  backups:
    schedule:
      every: 9
      unit: hours
    retention:
      keep_last: 5                  # existing routine schedule policy
    rollback_retention:
      keep_last: 5                  # proposed separate pre-rollback pool; configurable
      # keep_last: 0                # unlimited pre-rollback retention
~~~

- The pre-rollback pool defaults to **five verified snapshots**, but operators MAY choose another nonnegative integer. **Zero means unlimited** and disables automated pruning of rollback snapshots; it does not disable rollback creation. This policy is **independent** of the routine `lifecycle.backups.retention.keep_last` value. A five-copy setting is a *retention target* rather than a limit that forces unsafe deletion.
- **Before the rollback:** show the exact server/environment, selected historical backup ID, source world generation and age, live generation, compatibility changes, and an explicit warning: **"Restoring this older world may replace blocks, inventories, player positions, advancements, statistics, and other progress made after that backup. The current live world will first be saved separately so you can recover it."** Require a fresh, deliberate confirmation bound to that exact request and operator identity, plus existing production approval policies; do not infer approval from another restore or automate it on a stale alert.
- Acquire durable per-world ownership/fencing, drain new joins, quiesce all writers, perform the final acknowledged save/flush, and capture **every required current world/player/plugin data set**. Write a unique **pre-rollback snapshot** under a distinct backup ID/namespace, with full manifest, exact object versions, generation lineage and SHA-256 checksums. Perform independent readback/validation. The snapshot must be recoverable if the later rollback fails. If the live source or backup state is uncertain, **abort without modifying the live world**.
- **Rollback flow:** stage and validate the selected older verified backup in an isolated inactive root; verify world/game compatibility and lineage, retain original live state, and only then activate under an epoch-fenced, crash-recoverable compare-and-swap creating a **new descendant recovery generation**. Report provenance and retained pre-rollback snapshot location. Transfer players through Velocity only after fresh exact-target Paper readiness. If activation fails, preserve both the original live state and the staged target for safe operator reconciliation.
- **Separate retention rotation:** when a sixth eligible pre-rollback snapshot has been successfully verified and committed, the oldest *eligible unprotected* snapshot in this **rollback pool** may be removed, but only after the current rollback completes safely and the newest recovery snapshot is available. **Never overwrite the oldest snapshot in place.** Use new immutable object IDs and exact-version deletes only after current-head, rollback-chain, recovery-anchor, active-restore, Object Lock and dependency checks. If deletion is unsafe, retain more than the configured number. On an interrupted/failed rollback or incomplete new snapshot, **do not prune any prior rollback history**.
- **Unlimited option:** `rollback_retention.keep_last: 0` means retain all subsequent pre-rollback snapshots until the Wasabi account owner takes action outside GSC. Switching to unlimited does not restore copies previously and legitimately pruned.
- **Archived server exemption:** when a logical server is removed from active management, stop **both** scheduled backup rotation and rollback-history rotation. Keep the remaining pre-rollback snapshots alongside the ordinary backups and protected recovery catalog; removing a server never triggers backup deletion.
- For stronger durability, rollback snapshots must remain insulated from ordinary backup pruning. A narrowly scoped, independently auditable **rollback-retention maintenance** permission may delete only unprotected, independently eligible rollback-history versions of an active server. It MUST NOT have delete authority for archived data, current authoritative head, final recovery anchor, or protected Object Lock versions. Retention/policy failures keep extra backups and report storage charges; never bypass safety to enforce a five-copy quota.

**Current implementation boundary:** the repository already implements and tests configurable routine `keep_last: 5` and `keep_last: 0` policy selection; `rollback_retention.keep_last`, its separate inventory, operator warnings, snapshot capture and pruning workflow are **new proposed capabilities**, not implemented configuration or runtime behavior. Add a versioned contract/configuration change, tests and a recovery runbook before shipping them.

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

All unsafe cases must refuse destruction/promotion and retain recoverable evidence; test **approved rollback with warnings, exact-scoped confirmation, separately verified pre-rollback snapshot and independently configurable recovery-history retention (default five, zero unlimited)** separately. Define new versioned schemas, capabilities and conformance vectors for monotonic world heads, ancestry, verified write barriers, retention evidence, exact-version restore, fenced CAS activation and rollback provenance. Preserve the existing language-neutral and Go+bbolt default boundaries.

This section proposes the architecture only. It **does not** enable automatic destruction, change production Wasabi settings, rewrite the protocol without conformance review, provision paid resources, or claim eleven-nines player-data safety has been achieved.


## 7B. Non-destructive logical server removal and no-loss high-assurance release gate

This decision is **mandatory for the product design**, even where eleven-nines end-to-end numerical proof is not yet available. **Deleting a server from controller management is not deleting its world.** A world must remain recoverable until the Wasabi account owner separately performs a storage-side deletion outside Game Server Controller.

### 7B.1 User-visible Delete Server is an archive/decommission operation, not data erasure

- The management UI/CLI may expose a **Remove server** or **Delete server** action, but the backend semantics are **archive/decommission the logical server and hide it from normal active-server lists**. Never interpret that request as authorization to delete Wasabi objects, backup versions, manifests, playerdata or recovery anchors.
- If a VM still exists, a requested provider-instance termination remains a **separate, explicit, exact-target approval** and remains subject to Section 7A.2 final-world quiescence, complete verification and no-stale-world evidence. **Automatic production VM destruction stays disabled.** An archive request must never secretly destroy a VM or interrupt players. It may leave the VM running until a separately approved safe decommission succeeds.
- Once safe decommission is confirmed (or for an already inactive logical server), transition to an **archived/tombstoned management identity**. Remove it from standard active listings and disable new sessions, routine mutations and automatic provisioning; retain a minimal protected, access-controlled recovery catalog containing the logical server ID, environment, world lineage, latest committed head and all necessary exact backup/version/manifests, permissions/audit history and restore instructions. Do **not** erase the metadata required to safely find and recover the world.
- Archived identities and world lineage are reserved against accidental reuse. Recovery or re-import requires authorization, exact owner/lineage checks, versioned head reconciliation and a new safe active generation, not blind recreation under an old or colliding name. Provide a **separate authenticated archived-world recovery view**; archival metadata must be backed up independently of one bbolt database.
- Ordinary controller lifecycle, server-removal, destroy, restore and management-API credentials SHOULD have **no delete/retention-bypass authority** on protected world data. A **separate, narrowly scoped routine-retention worker** may use exact-version S3 deletion solely for eligible verified, unprotected snapshots of an active server when `keep_last > 0` (Section 7A.4a). It MUST NOT delete archived-server data, current/final recovery heads, restore or rollback dependencies, protected Object Lock versions or the backup catalog. Do not enable automatic lifecycle expiry on protected world data. Keep safeguards effective against a compromised ordinary controller process; audit denied attempts.
- For **archived server data and protected final recovery copies**, retain the surviving Wasabi versions indefinitely from GSC's perspective, subject to the storage provider/account continuing to exist, remaining in good standing and honoring its retention. For an **active server's ordinary scheduled backups**, the independent `keep_last` policy can allow rotation as described above. A finite Object Lock period does not guarantee indefinite retention, and expiry does not authorize deletion of archived/protected objects. Warn about account closure, unpaid bills, compromised owner credentials, provider failure and storage-side retention policies.
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
- No deletion of archived, current-head, final-recovery or dependent backup objects through normal lifecycle actions. For **enabled active-server routine rotation only**, independently authorize restricted deletion of eligible unprotected versions **after** complete verification of replacements; test `keep_last: 0`, retention-disabled, protected objects, missing credentials and archived-server non-deletion. Require no-delete IAM on protected namespaces and independent backup/audit catalog recovery after controller DB loss.
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
| PostgreSQL primary lost or old primary isolated | One authoritative database writer; safe Patroni promotion/fencing; no double dispatch or falsely acknowledged commit |
| Controller primary crashes or loses quorum while standbys run | Standbys must not dispatch until elected with a higher fencing epoch and old-primary effects reconciled |
| Former controller primary reconnects after standby promotion | Reject stale-epoch writes and adapter dispatches; no automatic failback |
| Regional owning cell unreachable or split-brain witness fails | Standby region cannot seize that world without old-owner/world-writer fencing, verified state and a committed cross-region ownership epoch |
| Synchronous standby / quorum lost | Explicit safe refusal of writes where required rather than unacknowledged unsafe operation |
| DB commit succeeds but client times out | Determine acceptance by durable idempotency binding, not duplicate insertion |
| Worker claim expires mid-provider request | New worker rejects stale commit and reconciles external outcome before acting |
| Vultr API times out after creating a VM | Inspect provider state and exact resource identity; avoid duplicate instance creation |
| Wasabi upload returns success but readback fails | Backup is not verified; destructive cleanup stays blocked |
| Source continues writing after purported final backup | Final write-barrier proof fails; no destruction or transfer; capture again after quiescence |
| Restore selects stale/incomparable generation | Refuse activation and preserve both worlds for audit and reconciliation |
| Operator removes a logical server from management | Archive and hide from active listings, preserve protected world backup versions and metadata; no S3 DeleteObject or VM destruction without separate approval |
| Normal lifecycle or archive action attempts to delete Wasabi world data | Deny with no-delete IAM and controller policy; never purge archived/protected world objects |
| Sixth nine-hour backup has not completed verification | Keep all previous routine backups; never overwrite the oldest in place |
| Sixth backup verified, active server with keep_last: 5 | Prune only the oldest eligible exact unprotected routine version(s); keep pinned or locked copies even if count exceeds five |
| Routine keep_last: 0 or an archived server | Routine retention worker never schedules routine backup deletes; preserve existing backups and catalog |
| Operator requests rollback to an older world | Warn about replaced blocks/player data, require exact approval, verify a separate pre-rollback live-world snapshot before any activation |
| Sixth pre-rollback snapshot is verified and rollback completes safely | Prune only the oldest eligible unprotected rollback-history snapshot; never touch the five routine backups or pinned recovery anchors |
| rollback_retention.keep_last: 0 | Retain all pre-rollback snapshots and suppress automatic pruning of that pool |
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
- **R2:** design and implement optional transactional PostgreSQL backend, **one controller primary plus two standbys**, claims, quorum election and fencing, three-controller/three-DB/three-etcd single-VM QA; preserve standalone.
- **R3:** distribute optional HA services across independent production failure domains; test primary-controller loss, standby promotion, stale-primary rejection, separate DB failover, multi-region old-owner fencing, failback, rolling upgrades and rollback; target four-nines-capable architecture.
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
6. Local single-primary/two-standby controller election, independent database-primary election, cross-region cell identity and per-world ownership transfer protocol, fencing, witness design, DR RPO/RTO, rollback/failback and failure-domain requirements.
7. Whether an independent Elixir/OTP implementation provides measured value without compromising protocol portability.
8. How to establish and independently verify any advertised availability figure.
9. Ordered world-head revisions, lineage ancestry, verified game-save barriers, anti-rewind anchors, crash-safe fenced activation, CAS and deliberately approved rollback provenance.
10. World-data loss/error-budget model, multi-object completeness, Wasabi Object Lock/versioning profile, **independently configurable routine and pre-rollback retention (each default five and zero unlimited), operator-confirmed rollback versus permanent archive/critical-backup non-deletion**, continuous durability/journaling boundary and independently verified recovery copies.
11. A new versioned, language-neutral server archive/decommission operation and distinct provider-instance destroy operation with correct behavior if final backup verification fails.

**Non-decisions:** no requirement to adopt Kubernetes/Agones, Ansible, Pterodactyl, alternative distributed databases, colocation, special hardware or an Elixir rewrite; no declaration that any availability, eleven-nines world durability or stale-overwrite target has been achieved; no automatic production destruction or live Wasabi setting changes. No lifecycle operation may delete protected world data simply because a logical server is removed.

## 11. Proposed acceptance criteria for this ADR

Before marking **Accepted**, maintainers should review that **single controller-primary/two-standby authority within an HA cell**, **one owning region per logical world with explicit fenced cross-region transfer**, the deployment tiers, nine-hour routine backup rotation (five default, zero unlimited), independently configurable pre-rollback snapshot retention (five default, zero unlimited), pre-rollback warnings and exact operator approval, non-destructive server removal, permanent archived/protected-backup retention, continuous fresh-write durability requirement, final backup and anti-rollback safety design, safety invariants, initial Go+bbolt investment, optional HA/OTP decisions, per-resource owner model, failure testing, budget tradeoffs and tier-specific SLIs accurately reflect project intent. Reconcile the roadmap regression separately. Acceptance of the *architecture direction* does not authorize deploying production clusters, adding dependencies or announcing achieved SLOs.

Related accepted ADRs remain authoritative where implementation is still limited to standalone. Follow-up implementation PRs require passing checks, security review, conformance and tests, and must preserve existing safe game-server behavior.
