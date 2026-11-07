# Supervised backup pilot

The supervised pilot is a local CLI wrapper around the server's existing `minecraft-backup.service`. It does not install or replace the systemd unit or the backup script that unit already runs. The unit definition and script are not stored in this repository; the external server setup remains their source of truth.

## What the pilot sends

- `gsc-pilot status --host <ssh-alias>` makes read-only `systemctl show` calls for `minecraft-vanilla.service`, `minecraft-backup.service`, and `minecraft-backup.timer`.
- `gsc-pilot backup --host <ssh-alias>` is a dry run.
- The only mutating remote command is exactly `sudo -n systemctl start --wait minecraft-backup.service`.
- Before and after an executed backup, the pilot makes read-only `sudo -n systemctl show --property=ActiveState,SubState,Result,ExecMainStatus,InvocationID minecraft-backup.service` calls.
- The pilot does not send a script path, script contents, or custom arguments to the backup unit.
- `gsc-pilot history` reads the local audit log and makes no remote connection.

The SSH account needs non-interactive sudo access for the pilot's `systemctl show` and `systemctl start --wait` calls.

Execute with both flags and the exact same SSH alias:

```sh
gsc-pilot backup --host <ssh-alias> --execute --confirm-host <ssh-alias>
```

## How completion is reported

The pilot does not trust the `systemctl start --wait` exit status alone. After the command returns, it reads systemd again and reports service success only when the invocation ID is new, the unit is in a completed state (`inactive/dead` or `active/exited`), `Result=success`, and `ExecMainStatus=0`.

If the post-run invocation ID is missing or malformed, a nonempty prior ID is malformed or unchanged, the unit remains nonterminal (including still running), or post-run state cannot be read, the pilot records an unknown outcome and does not retry. Inspect the existing service and its logs before trying again.

The pilot does not stop Paper, restore a world, destroy a server, or install or modify the backup service.

## Operator handoff checklist

No live-server validation is claimed by the repository's automated checks or by this checklist. It describes an operator-supervised handoff; it does not report that a live backup has been run. The two-VM capacity policy below remains declarative and is not enforced by the pilot CLI.

Before an executed backup:

1. Confirm the SSH alias resolves to the intended server and review its host key.
2. Confirm the SSH account has non-interactive sudo access for the pilot's read-only `systemctl show` calls and `systemctl start --wait minecraft-backup.service`.
3. Run `gsc-pilot status --host <ssh-alias>` and review the game, backup, and timer unit states.
4. Run `gsc-pilot backup --host <ssh-alias>` without `--execute`. Review the target and service in the dry-run output; dry-run does not connect to the server.
5. Execute only after those checks, using the exact same alias for both flags:

   ```sh
   gsc-pilot backup --host <ssh-alias> --execute --confirm-host <ssh-alias>
   ```

6. Review the command result and run `gsc-pilot history` under the same local account. If the result is `outcome_unknown`, do not retry; inspect the service state and logs through the existing server process before deciding what to do.
7. After systemd reports success, separately confirm that the expected backup artifacts exist and that the existing backup process verifies their storage and integrity. The pilot's `service_succeeded` event confirms only the systemd invocation result.

## Declarative VM capacity policy

The [machine-readable capacity policy](../pilot/capacity-policy.json) allows at most two VM instances total: one in `production`, plus one shared slot occupied by either `staging` or `testing`. `staging` and `testing` cannot occupy the shared slot at the same time.

A future create flow must reserve its slot atomically before calling the provider. The reservation consumes capacity. Every instance continues to consume capacity until the provider confirms it is absent or destroyed. A create or destroy with an unresolved outcome continues to consume its slot; an unknown create cannot free capacity until provider state confirms absence or destruction. Reject unsupported environments, duplicate or occupied slot requests, and any create that would exceed the total limit.

The repository includes reference cases for available slots, conflicts, unresolved outcomes, the total limit, and confirmed release. From the repository root, run `go run ./cmd/pilot-capacity-check` to validate them against the policy. This Go checker is a declarative reference check; it is not called by the pilot CLI or any provisioning path.

**This policy is declarative only.** The current `cmd/supervised-pilot` CLI supports backup, status, and local history; it has no VM-create path. The policy is not enforced by that CLI and does not constrain actual provisioning yet.

## What success does not prove

The `service_succeeded` audit event confirms only the systemd invocation result. The pilot does not inspect the existing script, backup files, object storage, checksums, or world contents. Do not treat this event as proof that a usable backup exists; verify the backup through the server's existing backup process.

The audit log is stored locally at `~/.local/state/gsc-pilot/operations.jsonl` with owner-only file permissions. Pilot tests use fake SSH; they do not run the service or connect to a live server.
