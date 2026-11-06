# Adapter Transport Binding

**Status:** Draft, version 0.4.0-draft.

## 1. Separation of semantics and transport

Adapter method semantics are defined in [methods.md](methods.md). This document defines the initial optional process transport profile, named `jsonrpc-stdio-v1`.

An adapter MAY instead be linked in-process if it implements the same logical contracts and passes the same conformance tests. No network transport or Kubernetes dependency is required.

## 2. Protocol stack

The initial process binding uses:
- UTF-8 JSON messages conforming to RFC 8259.
- JSON-RPC 2.0 request/response semantics.
- One JSON-RPC message per line (newline-delimited JSON framing).
- Standard input for controller-to-adapter messages.
- Standard output for adapter-to-controller messages.
- Standard error for diagnostics only.

The newline delimiter is framing, not JSON content. JSON string newlines MUST be escaped by the JSON serializer. A receiver MUST accept LF line endings and MAY accept CRLF. Blank lines are invalid. A message MUST NOT contain unescaped literal line breaks.

## 3. Process lifecycle

The controller starts the adapter using an explicit executable and argument vector; it MUST NOT construct a shell command string from untrusted configuration. Environment variables and inherited file descriptors MUST be minimized. The working directory, executable path, user identity, filesystem permissions, resource limits, and network access SHOULD be restricted by deployment policy.

The controller MUST verify adapter identity and descriptor after startup and before allowing capability calls. Adapter processes MUST NOT be trusted merely because they were configured.

On stdin EOF, the adapter MUST stop accepting new requests, finish or safely abandon in-flight calls according to documented deadlines, flush responses, and exit. The controller MUST persist pending operation state and reconcile it after an adapter crash.

## 4. Framing and limits

- Each line contains exactly one JSON-RPC object.
- JSON-RPC batch arrays and notifications are not supported by this binding.
- Request IDs MUST be strings. Numeric IDs are not accepted in this profile to avoid cross-language number precision ambiguity.
- A line MUST NOT exceed 4 MiB unless a future negotiated profile explicitly raises the limit.
- Large backup data MUST NOT be embedded in RPC messages. Storage transfer references are defined by the storage contract, but this profile does not define how they map to a byte stream. A separately versioned transfer binding is required before a process adapter may advertise `storage.object.put` or `storage.object.get`.
- The receiver MUST reject invalid UTF-8, duplicate JSON object member names, malformed JSON, and messages exceeding the limit.
- Implementations MUST handle partial reads and writes; operating-system pipe read boundaries are not message boundaries.

## 5. Ordering and concurrency

The controller MUST NOT send a request until initialization succeeds. Requests are correlated by string ID. Responses MAY arrive out of order only if the adapter descriptor declares concurrent request handling; otherwise the controller MUST serialize calls.

The adapter MUST NOT use notifications for mutating operations. It MUST return a response or the controller must treat the call as uncertain and reconcile.

## 6. stdout and stderr

stdout is reserved exclusively for protocol messages. Any non-protocol output on stdout is a protocol violation and the adapter MUST be treated as unhealthy. Diagnostic output belongs on stderr and MUST be redacted. Diagnostics MUST NOT contain secret values, raw credentials, unfiltered provider responses, or unnecessary player information.

## 7. Errors and shutdown

Malformed messages produce a JSON-RPC parse/invalid-request error when framing permits. An unrecoverable process failure is recorded as an adapter failure; all in-flight mutating calls are treated as uncertain until reconciled.

Graceful adapter shutdown is requested by closing stdin after no new calls are scheduled. A process kill is not proof that provider-side work stopped.

## 8. Future bindings

A future remote binding MAY use HTTPS, mutual TLS, or another authenticated transport. It MUST preserve capability semantics, request context, deadlines, structured errors, idempotency, reconciliation, and authorization boundaries. A new transport binding requires its own versioned specification and conformance tests.
