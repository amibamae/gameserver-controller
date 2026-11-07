# Durable Operation Protocol

**Status:** Draft, version 0.21.0-draft. This document defines language-independent operation semantics. JSON schemas and RPC/management bindings are published separately.

The operation protocol is shared by the controller core, adapters, command-line tools, and future management clients. Implementations in different languages MUST preserve the behavior defined here.

## 1. Purpose and scope

Game Server Controller performs work that can outlast the request that started it. Provisioning, startup, shutdown, backup, restore, updates, and destruction may involve multiple external systems, long waits, partial completion, or loss of connectivity.

The operation protocol defines how that work is accepted, recorded, observed, cancelled, retried, and recovered.

It applies to:
- Mutating lifecycle and administrative actions.
- Long-running read-only actions when they need durable progress or recovery.
- Multi-step workflows coordinated by the controller.
- Adapter calls that participate in a durable controller operation.

A short, read-only query MAY be handled as an ordinary request rather than a durable operation. A mutating action MUST be represented by a durable operation.

## 2. Fundamental guarantees

1. **Persist before side effect:** The controller MUST durably record an accepted operation before initiating any external side effect.
2. **No untracked work:** An adapter MUST NOT initiate work on behalf of the controller without an associated operation and step context.
3. **No exactly-once assumption:** The protocol does not promise exactly-once execution across process, network, or provider boundaries.
4. **Reconcile ambiguity:** If an external call may have taken effect but its outcome is unknown, the operation MUST enter reconciliation. It MUST NOT blindly repeat the call.
5. **Durable state:** A controller restart MUST NOT erase accepted operations or make unfinished work appear complete.
6. **Safe cancellation:** A cancellation request is not proof that work stopped or side effects were reversed.
7. **Explicit partial outcomes:** Failed or interrupted operations MUST preserve the record of completed steps and known external effects.
8. **Core policy remains authoritative:** Adapters and clients cannot override authorization, approval, environment isolation, or destructive-operation safeguards.
9. **Observable progress:** A client can retrieve the current operation state without depending on a live process connection.
10. **Language and transport neutrality:** These semantics do not depend on a programming language, serialization format, or transport.

## 3. Operation model

An **operation** is a durable record of one accepted request to perform a defined action.

An operation has:
- A unique `operation_id`.
- An operation type, such as `server.provision`, `server.backup`, or `server.destroy`.
- A schema/protocol version identifying the contract used.
- A target and scope, including `server_id` and `environment_id` when applicable.
- The authenticated actor and authorization decision.
- A request snapshot containing validated parameters and secret references only.
- An optional idempotency key.
- Creation, update, start, and completion timestamps as applicable.
- Current operation status and current step.
- A sequence of step records and relevant external references.
- Deadline and retry policy information.
- Progress information, if meaningful.
- A result, warnings, or structured error.
- Audit event references and recovery metadata.

Operation request parameters MUST be retained in a way that lets the controller safely resume or reconcile the accepted request. Resolved secret values MUST NOT be stored in the request snapshot, operation result, or audit trail.

The operation's accepted request is immutable. A changed target, changed parameter, or materially changed plan requires a new operation and, where applicable, a new approval.

## 4. Operation acceptance

Before accepting an operation, the controller MUST:
1. Authenticate the caller and establish trusted actor identity.
2. Validate the request and its target/environment scope.
3. Check authorization, policy, required capabilities, and approval requirements.
4. Validate that the request is not in conflict with an existing operation or resource state.
5. Resolve or validate secret references only as necessary, without exposing secret values.
6. Create the operation record and initial audit record in durable storage.
7. Commit the record before scheduling or dispatching work that can cause side effects.

If validation, authorization, or safety checks reject the request before acceptance, the controller returns a structured request error and MUST NOT start the operation. A security/audit record MAY still be written for the rejected attempt.

Acceptance means the operation has been durably recorded. It does **not** mean the requested action has completed or that all provider-side effects have begun.

