# Minecraft health recovery and backup-before-replacement

This optional Paper-specific reference implementation is a **game restart supervisor**, not a VM replacement daemon. Its only mutating operation is a bounded restart of `minecraft-vanilla.service` on the **existing** VM. It contains no provider create, reboot, destroy, storage delete, or world restore operation. It is disabled until a trusted operator explicitly installs and enables the controller-side timer.

## Ownership and detection

- **Systemd on the VM** handles ordinary Java process crashes with `Restart=on-failure` and a finite start-rate limit. It cannot reliably detect a live but frozen JVM.
- **Read-only VM probe** (`scripts/bootstrap/minecraft_probe.py`): runs with a narrow sudo allowance, inspects `minecraft-vanilla.service` and a bounded number of journal lines from the **current** systemd invocation. Returns only `status`, a fixed reason code, and invocation ID, never raw log content.
- **Controller recovery supervisor** (`scripts/recovery/controller_recover.py`): requires two unhealthy results for the same invocation within five minutes. It persists restart intent before the remote call, enforces a 20-minute cooldown, a maximum of two attempts per hour and three per rolling day, and verifies a **new** invocation reaches the Paper `Done (... )!` log marker after restart. Failed SSH, unreadable journal, unknown/starting state and an intentionally stopped game never authorize a restart.
- **Velocity bridge**: routes away from an unreachable backend and can report connection failures, but proxy reachability does **not** prove host failure and must never trigger VM creation.
- **Optional Paper plugin (future work)**: may expose tick heartbeat, MSPT/TPS, world-save and JVM metrics to a game adapter over authenticated, restricted transport. It must not be required for core crash recovery, must never possess Vultr credentials, and must never restart, replace or delete infrastructure itself. A hung main tick thread may prevent the plugin from answering. The controller must cross-check service state, logs, and game response.

## Opt-in installation

These steps affect only the controller-side supervisor. The VM must already contain the **new bootstrap's** `/usr/local/libexec/gsc/minecraft_probe.py` and the exact sudoers entry for the `minecraft-admin` account. Do **not** rerun the entire bootstrap on an existing production VM to install this one probe.

1. On the controller, use a pinned OpenSSH `known_hosts` key and a dedicated SSH alias such as `backend` mapped to `minecraft-admin@<existing VM host>`. Confirm that this user can run `sudo -n /usr/local/libexec/gsc/minecraft_probe.py`, and that the remote script prints a single JSON object without log lines.
2. From a reviewed checkout, install the controller-side files as root:

   ```sh
   sudo install -D -m 0755 scripts/recovery/controller_recover.py /usr/local/libexec/gsc/controller_recover.py
   sudo install -m 0644 scripts/recovery/gsc-paper-recovery@.service /etc/systemd/system/
   sudo install -m 0644 scripts/recovery/gsc-paper-recovery@.timer /etc/systemd/system/
   sudo systemctl daemon-reload
   ```

3. Check the service's `User=minecraft-controller` matches the controller's actual unprivileged SSH account. Check that the account's `~/.ssh` configuration and pinned host-key trust work **without** passwords or prompts.
4. Test one dry run before enabling the recurring service. The `--execute` option on a manual run requires `--confirm-host` to match exactly.

   ```sh
   sudo -u minecraft-controller python3 /usr/local/libexec/gsc/controller_recover.py --host backend
   sudo systemctl enable --now gsc-paper-recovery@backend.timer
   systemctl list-timers 'gsc-paper-recovery*'
   ```

The template runs approximately once per minute **only after explicit enablement**. Its state lives in a protected `/var/lib/gsc-paper-recovery` directory on the controller. The lock prevents duplicate concurrent recovery actions. Timers must not be enabled on a machine that can no longer prove the SSH host identity. An SSH key or credential leak is an incident, not a reason to relax `StrictHostKeyChecking`.

## Replacement as a last resort

A failed Minecraft check, stale or missing plugin heartbeat, unreachable game port, or failed SSH connection **is not enough evidence** to create or replace a VM. Exhaust bounded game restarts and diagnose the existing host: Java binaries, available RAM, storage space and filesystem errors, CPU and kernel health, networking and provider VM state. Separate `game process unhealthy` from `host cannot safely run or repair the game`.

If a host is genuinely unrecoverable, use this sequence:

1. Record the exact managed instance and why repair is not possible. Explicitly approve a new **billable** VM and satisfy managed-capacity limits; an unhealthy game never consumes approval for creation.
2. If the old disk is accessible, drain players, save/flush the Paper world, stop the service and capture a **new, timestamped and uniquely identified** recovery point under Wasabi `/backups`, separate from the mutable `/server` live sync. Do not allow overlapping writes into the snapshot.
3. Verify the manifest, file listing, object sizes, content checksums, data generation and exact source instance. Demonstrate that the backup exists **outside** the old VM and includes writes through the final quiesced save boundary. Test restoring in isolation when practical. Keep an older known-good backup as an additional fallback rather than overwriting it.
4. Only from a specifically selected verified backup or approved migration, provision and restore a replacement. Validate Paper readiness and proxy routing **before** switching players. Temporarily retaining both VM resources may need an explicit capacity reservation/exception; do not silently bypass the quota.
5. Destruction of the original VM is a **separate, operator-approved operation**, performed only after rechecking its exact identity, current stop state, off-instance consistent backup including final writes, and current authorizations. Never automatically destroy, and never retry provider deletion after an uncertain result without reconciliation.

**When a backup cannot be completed:** keep the existing VM and disk allocated for forensic recovery even if Minecraft will not start. Do not pretend that the last successful upload includes recent builds; an older backup may imply world rollback. A new VM may be considered under an approved incident plan, but it never grants permission to delete the source. Escalate storage rescue and any unavoidable data-loss decision to the operator.

## Durability and privacy

Wasabi states its object storage is **designed for 11-nines (99.999999999%) annual object durability**: https://docs.wasabi.com/docs/durability-security-reliability. This is **not** the probability that a live Minecraft world will be consistently captured and restored. Backup success requires independent checks and periodic restore drills. Use least-privilege storage credentials, keep backup versions or immutable recovery points, and never allow a `/server` sync to delete dated `/backups` snapshots.

Wasabi Object Lock is an option for a dedicated immutable backup bucket; it requires versioning and must be enabled at bucket creation: https://docs.wasabi.com/docs/object-locking. Retention/versioning can add cost. Keep VictoriaLogs and Homelab integrations **optional**. Recovery probes do not depend on either service and do not export raw player logs or personal data.

## Implementation status

The standalone supervisor and bootstrap probe are reference components in this branch. The controller's general workflow engine currently registers only `server.define` in `internal/controllerruntime`; full automated host diagnosis, Paper plugin telemetry, replacement orchestration, restore-drill verification and management API operations remain **unimplemented**. The `server.destroy` workflow now rejects backups without explicit independently verified off-instance, consistency, and final-write-boundary evidence. No deploy or timer activation occurs merely by merging these files.

The recovery timer must be tested on a disposable, isolated instance first. Run `python3 -m unittest discover -s scripts/recovery -p 'test_*.py'` and `go test ./scripts/bootstrap ./internal/workflow` as well as the repository's full validation suite.
