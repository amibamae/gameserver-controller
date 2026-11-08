# Backup capture streams

`internal/capturestream` provides an in-process broker for the protocol's
`adapter_to_controller` capture streams. It is not yet connected to a backup
workflow or an adapter transport.

The controller issues a cryptographically random, 256-bit handle bound to one
operation, step, adapter, logical server, environment, hosting instance, data
generation, data set, and capture ID. Issuance requires a positive byte limit,
a future expiry within five minutes, and an expiry no later than the invocation
deadline. The broker bounds outstanding unclaimed handles and removes expired
entries during issuance. Restart drops all handles; recovery must issue fresh
ones after reconciling uncertain captures.

A trusted transport claims the handle with the authenticated invocation scope
and a private staging sink. Claiming consumes it atomically, including when a
capture later fails. A mismatched scope cannot consume another invocation's
handle. The writer checks cancellation and expiry before and after writes,
rejects writes that would exceed the byte limit, and computes SHA-256 over the
bytes the sink accepted. Partial writes, sink errors, expiry, cancellation, and
abort prevent a completion receipt.

The transport calls `Complete` only after observing end of stream. Its receipt
contains the independently measured size and digest. The workflow must still
compare both with the adapter result, validate all preparation/result bindings,
and perform storage and game validation before marking a backup verified.
Staged bytes remain untrusted until those checks pass.

The caller owns sink cancellation, closure, and cleanup. The broker does not
interrupt a sink blocked inside `Write`; transport and filesystem adapters must
provide bounded I/O. Returning after expiry still invalidates the receipt.
The broker does not publish files or transfer data to storage itself.

Stream references contain bearer credentials. JSON encoding is only for the
transient invocation context. Ordinary formatted references are redacted,
and broker errors do not include the handle or underlying sink error text.
Callers must not log reference fields or persist their JSON representation.

The in-process adapter registry now carries `stream_transfers` in invocation
context and copies the reference slice before dispatch. A capability must
advertise a stream direction and transport profile. Capture requires exactly
one stream; expired, excessive-lifetime, or deadline-exceeding references are
rejected before dispatch. The broker still checks the authenticated scope when
the adapter claims the handle.

The registry rejects handle text in durable capability input and adapter
responses, including JSON-escaped strings. Streamed-call errors omit adapter
and schema diagnostic text while preserving whether side effects are possible.
A fake-adapter integration test exercises broker consumption through this
boundary. The workflow engine does not yet issue streams or assemble verified
backups; the transport caller must still compare independent capture evidence
with the adapter result.

Controller workflows should use `IssueTracked` with their private staging sink.
It returns a private `Ticket` alongside the transient reference. The adapter
cannot replace that sink when claiming the handle. After invocation, the
controller reads `Ticket.Evidence` to obtain the broker's measured receipt,
including the exact scope, independently of the adapter's returned JSON. An
unclaimed, open, failed, or aborted stream has no usable evidence.
`Ticket.Abort` revokes an unclaimed handle or invalidates an active capture;
the caller remains responsible for removing staged data. Tickets are
process-local and must not enter durable operation records.

`internal/captureinvoke.Runtime` wraps the adapter registry for injection into
the workflow engine. Capture dispatch requires the stored backup operation and
step to be running, the captured initial lifecycle state, an exact immutable
input and capability version, matching target, and the operation's server lock.
The runtime issues the transient reference and pins the private stage selected
by its injected staging factory. It compares all capture-result bindings,
completion, byte count, digest, and receipt scope before asking staging to seal
the capture. The staging implementation must make that evidence durable and
associate it with the exact operation and step before returning from `Seal`.

Mismatched, incomplete, or contradictory capture evidence returns an uncertain
invocation error so the engine reconciles instead of persisting successful
output. Capability-level unknown results are normalized to an unknown envelope
with their reconciliation reference. Unsuccessful staging is discarded after
the stream is aborted; a discard failure remains an explicit uncertain result.
This runtime can be composed with the engine, but there is no controller
composition or complete backup workflow using it yet. A durable filesystem
staging implementation and verified storage transfer remain required.