If durable operation storage is unavailable, a mutating request MUST fail closed. The controller MUST NOT perform the action without a durable operation record.

After acceptance, the caller receives the operation identifier and an initial status, or can recover that identifier using the same idempotency key if the acceptance response was lost.

## 5. Operation status model

The proposed operation statuses are:

| Status | Meaning | Classification |
|---|---|---|
| `pending` | Accepted and durably recorded; execution has not started | Non-terminal |
| `running` | A step is actively executing | Non-terminal |
| `waiting` | Waiting for a provider, timer/backoff, external event, or other prerequisite | Non-terminal |
| `cancellation_requested` | A cancellation request was accepted for evaluation; work may still be running | Non-terminal |
| `reconciling` | An external outcome or interrupted step is being investigated before proceeding | Non-terminal |
| `needs_operator` | Safe automation is paused until an authorized operator resolves or directs the situation | Suspended |
| `succeeded` | The requested outcome and required postconditions were confirmed | Terminal |
| `failed` | The operation definitively stopped without achieving its requested outcome | Terminal |
| `cancelled` | The operation stopped under its cancellation rules and no further work will be performed by it | Terminal |

Only `succeeded`, `failed`, and `cancelled` are terminal. `needs_operator` is suspended, not successful or failed; a documented, authorized operator action MAY resume it or explicitly end it. Implementations MUST NOT silently resume a suspended operation.

An implementation MUST NOT report `succeeded` merely because an adapter accepted a request. It must verify the required postconditions for the operation type.

An operation MUST NOT transition from a terminal state back to an active state. Further work requires a new operation linked to the prior operation when relevant.

## 6. Steps and external effects

A multi-step operation is represented as ordered, individually identifiable steps. A step record SHOULD include:
- Stable step identifier and step type.
- Intent/parameters with secret references only.
- Step status and timestamps.
- Attempt count and applicable deadline.
- Adapter identity and capability used.
- External operation/resource references.
- Observed result, structured error, and recovery information.

Before an external side effect, the controller MUST durably record the step intent and attempt context. After the call, it MUST durably record the observed outcome before proceeding to dependent steps.

Proposed step statuses:

| Step status | Meaning |
|---|---|
| `pending` | Step is planned but has not been dispatched |
| `running` | Dispatch or execution is in progress |
| `waiting` | Waiting for an asynchronous external result or prerequisite |
| `outcome_unknown` | The external effect may have occurred; reconciliation is required |
| `succeeded` | Step postcondition is confirmed |
| `failed` | Step definitively failed |
| `skipped` | Step was not required or was safely bypassed under the workflow definition |
| `cancelled` | Step was stopped according to its cancellation semantics |

An adapter's synchronous response may indicate that an external provider accepted a request and returned a provider operation reference. That is not necessarily completion. The controller records the reference and polls, waits for an event, or otherwise reconciles until the defined postcondition is observed.

External resource identifiers MUST be recorded with their adapter and environment scope as defined in [common protocol types](common-types.md).

## 7. Idempotency and duplicate requests

Idempotency prevents a lost response or client retry from unintentionally starting the same operation more than once.

For management HTTP operation-acceptance requests, the caller MUST supply the `Idempotency-Key` header. Its value MUST contain 1–128 visible ASCII characters (U+0021 through U+007E), is case-sensitive, and MUST be scoped to the authenticated actor. The key is a deduplication identifier, not an authentication credential. Clients SHOULD use at least 128 bits of randomness.

The controller MUST bind the actor and key to the operation type, target, environment, and RFC 8785 JCS fingerprint of the canonical validated request parameters. It MUST reject a same-actor reuse with different bound request data using `core.operation.idempotency_conflict`. The same actor, key, and request MUST identify the original operation while its full record is retained.

