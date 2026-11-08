# Backup resume context proposal

Status: Draft decision proposal. This document proposes protocol metadata and conformance work; it does not implement a controller workflow.

## Problem

The lifecycle permits backup from ready, degraded, and stopped states. The operation catalog defines resume behavior for ready and stopped servers, but leaves degraded behavior unspecified. The operation resource has no standard field that preserves the state before backup changes it.

The final result cannot establish the starting state. After a restart, observing stopped also cannot distinguish a server that was already stopped from one that backup stopped temporarily.

## Recommended contract

Add core-owned `backup_initial_lifecycle_state` metadata to a server.backup operation, with values ready, degraded, or stopped. Capture it while holding the operation's server lock, immediately before the first backup-related lifecycle transition or adapter dispatch. Persist it before either happens and preserve it unchanged through retries and restart.

The field belongs to the read-only operation resource. Clients and adapters cannot supply or replace it. It describes the exact server, environment, and instance in the durable operation target; a changed target requires reconciliation rather than reinterpreting the context.

Resolve the requested final state from the original parameters and this metadata:

| Initial state | Resume omitted | Resume false | Resume true |
|---|---|---|---|
| ready | ready | stopped | ready |
| stopped | stopped | stopped | stopped |
| degraded | stopped | stopped | ready, subject to current policy and fresh readiness |

For degraded servers, explicit true expresses an intentional request to try returning to service. It does not treat the prior degraded runtime as ready. Every ready result still requires a fresh readiness assertion bound to the exact target.

The current unconditional `default: true` annotation on `resume_after_backup` must be replaced with the conditional rules above. The core must preserve whether the caller omitted the field; applying a generic JSON Schema default before resolving this policy would erase that distinction.

An operation missing trustworthy starting-state context after dispatch remains reconciling or needs_operator. It cannot infer the initial state from the current server state, automatically start the runtime, or claim success without proving its final-state policy. Existing locks and partial-effect evidence remain durable.

## Implementation and conformance scope

A separate schema slice should add the operation field and require it for successful backup results. Normative text must require persistence before the first transition or dispatch, even though a response schema alone cannot prove write ordering or immutability.

Language-neutral fixtures should cover all nine table cells, unknown/missing context, contradictory final states, and readiness binding/freshness. A Go fake implementation exercise should additionally record the context commit and attempted effects, restart between backup and resume, reject context mutation, and prove that missing context dispatches no startup call.

Do not change controller or infrastructure code as part of the protocol design slice.

## Decision for review

Confirm whether an explicitly requested resume from degraded should be allowed under policy and new readiness checks, as recommended here, or whether all degraded backups must end stopped. Either choice must be explicit in the catalog, parameter schema description, and conformance matrix before claiming complete backup resume coverage.

Related contracts: [operation catalog](../protocol/operation-catalog.md), [lifecycle](../protocol/lifecycle.md), [durable operations](../protocol/operations.md), and [backup parameters](../protocol/schemas/operations/backup-parameters.schema.json).
