# Minecraft recovery: stage first, repair before replacing a VM

This branch adds **staging-safe recovery policy, a read-only Paper log probe, and a stronger backup-before-destruction gate**. It does **not** enable or deploy automatic restarts on production, wire a restart operation into `controller serve`, or authorize any Vultr instance creation or destruction.

## What runs today and what is staged

- **systemd** on the Minecraft VM already uses `Restart=on-failure` and `RestartSec=10`. The revised bootstrap adds `StartLimitIntervalSec=900` and `StartLimitBurst=3` to bound crash loops. A stopped game stays stopped.
- **Read-only Paper health probe**: `scripts/bootstrap/minecraft_probe.py` is bundled in *newly rendered* bootstrap scripts. It inspects `minecraft-vanilla.service` and bounded journald lines from the exact systemd invocation, returning only fixed status and reason codes and an invocation ID, never player messages or credentials. It does not restart anything.
- **Go recovery policy**: `internal/recovery` requires two fresh, matching unhealthy observations from the same existing VM and runtime. It produces a sequence of existing protocol capabilities, **not executable commands**. It cannot create/destroy a VM. It rejects stale/unknown evidence, different release/host/runtime identities, and intentional stops.
- **Durability gate**: `internal/workflow/server_destroy.go` requires the verified-backup resolver to independently attest off-VM storage, consistent capture, and inclusion of the last quiesced writes. Until a concrete resolver can prove those facts, destruction fails closed. These attestations cannot be inferred from the existence of a bucket or from a successful upload.

The original prototype contained a separate Python controller daemon issuing `systemctl restart` directly. **It was removed** because this bypassed the protocol's save-and-restart requirements and risked re-enabling proxy routing before game readiness.

## Required in-place recovery sequence

For a running Paper process with two independently confirmed unhealthy observations from the **same** exact systemd incarnation:

1. Persist and authorize an operation against the existing server, environment, instance, game release and unit. Reconcile any conflicting operation and enforce a bounded retry budget/cooldown.
2. Set the existing registered backend to **draining** via Velocity before restarting. No new player routing should resume merely because the socket answers.
3. Invoke `game.save.request` and require an exact, confirmed save from the same durable operation, selected game adapter, release and runtime.
4. Invoke the existing `execution.unit.restart` contract, which requires the confirmed save, bounded graceful stop, exact unit inspection and a **different** running `runtime_id`. Never silently force-kill or replay a restart after an unknown outcome.
5. Inspect the newly running unit. Run `game.readiness.check` for the selected release/check-set. Require a new Paper `Done (...)!` line belonging to the **new systemd invocation**, with fresh timestamps, and every required game check passing.
6. Verify the current instance and controller readiness assertion still match. Only then re-register the same backend through the Velocity bridge and permit returning players.

For an already exited/failed service, use the stopped-runtime **start** path instead of `execution.unit.restart`. Reconcile the logical lifecycle state and failed systemd invocation first. Do not send `systemctl restart` to a still-running process simply because its logs contain an exception.

The current Go policy models these actions as `integration.proxy.backend.drain`, `game.save.request`, `execution.unit.restart` (or `execution.unit.start` for an already-dead process), `execution.unit.inspect`, `game.readiness.check`, and `integration.proxy.backend.register`. **The orchestrator and runtime bindings are not implemented yet.** A stage test verifies the ordering and that an old, absent or stale `Done` cannot authorize re-registration. The proxy bridge already has a draining registration state that can keep the backend unroutable even if its TCP probe recovers.

### Why not add a Paper plugin now?

Paper's logs, systemd incarnation identity, and game readiness checks are enough to implement and test crash/failed-start recovery. A Paper plugin may later enrich TPS/MSPT, watchdog and world-save metrics, but a JVM stall can also stall the plugin. Keep any future plugin optional and advisory. It must never hold Vultr or Wasabi credentials.

## VM replacement and Wasabi durability

**A game failure is not a host failure.** Never automatically create a new Vultr VM because the game port or health probe failed. Before even considering a new billable VM, independently diagnose Java, memory, storage space/errors, filesystem integrity, SSH/OS availability, and provider instance health. A confirmed inability to repair Minecraft on the existing VM requires a separate approved replacement operation and capacity allocation.

**VM deletion is a separate, explicitly authorized operation and remains disabled by default.** Before destruction:

1. Drain players and attempt a clean `save-all flush`/graceful shutdown when possible; record the final quiesced write boundary.
2. Create a **new timestamped, uniquely identified, immutable recovery point** outside the original VM, e.g. in Wasabi `/backups`, rather than overwriting the mutable `/server` sync. Do not delete older recovery points.
3. Verify the exact source instance, environment, world data generation, manifest, object existence, size, checksum, consistency and final-write boundary. Test a restore in isolation when practical; preserve at least one known-good fallback.
4. Recheck the backup evidence, current VM identity and independent destruction approval immediately before calling the provider. If the old disk cannot be read or the most recent writes cannot be accounted for, **do not destroy it**. Preserve it for disk/data rescue; an approved replacement may be prepared separately without deleting the source.

Wasabi advertises storage designed for 99.999999999% annual **object durability**. This is not an end-to-end guarantee that a Minecraft save is correct, complete, recoverable, or immediately accessible. Object Lock/versioning on a properly configured backup bucket can help protect recovery points from accidental deletion. Storage retention and versioning incur costs.

## Optional observability and privacy

VictoriaLogs and Homelable are **optional**, never prerequisites for recovery. Probe output is redacted and bounded; full raw player logs remain local. The controller must not transmit credentials, player chat, or identifiable telemetry through these checks.

## Staging validation

No production services should be changed to evaluate this branch. Use an isolated staging VM and a test world to exercise the following acceptance scenarios:

- A confirmed failure on one exact VM plans a safe in-place restart; a new `Done` and new runtime identity are required before any Velocity re-registration.
- Missing, spoofed, stale, conflicting or unknown logs never authorize a restart or routing.
- An unverified, old, incomplete or non-off-instance Wasabi backup blocks the destroy workflow, even when the VM has failed.
- Restart attempts never cause hosting instance creation or deletion.

CI runs `go test ./internal/recovery ./internal/workflow ./scripts/bootstrap` alongside the full Go suite, and `python3 -m unittest discover -s scripts/observability -p 'test_*.py'` for the local probe. The Go runtime currently registers only `server.define`; a subsequent controlled integration is required to turn the staged sequence into an automatic controller workflow.