The actor/key association MUST be durable. It MUST remain associated for the entire non-terminal operation lifetime and MUST be retained with the complete operation record for at least 90 days after the operation becomes terminal. After that period, the controller MAY discard the full operation and request mapping, but MUST retain a privacy-minimized tombstone for the actor/key pair for the lifetime of the active controller installation, including restart, storage migration, and recovery from backup. A tombstone MUST retain only an opaque verifier and any metadata required to evaluate it; it MUST NOT retain request parameters, actor display data, or the full operation record. Implementations MUST restrict verifier access to idempotency lookup and recovery. A matching request after full-record expiry MUST be rejected with `core.operation.idempotency_key_expired`; it MUST NOT replay the old operation or create or dispatch a new one. Tombstones may be removed only when the controller installation is permanently retired and cannot be resumed from its durable state or recovery backups.

An HMAC-SHA-256 verifier over an unambiguous length-prefixed encoding of the authenticated actor ID and key is a RECOMMENDED implementation. An HMAC-based tombstone MUST include a key-version identifier, and the protected verification keyring MUST be preserved through migration and recovery backups. Key rotation MUST retain old verification keys for as long as any tombstone uses them. If a required verification key is lost, the controller MUST fail closed and MUST NOT accept or dispatch mutating requests whose idempotency state cannot be checked; it MUST restore the keyring or permanently retire the installation. This keyed verifier avoids retaining raw keys and resists offline guessing; other designs MAY be used if they provide equivalent privacy minimization and durable lookup semantics.

This management key does not by itself make an external provider call idempotent. Adapter capabilities and operation contracts MUST describe whether a provider action supports a distinct provider idempotency token, safe lookup/reconciliation, or neither. A provider token, if used, is scoped and interpreted according to that capability contract.

The protocol MUST NOT claim exactly-once execution. If a call may have reached an external system and the response is lost, the controller records an unknown outcome and reconciles before retrying or issuing dependent actions.

## 8. Timeouts, deadlines, and retries

The protocol distinguishes:
- **Request timeout:** how long a caller waits for an immediate response.
- **Step timeout:** how long the controller waits for a specific local/adapter interaction.
- **Operation deadline:** the absolute time by which the controller should stop initiating ordinary work and move to a defined expiry/recovery path.
- **Provider deadline:** a provider-side expiry or timeout, if the provider supports one.

A timeout describes the controller's wait, not necessarily the external action. An operation timeout MUST NOT be interpreted as proof that a provider-side action was cancelled, failed, or reversed.

Retry rules:
1. Each implementation or adapter profile MUST declare bounded retry and backoff policies where automatic retry is supported; no universal duration or attempt count is imposed by the protocol.
2. Backoff and provider retry guidance SHOULD be respected.
3. An action MAY be retried automatically only when it is known to be safe to repeat, protected by a provider idempotency mechanism, or reconciled to prove the prior attempt did not take effect.
4. A mutating call with an unknown outcome MUST be reconciled before retry.
5. Validation, authorization, unsupported-capability, integrity, compatibility, and safety-blocked errors MUST NOT be automatically retried without a changed request or explicit policy.
6. If an explicit safe retry policy is absent, the default is zero automatic retries for a mutating adapter call.
7. Exhausted retries MUST lead to a definitive failure only when the outcome is known; otherwise the operation enters `needs_operator` or remains in a documented reconciliation state.
8. Every attempt and retry decision MUST be recorded.

## 9. Cancellation

Cancellation is a request to stop future work where safe; it is not a rollback.

The controller MUST determine whether cancellation is safe for the current operation and step:
- Before any side effect, the operation can generally be cancelled.
- During a cancellable wait, the controller may stop polling or waiting while recording any provider-side action still in progress.
- During a mutating external call, cancellation may be unavailable or ambiguous.
- After an irreversible action or defined point of no return, cancellation may be rejected.
- A partially completed workflow may require a separate, explicitly authorized compensating operation.

