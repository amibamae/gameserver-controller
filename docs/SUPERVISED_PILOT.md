# Supervised backup pilot

The pilot is a local CLI wrapper around the server's existing `minecraft-backup.service`. It does not install or replace the unit or the script that unit runs. Neither is stored in this repository; the server setup remains the source of truth.

## Commands and remote effects

- `gsc-pilot status --host <ssh-alias>` makes read-only `systemctl show` calls for `minecraft-vanilla.service`, `minecraft-backup.service`, and `minecraft-backup.timer`.
- `gsc-pilot backup --host <ssh-alias>` prints a dry run. It does not connect to the server.
- The only mutating remote command is exactly `sudo -n systemctl start --wait minecraft-backup.service`.
- Before and after an executed backup, the pilot runs read-only `sudo -n systemctl show --property=ActiveState,SubState,Result,ExecMainStatus,InvocationID minecraft-backup.service`.
- The pilot sends no script path, script body, or custom unit arguments.
- `gsc-pilot history` reads the local audit log and makes no SSH connection.

The SSH account needs non-interactive sudo permission for the pilot's `systemctl show` and `systemctl start --wait` commands.

For an executed backup, pass both flags with the same SSH alias:

```sh
gsc-pilot backup --host <ssh-alias> --execute --confirm-host <ssh-alias>
```

## What counts as success

Do not trust `systemctl start --wait`'s exit code by itself. After the command returns, the pilot reads systemd again. It reports service success only when the new invocation ID differs from the previous one, the unit has reached a completed state (`inactive/dead` or `active/exited`), `Result=success`, and `ExecMainStatus=0`.

The pilot records an unknown outcome and will not retry if the post-run invocation ID is missing, malformed, or unchanged; if the unit remains nonterminal, including still running; or if it cannot read the final state. Inspect the service and its logs before you try again.

The pilot does not stop Paper, restore a world, destroy a server, or install or change the backup service.

## Operator handoff

The repository's automated checks and this checklist do not claim that a live backup has been run. This is a supervised handoff, not a live-server validation report. The one-managed-VM policy below is declarative; the pilot CLI does not enforce it.

Before you execute:

1. Confirm the SSH alias reaches the intended server and review its host key.
2. Check that the account has non-interactive sudo for the read-only `systemctl show` calls and the exact backup start command.
3. Run `gsc-pilot status --host <ssh-alias>` and review the game, backup, and timer unit states.
4. Run `gsc-pilot backup --host <ssh-alias>` without `--execute`. Review the service and target in the dry-run output.
5. Only then run the command with both flags and the same alias:

   ```sh
   gsc-pilot backup --host <ssh-alias> --execute --confirm-host <ssh-alias>
   ```

6. Review the result and run `gsc-pilot history` as the same local user. If the result is `outcome_unknown`, do not retry; inspect service state and logs through the server's existing process first.
7. After systemd reports success, verify that the expected backup artifacts exist. Then use the existing backup process to check storage and integrity. The `service_succeeded` event confirms the systemd invocation only.

## Declarative VM capacity policy

The [capacity policy file](../pilot/capacity-policy.json) sets an initial hard limit of **one controller-owned VM**. That single slot can serve production or one shared `staging`/`testing` VM. A live production VM may remain unmanaged and does not consume controller-owned capacity; production adoption is optional. A Vultr instance created by the controller counts as managed. Staging and testing share the one slot, so the controller cannot manage both at once.

A future Vultr create/adopt path must reserve a slot atomically before calling the provider. The reservation consumes capacity. Each controller-owned instance continues to count until Vultr confirms it is absent or destroyed. An unresolved create or destroy also consumes the slot. For an unknown create result, do not free capacity until provider state confirms absence or destruction. Reject unsupported environments, duplicate or occupied slot requests, and requests that exceed the limit.

The intended hard-coded cap lives in source-controlled
`pilot/capacity-policy.json`; it is not a runtime CLI flag or Go constant.
Once the Vultr create/adopt path consumes this policy, maintainers can change
the integer `max_instances_total`, increment `policy_version`, validate it
with `go run ./cmd/pilot-capacity-check` from the repository root, and rebuild
the pilot with the new cap. The total cannot exceed the sum of the declared
per-environment slots. The checker covers free slots, unmanaged production,
conflicts, unresolved outcomes, and confirmed release.

Today, the checker validates the policy file, but the backup/status CLI has no
Vultr create/adopt path. Editing the file and rebuilding the current CLI does
not change runtime behavior yet. The future provider workflow must read the
checked-in policy before it calls the API.

**The policy is declarative only today.** The current `cmd/supervised-pilot` CLI supports backup, status, and local history. It cannot create or adopt Vultr instances.

## What the success event proves

The `service_succeeded` audit event records a successful systemd invocation. It does not inspect the backup script, files, object storage, checksums, or world contents. Use the server's existing backup process to verify that a usable backup exists.

The local audit log is `~/.local/state/gsc-pilot/operations.jsonl` with owner-only file permissions. Pilot tests use fake SSH; they do not run the service or connect to a live server.
