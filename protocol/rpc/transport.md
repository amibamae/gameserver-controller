# Adapter Transport Binding

**Status:** Draft, version 0.21.0-draft.

## 1. Keep meaning separate from transport

[methods.md](methods.md) defines adapter method behavior. This document defines the first optional process transport, `jsonrpc-stdio-v1`.

An adapter MAY instead run in-process if it implements the same contracts and passes the same conformance tests. The protocol does not require a network transport or Kubernetes.

## 2. Process profile

The process binding uses:

- UTF-8 JSON under RFC 8259.
- JSON-RPC 2.0 request/response semantics.
- One JSON-RPC message per line.
- Standard input for controller-to-adapter messages.
- Standard output for adapter-to-controller messages.
- Standard error for diagnostics only.

A newline ends the frame; it is not part of the JSON object. JSON serializers MUST escape newlines inside strings. Receivers MUST accept LF and MAY accept CRLF. Blank lines and unescaped literal line breaks are invalid.

## 3. Start and stop the adapter

The controller starts the adapter with an explicit executable and argument vector. Never build a shell command string from untrusted configuration. Minimize inherited environment variables and file descriptors. Deployment policy SHOULD restrict the working directory, executable, user, filesystem permissions, resource use, and network access.

After startup, the controller verifies the adapter identity and descriptor before enabling capability calls. A configured executable is not automatically trusted.

When stdin reaches EOF, the adapter stops accepting calls, finishes or safely abandons in-flight work under its documented deadlines, flushes responses, and exits. If the process crashes, the controller keeps pending operation state and reconciles it.

## 4. Frames and limits

- Each line carries exactly one JSON-RPC object.
- Batch arrays and notifications are not supported by this profile.
- Request IDs are strings. Numeric IDs are rejected to avoid cross-language precision errors.
- A line is limited to 4 MiB unless a future negotiated profile raises the limit.
- Do not put backup bytes in RPC messages. The optional `local-unix-stream-v1` profile carries storage data through a separate byte stream. A process adapter advertises `storage.object.put` or `storage.object.get` only when that profile is declared and negotiated.
- Reject invalid UTF-8, duplicate JSON object members, malformed JSON, and oversized frames.
- Handle partial reads and writes. Pipe read boundaries do not mark message boundaries.

## 5. Ordering and concurrency

Do not send requests until initialization succeeds. Correlate responses by string ID. Responses MAY arrive out of order only if the adapter descriptor declares concurrent handling; otherwise, serialize calls.

Mutating operations MUST return a response, or the controller treats the result as uncertain and reconciles it. Do not use notifications for mutations.

## 6. stdout and stderr

Reserve stdout for protocol frames. Any other stdout output violates the binding and makes the adapter unhealthy. Send only redacted diagnostics to stderr. Do not log secrets, raw credentials, unfiltered provider responses, or unnecessary player data.

## 7. Errors and shutdown

When framing permits, malformed messages receive a JSON-RPC parse or invalid-request error. Record an unrecoverable process exit as an adapter failure. Treat every in-flight mutation as uncertain until reconciled.

To request graceful shutdown, close stdin after scheduling no more calls. Killing the process does not prove provider-side work stopped.

## 8. Future transports

Remote adapter authentication and transport are outside the initial 1.0 profile, which uses in-process adapters and optional local stdio. A future binding may use HTTPS, mutual TLS, or another authenticated transport. It MUST preserve capability semantics, request context, deadlines, structured errors, idempotency, reconciliation, and authorization boundaries, and it needs its own versioned specification and conformance tests.

## 9. Bounded local stream profile

The optional `local-unix-stream-v1` profile pairs with `jsonrpc-stdio-v1`. JSON-RPC remains the control channel; object bytes use one local Unix-domain stream socket. The controller MAY omit this offer when the platform or deployment cannot support it.

Before starting the adapter, the controller creates a private runtime directory owned by the controller user with mode `0700`, then listens on one absolute socket path no longer than 100 ASCII bytes. It SHOULD set the socket file to `0600` where supported. Because pathname-socket permissions vary by OS, the private directory is the primary access control.

Use a unique directory for each adapter session. Reject an existing socket path, and remove the socket and directory when the session ends. The adapter learns the path only through the optional `stream_transport` member of `adapter.initialize`. It selects the profile by returning `selected_stream_transport`; omission means the profile was not selected. The controller MUST have offered any profile the adapter selects.

A descriptor that advertises `storage.object.put` or `storage.object.get` MUST list `local-unix-stream-v1` in `stream_transport_profiles`, and initialization MUST select it. Otherwise, reject those capabilities before invocation.

For each transfer, the adapter opens one connection and writes the 64-character lowercase-hex `transfer_id`, followed by LF. That is the whole handshake. The ID is an unguessable 256-bit bearer capability. The controller binds it to one adapter session, operation, step, environment, direction, byte limit, and expiry, then atomically consumes it on the first connection attempt. Reject unknown, expired, reused, or out-of-scope IDs before accepting payload bytes.

After a valid handshake, the connection carries raw bytes until EOF. For `controller_to_adapter`, the adapter reads and the controller writes. For `adapter_to_controller`, the adapter writes and the controller reads. Each handle permits one connection in one direction. Stop the stream if it would exceed the handle limit or the operation's expected byte count. A transfer completes only at EOF with the exact expected length. Leave interrupted transfers partial or unknown; never count them as verified objects. Check object checksums separately.

A transfer ID is a secret capability. Never put it in logs or diagnostics. This socket is a transport, not a sandbox; deployments still need their normal adapter process isolation.