When cancellation is requested, status becomes `cancellation_requested` while the controller evaluates and applies the operation-specific rules. It becomes `cancelled` only after the controller confirms that no further work will be performed by that operation and records any remaining external state. If the external outcome is uncertain, the operation enters `reconciling` or `needs_operator`, not `cancelled`. In particular, if a mutating call may have been dispatched but its response is missing, the controller MUST keep the operation non-terminal in `reconciling` (or `needs_operator` when reconciliation is unavailable), preserve the step intent and any resource lock, and reconcile before retrying or ending cancellation. If durable state proves the step was never dispatched and no external effect is possible, the controller MAY cancel it without a provider call.

The protocol MUST define cancellation behavior and any point of no return for each operation type. Implementations MUST NOT promise that all operations are cancellable.

## 10. Recovery and reconciliation

On startup, the controller MUST inspect durable non-terminal operations and determine whether each can safely resume, must wait, requires reconciliation, or needs operator intervention.

For each interrupted step:
1. Read its durable intent and last recorded attempt.
2. Determine whether dispatch occurred and whether an external operation reference exists.
3. Query the adapter/provider or inspect the affected resource using read-only methods where possible.
4. Compare observed state with the step's preconditions and postconditions.
5. Record the reconciled result before taking further action.
6. Resume only when the next action is safe and authorized; otherwise suspend for operator review.

If dispatch status or external result is unknown, the step is `outcome_unknown` and the operation is `reconciling`. The controller MUST NOT infer success or failure from a missing response.

If the adapter cannot determine the external outcome, the operation MUST move to `needs_operator`. It MUST retain resource locks or mark the affected resource as requiring recovery so that a second operation cannot race the unresolved work.

Recovery MUST NOT:
- Repeat a possibly completed destructive action blindly.
- Treat an unverified backup as safe for destruction.
- Route players to a backend that has not passed readiness checks.
- Apply a compensating action without its own authorization and safety validation.
- Discard partial results or external resource references.

## 11. Progress and events

Operation state is authoritative. Progress notifications and event delivery are supplementary; losing a notification MUST NOT lose the operation or its result.

The canonical event object is defined by [operation-event.schema.json](schemas/operations/operation-event.schema.json). It contains:
- `event_id`: unique event identifier within the controller installation.
- `operation_id`: owning durable operation.
- `sequence`: monotonically increasing integer within that operation, starting at 1 and never reused.
- `event_type`: stable namespaced event type, such as `operation.status.changed`, `operation.step.started`, `operation.step.completed`, `operation.progress.updated`, or `operation.reconciliation.required`.
- `timestamp`: RFC 3339 event time.
- Optional `step_id`, `status`, and documented safe `data`.

The controller MUST persist an event before making it externally observable. Sequence numbers are ordered per operation, not globally. Consumers MUST tolerate duplicate delivery, detect gaps, and retrieve the operation resource as the authoritative state. An event stream MAY replay events after a supplied sequence cursor. Event-stream delivery is at-least-once when events are emitted; duplicate delivery is permitted and consumers MUST deduplicate by event identity or sequence within an operation. Retention and gap recovery behavior MUST be documented by each implementation. The controller MUST NOT depend on event retention for operation recovery because the durable operation snapshot is authoritative.

Progress SHOULD identify a stable machine-readable phase/step identifier, human-readable status text, optional completed/total units or percentage when meaningful, and last-update time. Percentage is optional and MUST NOT imply a completion estimate when work cannot be measured.

The management API MAY expose Server-Sent Events for operation events. This is optional; clients MUST remain functional by polling the operation resource if streaming is unavailable or disconnected. A streaming event's data is a JSON serialization of the canonical event object.

Progress, events, results, and errors MUST NOT expose secret values or unnecessary player data.

## 12. Results, failures, and partial completion

