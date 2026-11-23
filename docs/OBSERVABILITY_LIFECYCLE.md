# Opt-in observability lifecycle follow-up

**All VictoriaLogs and Homelable operations are disabled unless explicitly enabled.** This implementation provides tools and example systemd units, not an automatic installation or production deployment. No log collector, topology reconciler, or network call participates in the server's health gate, world operations, backup safety, or routing.

## VictoriaLogs: automatic setup during a confirmed VM handoff

The existing optional `gsc-pilot observability` command still works independently. To avoid needing a second manual command, add `--enable-victorialogs` to an **already authorized** `gsc-pilot handoff` invocation:

```sh
gsc-pilot handoff \
  --operation-id <provision-operation-id> \
  --wasabi-region us-east-1 \
  --execute --confirm-handoff <server-id>@staging/<instance-id> \
  --enable-victorialogs \
  --logs-privacy lifecycle \
  --logs-endpoint https://logs.example.invalid/insert/jsonline
```

Other required handoff credentials/flags remain unchanged. The controller applies the already supported private configuration step **after** a successful pinned-SSH handoff and Wasabi restore, using the same exact instance confirmation. The endpoint must be HTTPS and end in `/insert/jsonline`. Its private ingestion token must exist in the controller's `gsc-victorialogs-ingest-token` systemd credential store. The token is delivered only through authenticated SSH stdin, not through cloud-init, flags, source code or logs.

- **Default:** no `--enable-victorialogs` means no log credential access, provider lookup, SSH configuration or exporter activation.
- A dry-run handoff does not configure anything, even if opt-in is requested.
- `--logs-endpoint`, `--logs-privacy` and `--logs-credential-ref` are rejected on the handoff command unless opt-in was explicitly selected.
- Automatic setup permits `lifecycle` privacy only. Raw gameplay, chat, IPs and player identifiers stay excluded.
- If log configuration fails after a successful handoff, the error is reported as a generic **warning**, not a failed game deployment. Retry separately with `gsc-pilot observability`; a failed logging service does not stop the VM, restore or gameplay.

No change to automatic destruction policy. VM destruction remains disabled unless separately authorized under the pre-existing lifecycle rules.

## VictoriaLogs: persistent controller-side collection

The original `victorialogs_bridge.py` remains a one-shot filter. The new `victorialogs_collector.py` collects up to 250 journald records per invocation from an explicitly selected controller service, extracts only the existing allowlisted `integration.proxy.*` JSON events, and sends them to the configured HTTPS ingestion gateway. It never forwards raw journald entries.

The collector reads a systemd-loaded ingestion token, writes a private journal cursor in `$STATE_DIRECTORY/cursor`, and **advances it only after an acknowledged batch**. Failed ingestion replays the same entries on the next run, so the delivery contract is at-least-once; duplicate events are possible if the process crashes after upload but before its cursor is saved. An expired or corrupt cursor fails closed and needs operator review rather than silently skipping older records. Journal retention must be long enough for expected collector outages.

Example units are in [configs/systemd](../configs/systemd/):

- `gsc-victorialogs-controller.service.example`
- `gsc-victorialogs-controller.timer.example`

They are examples only. To deploy, inspect and adjust the paths/service name and HTTPS gateway URL, install a dedicated root-private `/etc/gsc-observability/controller.token`, copy the files to systemd under names ending `.service` and `.timer`, then run `systemctl daemon-reload`. **Only then**, if desired, explicitly enable the timer. Do not enable its service independently. Enforce the dedicated bearer token at the HTTPS gateway; VictoriaLogs' native endpoint must not be publicly exposed.

## Homelable: managed-only VM state, never topology authority

`go run ./cmd/homelable-inventory --state-db /path/to/existing/pilot.db --design-id <existing-design-id>` exports the locally recorded provider state as JSON **without calling Vultr or Homelable**. No IPs, credentials or player information are included. It exports a VM only when the provider instance is known. A confirmed absent or destroyed instance is `retired`; an uncertain destruction remains `active`. A missing entry never causes deletion or inferred retirement.

Use `homelable_lifecycle.py --url https://homelable.internal --snapshot /path/to/snapshot.json` for an entirely offline preview. The `--apply --token-file /private/jwt` form is opt-in. On apply, the script:

1. Reads existing nodes in the designated Homelable design.
2. Identifies owned nodes by the exact, controller-generated `gsc-managed-operation:<operation-id>` marker in `notes`.
3. Refuses ambiguous identities or a manually created node with the same label.
4. Creates missing **active** managed nodes, or patches **only status** (`online` for confirmed-present, `offline` for retired). `online` means provider presence, **not** confirmed Paper readiness.
5. Never sends `DELETE`, edits a foreign node, or uses the destructive whole-canvas `canvas/save` API. Missing retired nodes are not recreated.

A retired node stays visible as historical topology. Manually changing or removing its ownership marker stops automated updates. A token expiry or API outage only leaves topology stale; it cannot gate Minecraft. Homelable's local JWTs expire after approximately 24 hours by default; renew the narrowly controlled token as needed and keep the UI/API on a private network. <https://github.com/Pouzor/homelable/blob/main/docs/api.md>

For operator-owned periodic reconciliation, build the inventory command into a local binary and configure the example systemd units:

```sh
go build -o /usr/local/bin/gsc-homelable-inventory ./cmd/homelable-inventory
```

Install `configs/systemd/gsc-homelable-lifecycle.service.example` and the paired timer **only when explicitly enabling this integration**. Set a root-private `/etc/gsc-observability/homelable.env` with:

```sh
GSC_STATE_DB=/home/minecraft-controller/.local/state/gsc-pilot/pilot.db
GSC_HOMELABLE_URL=https://homelable.example.invalid
GSC_HOMELABLE_DESIGN_ID=existing-design-id
GSC_INVENTORY_BIN=/usr/local/bin/gsc-homelable-inventory
GSC_REPO_DIR=/opt/gameserver-controller
```

Use a private `/etc/gsc-observability/homelable.jwt` for the short-lived JWT, a service user permitted to read the **existing pilot state database**, and a network boundary restricting Homelable to your trusted network. The wrapper exports a private temporary snapshot and deletes it after synchronization. The service and timer are not activated by installation or by the GitHub merge.

## Staging and recovery validation

CI runs synthetic/offline failure tests only; this branch does **not** provision billable or real Vultr instances. The tests cover: an impostor's SSH host key, an already-destroyed instance, missing or malformed credentials/policies, opt-out and dry-run with zero calls, failed VictoriaLogs ingestion and journal replay, malformed saved cursors, log privacy filtering, topology ownership conflicts, retired instances and API non-destructiveness.

Before connecting any real staging infrastructure, use a disposable, isolated staging VM and synthetic log messages. Verify the externally enforced HTTPS ingestion token, firewall boundaries, SSH authentication refusal, log retention and cursor replay after network interruption, journal pruning and VM deletion. Review the results before enabling production. A disappearance is not a signal to delete a Homelable node or modify any backup/destroy decision.
