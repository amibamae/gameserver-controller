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

## What success does not prove

The `service_succeeded` audit event confirms only the systemd invocation result. The pilot does not inspect the existing script, backup files, object storage, checksums, or world contents. Do not treat this event as proof that a usable backup exists; verify the backup through the server's existing backup process.

The audit log is stored locally at `~/.local/state/gsc-pilot/operations.jsonl` with owner-only file permissions. Pilot tests use fake SSH; they do not run the service or connect to a live server.