The terminal operation record carries its final status and `completed_at` timestamp. Its optional `result` property uses the canonical [operation-result.schema.json](schemas/operations/operation-result.schema.json), which contains:
- A confirmed outcome summary.
- Affected external resource references.
- Completed and failed step summaries.
- Warnings and unresolved conditions.
- Related audit event IDs.
- A `partial` flag indicating whether only part of the requested outcome was achieved.

Each result identifies its `operation_type`, which MUST match the enclosing operation. Successful definition, provisioning, start, stop, backup, restore, and destruction results MUST include `operation_data` validated against the corresponding registered result schema: [define](schemas/operations/server-define-result.schema.json), [provision](schemas/operations/server-provision-result.schema.json), [start](schemas/operations/server-start-result.schema.json), [stop](schemas/operations/server-stop-result.schema.json), [backup](schemas/operations/server-backup-result.schema.json), [restore](schemas/operations/server-restore-result.schema.json), or [destroy](schemas/operations/server-destroy-result.schema.json). These schemas add operation-specific facts while preserving the canonical result envelope.

A failed operation MUST include a structured operation-level `error`. When known side effects or partial completion exist, the operation SHOULD also retain a result with `partial: true`. A successful operation MUST include a result and completion timestamp. A cancelled operation MUST include a completion timestamp and preserve any known external state.

The canonical [operation.schema.json](schemas/operations/operation.schema.json) requires the trusted `actor_id` and immutable `request_snapshot`. The snapshot MUST contain validated request parameters and secret references only; resolved secret values MUST NOT be stored. A request fingerprint MAY be stored to support deterministic duplicate-request checks. When an implementation runs from an untagged draft, it SHOULD preserve the exact source/specification revision in protected operation metadata; a released implementation SHOULD populate `schema_package_revision` with the immutable package/tag identity.

For `failed` or `needs_operator` operations, the record MUST preserve known side effects and partial completion. For example, if provisioning created a VM but game preparation failed, the operation record must retain the VM reference and its observed state.

The controller MUST NOT assume that failure automatically reverses earlier steps. Compensation or cleanup is a separate workflow unless the operation contract explicitly defines a safe, authorized compensation step. A compensation step is itself recorded and subject to authorization, idempotency, and recovery rules.

A failed operation may therefore leave resources allocated or data preserved for safety. This is preferable to untracked or unsafe cleanup.

## 13. Concurrency and resource coordination

Mutating operations that affect the same logical server or shared data MUST be serialized according to a documented lock scope. The initial design uses an exclusive per-server lock for operations such as start, stop, backup, restore, upgrade, and destroy.

- A lock records its owning operation ID and scope and is part of durable recovery state.
- A second conflicting operation MUST be rejected with `core.operation.conflict` unless the implementation declares an explicit durable queue policy; it MUST NOT run concurrently by accident.
- A lock MUST NOT be released merely because a process or connection disappeared, a worker crashed, or a lease/time limit elapsed.
- On controller restart, the operation and its lock MUST be restored before dispatching work that could conflict. If the owner or external outcome cannot be safely recovered, the operation MUST enter or remain in reconciliation or `needs_operator`, and the lock MUST remain held.
- A lock is released only after the owning operation is terminal and its relevant external outcomes are reconciled; the terminal transition and lock release MUST be committed durably as one recovery-safe change.
- Multi-resource operations MUST acquire locks in a stable, documented order, such as lexical order of `(resource_kind, environment_id, resource_id)`, to avoid deadlock.
- A suspended operation with unresolved external effects keeps the relevant resource blocked until safe release is confirmed.

The operation protocol does not select a database or distributed lock implementation; it does require equivalent durable ownership, recovery, and release semantics.

## 14. Authorization, approvals, and audit

The controller establishes the trusted actor identity and authorization context. A client or adapter MUST NOT be able to elevate its own privileges by supplying actor or permission fields.

Authorization and approval checks occur before acceptance and again before sensitive steps when relevant. An approval MUST be bound to the exact action, target, environment, and material parameters it authorizes. A changed target or materially changed plan requires renewed approval.

