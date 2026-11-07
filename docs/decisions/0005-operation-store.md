# ADR 0005: Initial operation persistence

**Status:** Accepted for the first Go controller process.

## Context

The protocol says the controller must durably record an accepted operation and its recovery state before it sends any external side effect. The first controller should run as one local process, without a separately operated database service.

## Decision

Use [bbolt](https://github.com/etcd-io/bbolt) as the first embedded operation store. Keep its database on a local filesystem in a user-private state directory. Use bbolt's synchronous transactions, one transaction for each accepted-operation or operation-update write, a versioned metadata bucket, and an exclusive file lock so only one controller process can own the store at a time.

The store exposes atomic acceptance, reads, compare-and-swap updates, recovery listing, and resource-lock inspection. When it accepts work, it checks an actor-scoped idempotency key, writes the operation, and reserves that server's logical resource lock in the same transaction.

For idempotency, the index keeps versioned HMAC verifiers and a keyed digest of the request binding. It never stores the client's key or request metadata as plain text. New operation acceptance fails closed if the database contains a key version that the configured keyring lacks.

All seven standard `server.*` operations share an exclusive lock keyed by environment and server ID. At startup, the store checks that the operation, idempotency, and both directions of the resource-lock indexes agree. If recovery state disagrees, startup fails closed. On restart, the store restores each lock with the database and releases it in the same transaction that records a safe terminal outcome.

Accepted request fields, schema revision, step order, and the recorded adapter capability identity are immutable. Operation and step status changes must follow the protocol transition tables. A terminal operation cannot change, and an unresolved external effect blocks terminal failure or cancellation.

The store accepts only validated request snapshots with secret references. Resolved secret values must be removed before a snapshot reaches this package. The database file is created with mode `0600`. Protect the host's disk and keep database copies out of public or shared storage.

## Consequences and limits

- The first release supports one controller process per database file. Multiple controllers or remote workers need a different transactional and locking design.
- The initial implementation keeps operation records indefinitely. Automated 90-day retention, tombstone conversion, backup export, and migration tooling are later work.
- Back up the keyring with the database. Opening a nonempty database from an earlier schema without an explicit migration currently fails closed.
- Back up a live database with a consistent bbolt snapshot or stop the controller first. Copying the file during writes is unsupported.
- bbolt is an implementation detail, not a protocol dependency. The operation-store interface keeps controller services independent of this backend.

## Reconsider this choice when

Review the decision before adding multiple controller replicas, network-attached storage, automated record retention, or a migration and backup workflow that needs a database snapshot interface.