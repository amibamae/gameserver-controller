# Adapter Transport Binding

**Status:** Draft, version 0.21.0-draft.

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
- Large backup data MUST NOT be embedded in RPC messages. `local-unix-stream-v1` defines an optional, separately negotiated byte-stream channel for storage transfer references. A process adapter MUST NOT advertise `storage.object.put` or `storage.object.get` unless the profile is negotiated and declared in its descriptor.
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

Remote adapter authentication and transport are out of scope for the initial 1.0 profile, which uses in-process adapters and the optional local stdio binding. A future remote binding MAY use HTTPS, mutual TLS, or another authenticated transport. It MUST preserve capability semantics, request context, deadlines, structured errors, idempotency, reconciliation, and authorization boundaries. A new remote binding requires a separately versioned specification and conformance tests.


## 9. Bounded local stream profile

The optional `local-unix-stream-v1` profile pairs with `jsonrpc-stdio-v1`. JSON-RPC remains the control channel; object bytes use one local Unix-domain stream socket. The controller MAY omit the offer when the platform or deployment does not support this profile.

Before starting the adapter, the controller creates a private runtime directory owned by the controller user with permissions set to 0700, and listens on one absolute socket path no longer than 100 ASCII bytes. It SHOULD set socket-file permissions to 0600 where supported; the private directory is the primary access control because pathname-socket permission behavior varies by operating system. The directory MUST be unique to the adapter session. The controller MUST reject a pre-existing path and remove the socket and directory after the session ends. The adapter learns the path only through the optional `stream_transport` member of `adapter.initialize`. The adapter selects the profile by returning `selected_stream_transport`; omission means the profile was not selected. The selected profile MUST have been offered.

A descriptor that advertises `storage.object.put` or `storage.object.get` MUST list `local-unix-stream-v1` in `stream_transport_profiles`, and the controller MUST have received that selection during initialization. If either condition is missing, the controller MUST reject those capabilities before invocation.

For each transfer, the adapter opens one connection to the negotiated socket path and writes the 64-character lowercase hexadecimal `transfer_id` followed by LF. This is the complete handshake; no additional metadata is sent. The transfer ID is an unguessable 256-bit bearer capability. The controller MUST bind each ID to the exact adapter session, operation, step, environment, direction, byte limit, and expiry, and atomically consume it on the first connection attempt. Unknown, expired, reused, or out-of-scope IDs MUST be rejected before any payload bytes are accepted.

After a valid handshake, the connection carries raw bytes until EOF. For `controller_to_adapter`, the adapter reads and the controller writes. For `adapter_to_controller`, the adapter writes and the controller reads. Each handle permits exactly one connection and one direction. The receiver MUST stop the stream when the handle limit or the operation's expected byte count would be exceeded. A transfer is complete only at EOF with the exact expected byte count; incomplete streams MUST remain partial or unknown and MUST NOT be treated as verified objects. Object checksum verification remains a separate operation.

A transfer ID is a secret capability and MUST NOT appear in logs or diagnostics. The local socket profile is a transport mechanism, not a sandbox boundary. Deployments MUST use their normal process isolation policy for adapters.