The audit trail SHOULD record:
- Request accepted or rejected.
- Actor, operation, target, environment, and authorization decision.
- Approval references and the scope approved.
- State transitions and step attempts.
- Retry, cancellation, reconciliation, and recovery decisions.
- Operator intervention and final outcome.

Audit data MUST be protected from unauthorized modification and MUST redact secrets and unnecessary player data. Operation history is not a substitute for the security audit trail; the two may be linked.

## 15. Legal operation status transitions

The following transitions are permitted for the operation status values in this draft. Any unlisted transition MUST be rejected.

| From | To | Rule |
|---|---|---|
| `pending` | `running` | Worker begins the accepted operation. |
| `pending` | `cancellation_requested` | Cancellation accepted for evaluation before work begins. |
| `pending` | `cancelled` | Safe cancellation confirmed before dispatch. |
| `pending` | `failed` | Pre-execution failure is definitive and operation cannot run. |
| `running` | `waiting` | Waiting on a provider, timer, or prerequisite. |
| `running` | `cancellation_requested` | Cancellation request received. |
| `running` | `reconciling` | Dispatched effect or interrupted step has uncertain outcome. |
| `running` | `needs_operator` | Automation cannot safely proceed. |
| `running` | `succeeded` | Requested outcome and postconditions confirmed. |
| `running` | `failed` | Definitive failure; no unresolved external effect remains. |
| `waiting` | `running` | Prerequisite satisfied and work resumes. |
| `waiting` | `cancellation_requested` | Cancellation requested while waiting. |
| `waiting` | `reconciling` | External outcome became uncertain. |
| `waiting` | `needs_operator` | Automated resolution is unsafe or exhausted. |
| `waiting` | `failed` | Definitive failure. |
| `cancellation_requested` | `cancelled` | No further work will be performed and remaining external state is known. |
| `cancellation_requested` | `reconciling` | Cancellation intersects an uncertain in-flight effect. |
| `cancellation_requested` | `needs_operator` | Safe cancellation cannot be determined automatically. |
| `cancellation_requested` | `succeeded` | The requested outcome completed before cancellation could take effect and postconditions are confirmed. |
| `cancellation_requested` | `failed` | Work definitively failed and no unknown effect remains. |
| `reconciling` | `running` | Prior effect is known and safe work can resume. |
| `reconciling` | `waiting` | External work is confirmed pending. |
| `reconciling` | `needs_operator` | Outcome cannot be safely determined. |
| `reconciling` | `succeeded` | Reconciliation confirms the requested outcome and postconditions. |
| `reconciling` | `failed` | Reconciliation confirms definitive failure. |
| `reconciling` | `cancelled` | Reconciliation confirms safe cancellation and no further work. |
| `needs_operator` | `reconciling` | Authorized operator directs further state reconciliation. |
| `needs_operator` | `running` | Authorized operator resolves the blocker and explicitly resumes safe work. |
| `needs_operator` | `failed` | Operator records a definitive failure resolution. |
| `needs_operator` | `cancelled` | Operator confirms safe cancellation and no further work. |

Only `succeeded`, `failed`, and `cancelled` are terminal. `needs_operator` is suspended, not terminal. Terminal states never transition again; additional work requires a new operation linked to the prior operation.

An operation MUST NOT become `failed` while a mutating external effect remains unresolved. It MUST remain `reconciling` or move to `needs_operator`.

## 16. Step status transitions

Step statuses are `pending`, `running`, `waiting`, `outcome_unknown`, `succeeded`, `failed`, `skipped`, and `cancelled`.

Permitted transitions:
- `pending` -> `running`, `skipped`, or `cancelled`.
- `running` -> `waiting`, `outcome_unknown`, `succeeded`, `failed`, or `cancelled`.
- `waiting` -> `running`, `outcome_unknown`, `succeeded`, `failed`, or `cancelled`.
- `outcome_unknown` -> `running`, `waiting`, `succeeded`, or `failed`, but only after reconciliation establishes the safe next state.
- Terminal step statuses never transition again.

