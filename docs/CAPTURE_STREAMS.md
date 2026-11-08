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