A step marked `succeeded` MUST have its postcondition evidence persisted. An `outcome_unknown` step MUST NOT be blindly re-dispatched.



## 17. Illustrative example: uncertain destroy result

A destroy operation is accepted and durably recorded. The controller records the intent to request provider destruction and dispatches the adapter call. The connection then drops before a response arrives.

The controller:
1. Marks the step `outcome_unknown` and operation `reconciling`.
2. Does not issue another destroy request.
3. Uses the hosting adapter to inspect the exact instance in the recorded environment.
4. If absence is confirmed and the provider semantics establish successful destruction, records the step as succeeded and continues postcondition checks.
5. If the instance still exists and the provider confirms no destroy request remains active, retries only if the adapter contract says that retry is safe.
6. If the result remains ambiguous, moves the operation to `needs_operator` and keeps the logical server blocked from conflicting operations.

This is the required pattern for any mutating external call whose result may be unknown, not only VM destruction.

## 18. Resolved policy decisions

- **Standard operation catalog:** The initial standard catalog contains `server.define`, `server.provision`, `server.start`, `server.stop`, `server.backup`, `server.restore`, and `server.destroy`. Their request/result schemas and postconditions are the standard catalog for the initial 1.0 profile. New standard core operations require documented schemas and conformance cases. An `upgrade` operation is not part of this initial catalog.
- **Record representation and retention:** The schemas define the logical operation and step records; physical storage and database choice are implementation-specific. Implementations MUST retain accepted non-terminal operations and recovery-critical data, document terminal-record retention, and never discard a record while its external outcome or lock ownership remains unresolved.
- **State and events:** The durable operation snapshot is authoritative for reads and recovery. The controller MAY keep a separate event log and MAY expose an event stream. Emitted events are persisted before visibility and may be delivered at least once; event retention and gap recovery are implementation-documented.
- **Idempotency:** Management operation acceptance uses the required case-sensitive 1–128 visible-ASCII `Idempotency-Key` header. The actor/key association survives the full non-terminal lifetime, the complete operation record is retained for at least 90 days after terminal status, and a privacy-minimized tombstone remains for the active controller installation lifetime; HMAC-based deployments preserve the versioned keyring while any matching tombstone remains. Reuse after full-record expiry returns `core.operation.idempotency_key_expired` without replay, new operation, or dispatch.
- **Timeouts and retries:** There are no universal numeric timeout, retry, or backoff defaults. Implementations and adapter profiles document bounded policies. If no safe retry policy is declared, a mutating adapter call receives zero automatic retries; an unknown outcome is reconciled before any retry.
- **Events and progress:** Progress is advisory and events supplement the authoritative operation resource. Consumers handle duplicate delivery and sequence gaps by reading the operation resource. Retention and replay behavior are documented by each implementation.
- **Locks and recovery:** Durable operation-owned locks survive controller restart and are restored before conflicting dispatch. A conflicting request is rejected with `core.operation.conflict` unless an explicit durable queue policy is declared. Worker failure or elapsed time alone does not release a lock. Unresolved external effects keep the lock until reconciled and the operation is terminal. Multi-resource locks use a stable documented order. Storage mechanism is out of scope.
- **Acceptance:** The initial management HTTP binding accepts mutating requests asynchronously and returns `202 Accepted` only after durable commit. An adapter RPC call is request-response at the transport layer; an adapter result of `accepted` means provider work is pending and does not mean the durable management operation is complete.

These choices close the operation decisions for the draft. Transport-specific bindings MUST preserve them.

## Related specifications

- [Protocol principles and communication surfaces](README.md)
- [Common protocol types](common-types.md)
- [Core design and lifecycle model](../docs/CORE_DESIGN.md)
- [Adapter contracts](../docs/ADAPTER_CONTRACTS.md)
